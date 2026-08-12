package wa

import (
	"encoding/binary"
	"io"
	"testing"

	meowcaller "github.com/purpshell/meowcaller"
)

// frameOf builds one s16le frame filled with v.
func frameOf(v int16) []byte {
	b := make([]byte, frameBytes)
	for i := 0; i < meowcaller.FrameSamples; i++ {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func TestSourceHoldsSilenceUntilPrimed(t *testing.T) {
	s := newStreamSource()
	s.Push(frameOf(1000))

	// One frame is below the prefill mark, so playback has not started.
	f, err := s.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f[0] != 0 {
		t.Fatalf("expected silence before prefill, got %v", f[0])
	}

	s.Push(frameOf(1000))
	s.Push(frameOf(1000))
	f, err = s.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f[0] == 0 {
		t.Fatal("expected audio once primed, got silence")
	}
}

func TestSourceUnderrunIsSilenceNotEOF(t *testing.T) {
	s := newStreamSource()
	for i := 0; i < defaultPrefillFrames; i++ {
		s.Push(frameOf(500))
	}
	for i := 0; i < defaultPrefillFrames; i++ {
		if _, err := s.ReadFrame(); err != nil {
			t.Fatalf("drain %d: %v", i, err)
		}
	}

	// An empty queue mid-call must not end the Player.
	f, err := s.ReadFrame()
	if err != nil {
		t.Fatalf("underrun returned error: %v", err)
	}
	if f[0] != 0 {
		t.Fatal("underrun should yield silence")
	}
	if got := s.underruns.Load(); got != 1 {
		t.Fatalf("underruns = %d, want 1", got)
	}
}

func TestSourceEOFOnlyAfterCloseAndDrain(t *testing.T) {
	s := newStreamSource()
	for i := 0; i < defaultPrefillFrames; i++ {
		s.Push(frameOf(700))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i := 0; i < defaultPrefillFrames; i++ {
		if _, err := s.ReadFrame(); err != nil {
			t.Fatalf("queued frame %d should still play: %v", i, err)
		}
	}
	if _, err := s.ReadFrame(); err != io.EOF {
		t.Fatalf("after close and drain got %v, want io.EOF", err)
	}
}

func TestSourceReassemblesUnalignedChunks(t *testing.T) {
	s := newStreamSource()
	full := frameOf(2000)

	// Network chunks will not align to 1920 bytes; the carry buffer has to bridge them.
	s.Push(full[:100])
	if d := s.Depth(); d != 0 {
		t.Fatalf("partial frame should not enqueue, depth = %d", d)
	}
	s.Push(full[100:])
	if d := s.Depth(); d != 1 {
		t.Fatalf("completed frame should enqueue, depth = %d", d)
	}

	// A chunk spanning a frame boundary yields one frame and keeps the remainder.
	s.Push(append(append([]byte{}, full...), full[:64]...))
	if d := s.Depth(); d != 2 {
		t.Fatalf("depth = %d, want 2", d)
	}
	s.Push(full[64:])
	if d := s.Depth(); d != 3 {
		t.Fatalf("depth = %d, want 3", d)
	}
}

func TestSourceFlushIsBargeIn(t *testing.T) {
	s := newStreamSource()
	for i := 0; i < 10; i++ {
		s.Push(frameOf(3000))
	}
	s.Push(frameOf(3000)[:200]) // partial, must go too

	s.Flush()

	if d := s.Depth(); d != 0 {
		t.Fatalf("depth after flush = %d, want 0", d)
	}
	f, err := s.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f[0] != 0 {
		t.Fatal("first frame after barge-in should be silence")
	}
	// The half-syllable left in carry must not resurface on the next push.
	s.Push(frameOf(1234)[:frameBytes-200])
	if d := s.Depth(); d != 0 {
		t.Fatalf("stale carry survived flush, depth = %d", d)
	}
}

func TestSourceDropsOldestOverCeiling(t *testing.T) {
	s := newStreamSource()
	for i := 0; i < maxQueueFrames+5; i++ {
		s.Push(frameOf(int16(i + 1)))
	}
	if d := s.Depth(); d != maxQueueFrames {
		t.Fatalf("depth = %d, want %d", d, maxQueueFrames)
	}
	if got := s.dropped.Load(); got != 5 {
		t.Fatalf("dropped = %d, want 5", got)
	}
	// Oldest went first, so the head is frame 6.
	f, err := s.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if want := float32(6) / 32768; f[0] != want {
		t.Fatalf("head sample = %v, want %v", f[0], want)
	}
}

func TestPCMRoundTrip(t *testing.T) {
	in := []int16{0, 1, -1, 16384, -16384, 32767, -32768}
	b := make([]byte, len(in)*2)
	for i, v := range in {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}

	back := floatToPCM(pcmToFloat(b))
	for i := range in {
		got := int16(binary.LittleEndian.Uint16(back[i*2:]))
		if diff := int(got) - int(in[i]); diff > 2 || diff < -2 {
			t.Fatalf("sample %d: got %d, want ~%d", i, got, in[i])
		}
	}
}

func TestFloatToPCMClamps(t *testing.T) {
	// Values outside [-1,1] must saturate, not wrap into the opposite sign.
	out := floatToPCM([]float32{2.5, -2.5})
	if got := int16(binary.LittleEndian.Uint16(out[0:])); got < 32000 {
		t.Fatalf("positive overflow wrapped: %d", got)
	}
	if got := int16(binary.LittleEndian.Uint16(out[2:])); got > -32000 {
		t.Fatalf("negative overflow wrapped: %d", got)
	}
}

func TestSinkDeliversAndDrops(t *testing.T) {
	s := newStreamSink()
	frame := make([]float32, meowcaller.FrameSamples)
	frame[0] = 0.5

	if err := s.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	select {
	case b := <-s.Frames():
		if len(b) != frameBytes {
			t.Fatalf("frame len = %d, want %d", len(b), frameBytes)
		}
	default:
		t.Fatal("frame was not delivered")
	}

	// Overfilling must not block the codec thread.
	for i := 0; i < sinkQueueFrames+10; i++ {
		if err := s.WriteFrame(frame); err != nil {
			t.Fatalf("WriteFrame %d: %v", i, err)
		}
	}
	if got := s.dropped.Load(); got != 10 {
		t.Fatalf("dropped = %d, want 10", got)
	}
}

func TestSinkCloseIsIdempotent(t *testing.T) {
	s := newStreamSink()
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Writing after close is a no-op rather than a panic on a closed channel.
	if err := s.WriteFrame(make([]float32, meowcaller.FrameSamples)); err != nil {
		t.Fatalf("WriteFrame after close: %v", err)
	}
}
