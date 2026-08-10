package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
)

// The media half of calling, delegated to meowcaller.
//
// wacli's own call stack is in _research/callstack: it solved signalling, relay allocation, the SSRC
// derivation, the e2e media key and SRTP, and finally the codec (WhatsApp 1:1 calls are MLow, not
// Opus). What it never had was the outbound path. A call we place gets its relay allocation in
// <ack class="call" type="offer">, and whatsmeow has no ack handler at all — client.go drops the
// node without even a debug line — so a caller never learns which relay to allocate on.
//
// meowcaller intercepts that ack, and implements the rest of the stack besides. It is pure Go, MIT
// licensed, and builds on the same upstream whatsmeow this repo already pins, so it drops in.
//
// One ordering rule matters: meowcaller.NewClient installs handlers by reaching into whatsmeow's
// unexported nodeHandlers map, and refuses to do so once the client is connected. It must therefore
// be constructed *before* Connect — see NewService.

// callMedia owns the meowcaller client and the calls currently live on it.
//
// meowcaller's OnReady/OnEnd/OnStateChange are single-slot setters, not listener lists — registering
// twice silently discards the first. Every one of them is therefore set exactly once, in track().
type callMedia struct {
	client *meowcaller.Client

	mu   sync.Mutex
	live map[string]*liveCall
}

// liveCall is a call in progress plus the cleanups owed when it ends.
type liveCall struct {
	call    *meowcaller.Call
	cleanup []func()
}

// AudioRequest describes a call's audio in both directions. Say and File are mutually exclusive.
type AudioRequest struct {
	// Say is spoken with the system speech synthesiser.
	Say string
	// Voice picks the synthesiser voice; empty means the system default.
	Voice string
	// File is a .wav/.mp3/.opus to play instead of Say.
	File string
	// Repeat restarts the audio when it finishes instead of hanging up.
	Repeat bool
	// Record is a path to write the other party's voice to, as a 16 kHz mono WAV.
	Record string
}

// nothingToPlay reports whether there is no outbound audio. Recording is independent: a call can
// record the peer without sending anything.
func (a AudioRequest) nothingToPlay() bool { return a.Say == "" && a.File == "" }

// newCallMedia wires meowcaller onto an unconnected whatsmeow client.
func newCallMedia(s *Service, log zerolog.Logger) *callMedia {
	m := &callMedia{live: map[string]*liveCall{}}
	m.client = meowcaller.NewClient(s.client, meowcaller.WithLogger(log))
	m.client.OnIncomingCall(func(call *meowcaller.Call) {
		m.track(s, call)
		s.log.Infof("incoming call %s from %s — answer it with: wacli call answer --say '...'",
			call.ID(), call.Peer())
	})
	return m
}

// track remembers a live call, sets its one-shot listeners, and forgets it once it ends.
func (m *callMedia) track(s *Service, call *meowcaller.Call) {
	id := call.ID()
	m.mu.Lock()
	m.live[id] = &liveCall{call: call}
	m.mu.Unlock()

	call.OnStateChange(func(p meowcaller.CallPhase) {
		s.log.Infof("call %s: phase %v", id, p)
	})
	// OnReady fires on the first RTP decoded *from* the relay, i.e. it reports inbound audio, not
	// outbound readiness. Outbound playback must never wait on it — see attachAudio.
	call.OnReady(func() { s.log.Infof("call %s: inbound audio flowing", id) })
	call.OnEnd(func(reason string) {
		m.mu.Lock()
		lc := m.live[id]
		delete(m.live, id)
		m.mu.Unlock()
		if lc != nil {
			for _, fn := range lc.cleanup {
				fn()
			}
		}
		s.log.Infof("call %s ended: %s", id, reason)
		// Free the call slot and dial whatever is queued behind this one.
		s.queue.finished(s, id)
	})
}

// onEnd registers work to run when the call ends. OnEnd itself is claimed by track().
func (m *callMedia) onEnd(id string, fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lc, ok := m.live[id]; ok {
		lc.cleanup = append(lc.cleanup, fn)
	}
}

// get returns a live call by id, or the only one if id is empty.
func (m *callMedia) get(id string) (*meowcaller.Call, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != "" {
		if lc, ok := m.live[id]; ok {
			return lc.call, nil
		}
		return nil, fmt.Errorf("no live call %s", id)
	}
	switch len(m.live) {
	case 0:
		return nil, errors.New("no live call")
	case 1:
		for _, lc := range m.live {
			return lc.call, nil
		}
	}
	return nil, fmt.Errorf("%d calls are live — pass --id", len(m.live))
}

// attachAudio prepares the request and returns the func that starts playing it.
//
// It deliberately does not use OnReady. That listener fires on the first RTP decoded *from* the
// relay — inbound audio — so gating playback on it means we stay silent until the peer's audio
// reaches us. meanwhile meowcaller sends silence to hold the relay bridge, and the call connects
// and stays mute. It is also a potential deadlock: a peer that waits for our media before sending
// its own leaves both sides silent forever.
//
// Subscribing early is safe instead. The engine pulls the player every frame, so frames are
// consumed only while media is actually being sent — nothing is lost by attaching sooner. The
// caller decides when: on peer-accept for an outgoing call, right after Answer for an incoming one.
func (m *callMedia) attachAudio(s *Service, call *meowcaller.Call, req AudioRequest, onEvent func(string)) (func(), error) {
	if req.Record != "" {
		if err := m.attachRecorder(s, call, req.Record, onEvent); err != nil {
			return nil, err
		}
	}
	if req.nothingToPlay() {
		return func() {}, nil
	}
	path, cleanup, err := req.resolve()
	if err != nil {
		return nil, err
	}
	// Open once up front so a bad file fails the command rather than a silent call.
	if probe, err := openAudioSource(path); err != nil {
		cleanup()
		return nil, err
	} else {
		_ = probe.Close()
	}
	m.onEnd(call.ID(), cleanup)

	// Each Play returns a *new* Player, so the finish handler has to be re-registered on every pass
	// or the loop stops after one repeat.
	var play func()
	play = func() {
		src, err := openAudioSource(path)
		if err != nil {
			onEvent(fmt.Sprintf("open audio: %v", err))
			return
		}
		player := call.Play(src)
		player.OnFinish(func() {
			if !req.Repeat {
				onEvent("audio finished — hanging up")
				_ = call.Hangup()
				return
			}
			play()
		})
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			onEvent("playing audio")
			play()
		})
	}, nil
}

// attachRecorder writes the other party's voice to a 16 kHz mono WAV for the length of the call.
//
// The sink must be closed when the call ends: the recorder writes a placeholder RIFF header up
// front and only rewrites the real data length on Close, so an unclosed file reads as empty however
// much audio it actually contains.
func (m *callMedia) attachRecorder(s *Service, call *meowcaller.Call, path string, onEvent func(string)) error {
	// Resolve against the daemon's working directory once, up front. The path arrives as text over
	// the local API, so a relative one would otherwise land wherever the daemon happens to have been
	// started — not where the user ran the command — and be reported back just as ambiguously.
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	sink, err := meowcaller.WAVRecorder(path)
	if err != nil {
		return fmt.Errorf("record to %s: %w", path, err)
	}
	counted := &countingSink{inner: sink}
	call.Receive(counted)
	s.calls.setRecording(call.ID(), path)
	m.onEnd(call.ID(), func() {
		if err := counted.Close(); err != nil {
			onEvent(fmt.Sprintf("close recording: %v", err))
			return
		}
		frames := counted.frames.Load()
		if frames == 0 {
			// Worth calling out rather than leaving a silent 44-byte file to be puzzled over:
			// nothing arriving means the inbound leg never carried audio, not that we failed to write.
			onEvent(fmt.Sprintf("recorded nothing to %s — no inbound audio arrived", path))
			return
		}
		secs := float64(frames*meowcaller.FrameSamples) / float64(meowcaller.SampleRate)
		s.calls.setRecorded(call.ID(), secs)
		onEvent(fmt.Sprintf("recorded %.1fs of the other party to %s", secs, path))
	})
	onEvent("recording the other party to " + path)
	return nil
}

// countingSink counts frames on their way to the recorder, so the end of a call can report whether
// any inbound audio actually arrived.
type countingSink struct {
	inner  meowcaller.AudioSink
	frames atomic.Int64
}

func (c *countingSink) WriteFrame(frame []float32) error {
	c.frames.Add(1)
	return c.inner.WriteFrame(frame)
}

func (c *countingSink) Close() error { return c.inner.Close() }

// resolve turns the request into a playable file, synthesising speech if needed. The returned
// cleanup removes anything it created.
func (a AudioRequest) resolve() (string, func(), error) {
	if a.File != "" {
		if _, err := os.Stat(a.File); err != nil {
			return "", func() {}, fmt.Errorf("audio file: %w", err)
		}
		return a.File, func() {}, nil
	}
	path, err := synthesiseSpeech(a.Say, a.Voice)
	if err != nil {
		return "", func() {}, err
	}
	return path, func() { os.Remove(path) }, nil
}

// openAudioSource picks a decoder by extension. meowcaller resamples and downmixes to the 16 kHz
// mono the call needs, so any sample rate or channel count works.
func openAudioSource(path string) (meowcaller.AudioSource, error) {
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".wav":
		return meowcaller.WAVFile(path)
	case ".mp3":
		return meowcaller.MP3File(path)
	case ".opus", ".ogg":
		return meowcaller.OpusFile(path)
	default:
		return nil, fmt.Errorf("unsupported audio extension %q (want .wav/.mp3/.opus)", ext)
	}
}

// synthesiseSpeech renders text to a WAV with the macOS speech synthesiser, so --say needs nothing
// installed. afconvert is only involved because `say` writes AIFF.
func synthesiseSpeech(text, voice string) (string, error) {
	if _, err := exec.LookPath("say"); err != nil {
		return "", errors.New("--say needs the macOS `say` command; pass --audio <file> instead")
	}
	aiff, err := os.CreateTemp("", "wacli-say-*.aiff")
	if err != nil {
		return "", err
	}
	aiff.Close()
	defer os.Remove(aiff.Name())

	args := []string{"-o", aiff.Name()}
	if voice != "" {
		args = append(args, "-v", voice)
	}
	args = append(args, text)
	if out, err := exec.Command("say", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("say: %v: %s", err, strings.TrimSpace(string(out)))
	}

	wav := strings.TrimSuffix(aiff.Name(), ".aiff") + ".wav"
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1",
		aiff.Name(), wav).CombinedOutput(); err != nil {
		return "", fmt.Errorf("afconvert: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return wav, nil
}

// AnswerWithAudio accepts a ringing call and speaks into it.
func (s *Service) AnswerWithAudio(ref string, req AudioRequest) (string, error) {
	if s.media == nil {
		return "", errors.New("media stack is not initialised")
	}
	// Accept a short handle, a full call ID, or a unique prefix — but fall back to the media layer's
	// own view, since an offer can reach meowcaller before wacli's registry sees the event.
	callID := ref
	if resolved, err := s.calls.resolve(ref); err == nil {
		callID = resolved
	}
	call, err := s.media.get(callID)
	if err != nil {
		return "", err
	}
	// Answering is refused rather than queued while another call holds the slot: by the time the
	// slot freed, this caller would long since have hung up.
	if !s.queue.acquire() {
		return "", fmt.Errorf("another call is active (%s) — end it first", s.queue.activeCall())
	}
	s.queue.hold(call.ID())

	start, err := s.media.attachAudio(s, call, req, func(msg string) {
		s.log.Infof("call %s: %s", call.ID(), msg)
	})
	if err != nil {
		s.queue.finished(s, call.ID())
		return "", err
	}
	if err := call.Answer(); err != nil {
		s.queue.finished(s, call.ID())
		return "", fmt.Errorf("answer: %w", err)
	}
	s.log.Infof("answered call %s from %s", call.ID(), call.Peer())
	// We are the one accepting, so there is no peer-accept to wait for: subscribe now and let the
	// engine pull frames as soon as it starts sending.
	start()
	return call.ID(), nil
}

// HangupMedia ends a live media call.
func (s *Service) HangupMedia(callID string) error {
	if s.media == nil {
		return errors.New("media stack is not initialised")
	}
	call, err := s.media.get(callID)
	if err != nil {
		return err
	}
	return call.Hangup()
}

