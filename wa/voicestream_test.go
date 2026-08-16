package wa

import (
	"testing"
)

// Synthesis chunks are not frame-aligned — measured on a live call, not one
// in a hundred was. Every byte must still reach the queue: the tail of one
// chunk joins the head of the next.
func TestPushLosesNothingAcrossUnalignedChunks(t *testing.T) {
	src := newStreamSource()
	src.prefill = 0
	// The chunk sizes seen from Sarvam on a real reply.
	sizes := []int{9628, 6424, 3212, 3212, 3212, 6424, 3212, 1292}
	total := 0
	for _, n := range sizes {
		src.Push(make([]byte, n))
		total += n
	}
	// Whole frames queued; the remainder is carried, not dropped.
	wantFrames := total / frameBytes
	if got := src.Depth(); got != wantFrames {
		t.Fatalf("queued %d frames, want %d (from %d bytes)", got, wantFrames, total)
	}
	if carry := len(src.carry); carry != total%frameBytes {
		t.Fatalf("carried %d bytes, want %d", carry, total%frameBytes)
	}
	// Topping up completes the carried frame.
	src.Push(make([]byte, frameBytes-total%frameBytes))
	if got := src.Depth(); got != wantFrames+1 {
		t.Fatalf("after top-up queued %d frames, want %d", got, wantFrames+1)
	}
}

// Pause holds the reply in place: the caller hears silence, nothing is lost,
// and Resume continues from the same frame. Flush is the other outcome.
func TestPauseHoldsPlaybackWithoutLosingIt(t *testing.T) {
	src := newStreamSource()
	src.prefill = 0
	for i := 0; i < 5; i++ {
		src.Push(make([]byte, frameBytes))
	}
	if _, err := src.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	src.Pause()
	for i := 0; i < 3; i++ {
		src.ReadFrame() // silence, not consumption
	}
	if got := src.Depth(); got != 4 {
		t.Fatalf("paused playback consumed frames: depth %d, want 4", got)
	}
	src.Resume()
	src.ReadFrame()
	if got := src.Depth(); got != 3 {
		t.Fatalf("resume did not continue: depth %d, want 3", got)
	}
	src.Pause()
	src.Flush()
	if got := src.Depth(); got != 0 {
		t.Fatalf("flush left %d frames", got)
	}
	// Flush also lifts the pause, or the next reply would play into silence.
	src.Push(make([]byte, frameBytes))
	if _, err := src.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	if got := src.Depth(); got != 0 {
		t.Fatalf("still paused after flush: depth %d", got)
	}
}
