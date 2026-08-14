package wa

import (
	"encoding/binary"
	"io"
	"sync"
	"sync/atomic"

	meowcaller "github.com/purpshell/meowcaller"
)

// Duplex streaming audio, the substrate for AI-driven calls.
//
// The file-based AudioRequest in callmedia.go plays a fixed source and records the peer to a WAV —
// an answering machine. A conversation needs the opposite shape: frames arriving from the network
// while the peer's frames leave for it, both on the codec's 60 ms cadence, with the ability to cut
// playback mid-sentence when the peer starts talking.
//
// meowcaller makes that reachable because AudioSource and AudioSink are interfaces, not just the
// WAV/MP3/Opus helpers. streamSource and streamSink below are the two halves.
//
// Format is fixed throughout: 16 kHz mono, 960 samples per frame, float32 in meowcaller and
// signed 16-bit little-endian on the wire. Sarvam speaks exactly that in both directions, so
// nothing in this path resamples.

const (
	// frameBytes is one 60 ms frame as s16le.
	frameBytes = meowcaller.FrameSamples * 2

	// defaultPrefillFrames is how much audio buffers before playback starts, absorbing jitter on
	// the way in. Three frames is 180 ms — enough to ride out a hiccup, short enough not to be
	// heard as a delay.
	defaultPrefillFrames = 3

	// maxQueueFrames caps the playback queue at ~45 s of speech.
	//
	// It was ~6 s, on the theory that a producer outrunning the call meant
	// something was wrong. But synthesis is SUPPOSED to outrun the call: a
	// whole reply arrives in a burst a few seconds long, so any reply with more
	// than six seconds of audio overflowed the queue and dropping-the-oldest
	// spliced chunks out of the middle of the sentence — heard as a glitchy
	// voice on long answers and a clean one on short answers. Latency is not
	// the queue's job here: barge-in flushes it the moment the caller speaks,
	// so depth costs nothing when it matters.
	maxQueueFrames = 750

	// sinkQueueFrames is the uplink buffer, ~3 s. WriteFrame runs on the codec thread and must
	// never block, so a full queue drops rather than waits.
	sinkQueueFrames = 50
)

// streamSource feeds network audio into a call.
//
// It never returns io.EOF while the call is live: an exhausted source makes the Player fire
// OnFinish and go idle, so an underrun yields a silent frame instead. EOF is reserved for Close,
// which is the only thing that should end playback.
type streamSource struct {
	mu      sync.Mutex
	queue   [][]float32
	carry   []byte
	prefill int
	primed  bool
	closed  bool

	silence []float32

	underruns atomic.Int64
	dropped   atomic.Int64
}

func newStreamSource() *streamSource {
	return &streamSource{
		prefill: defaultPrefillFrames,
		silence: make([]float32, meowcaller.FrameSamples),
	}
}

// ReadFrame is pulled by the Player every 60 ms.
func (s *streamSource) ReadFrame() ([]float32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed && len(s.queue) == 0 {
		return nil, io.EOF
	}
	// Hold silence until enough has arrived to play through the next gap.
	if !s.primed {
		if len(s.queue) < s.prefill {
			return s.silence, nil
		}
		s.primed = true
	}
	if len(s.queue) == 0 {
		s.underruns.Add(1)
		s.primed = false
		return s.silence, nil
	}

	frame := s.queue[0]
	s.queue[0] = nil
	s.queue = s.queue[1:]
	return frame, nil
}

// Push queues s16le PCM of any length, buffering whatever does not fill a frame.
func (s *streamSource) Push(pcm []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}

	s.carry = append(s.carry, pcm...)
	for len(s.carry) >= frameBytes {
		s.enqueue(pcmToFloat(s.carry[:frameBytes]))
		s.carry = s.carry[frameBytes:]
	}
	// Reclaim the backing array once it has been fully consumed.
	if len(s.carry) == 0 && cap(s.carry) > frameBytes*4 {
		s.carry = nil
	}
}

// PushFrames queues already-decoded frames, used for cached scripted lines.
func (s *streamSource) PushFrames(frames [][]float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for _, f := range frames {
		s.enqueue(f)
	}
}

// enqueue appends one frame, dropping the oldest if the queue is over its ceiling.
func (s *streamSource) enqueue(frame []float32) {
	if len(s.queue) >= maxQueueFrames {
		s.queue[0] = nil
		s.queue = s.queue[1:]
		s.dropped.Add(1)
	}
	s.queue = append(s.queue, frame)
}

// Flush discards queued audio so the next frame is silence.
//
// This is barge-in: the peer has started talking, and continuing to speak over them is the single
// most robotic thing a voice agent can do. The partial frame in carry goes too — replaying half a
// syllable after the interruption sounds worse than the cut.
func (s *streamSource) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.queue {
		s.queue[i] = nil
	}
	s.queue = s.queue[:0]
	s.carry = nil
	s.primed = false
}

// Depth reports queued frames, for pacing decisions and diagnostics.
func (s *streamSource) Depth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// Close ends playback once the queue drains.
func (s *streamSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// streamSink carries the peer's audio to the network.
//
// WriteFrame is called on meowcaller's decode path, so it hands off to a buffered channel and
// returns. Blocking here would stall the codec; a full channel means the uplink is not keeping up,
// and dropping the newest frame is better than stalling every frame behind it.
type streamSink struct {
	out    chan []byte
	closed atomic.Bool
	once   sync.Once

	frames  atomic.Int64
	dropped atomic.Int64
}

func newStreamSink() *streamSink {
	return &streamSink{out: make(chan []byte, sinkQueueFrames)}
}

// WriteFrame consumes one decoded frame from the peer.
func (s *streamSink) WriteFrame(frame []float32) error {
	if s.closed.Load() {
		return nil
	}
	s.frames.Add(1)
	select {
	case s.out <- floatToPCM(frame):
	default:
		s.dropped.Add(1)
	}
	return nil
}

// Frames returns the channel the uplink pump drains.
func (s *streamSink) Frames() <-chan []byte { return s.out }

// Close stops accepting frames and closes the channel exactly once.
func (s *streamSink) Close() error {
	s.once.Do(func() {
		s.closed.Store(true)
		close(s.out)
	})
	return nil
}

// pcmToFloat decodes one s16le frame to the float32 the codec wants.
func pcmToFloat(b []byte) []float32 {
	out := make([]float32, len(b)/2)
	for i := range out {
		out[i] = float32(int16(binary.LittleEndian.Uint16(b[i*2:]))) / 32768
	}
	return out
}

// floatToPCM encodes one frame as s16le, clamping rather than wrapping on overflow.
func floatToPCM(frame []float32) []byte {
	out := make([]byte, len(frame)*2)
	for i, v := range frame {
		switch {
		case v > 1:
			v = 1
		case v < -1:
			v = -1
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(v*32767)))
	}
	return out
}
