package mobile

import "testing"

func TestAddCachedLineValidates(t *testing.T) {
	t.Cleanup(ClearCachedLines)

	if err := AddCachedLine("", []byte{0, 0}); err == nil {
		t.Fatal("empty id should be rejected")
	}
	// s16le means an even byte count; an odd one is a truncated sample, not audio.
	if err := AddCachedLine("greeting", []byte{0, 0, 0}); err == nil {
		t.Fatal("odd byte count should be rejected")
	}
	if err := AddCachedLine("greeting", []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("valid line rejected: %v", err)
	}
}

func TestAddCachedLineCopiesInput(t *testing.T) {
	t.Cleanup(ClearCachedLines)

	// The host's array may be a reused JNI view, so the stored copy must not alias it.
	pcm := []byte{1, 2, 3, 4}
	if err := AddCachedLine("greeting", pcm); err != nil {
		t.Fatalf("AddCachedLine: %v", err)
	}
	pcm[0] = 99

	cacheMu.Lock()
	got := cachedLines["greeting"][0]
	cacheMu.Unlock()
	if got != 1 {
		t.Fatalf("stored line aliases caller's slice: first byte = %d", got)
	}
}

func TestClearCachedLines(t *testing.T) {
	if err := AddCachedLine("a", []byte{0, 0}); err != nil {
		t.Fatalf("AddCachedLine: %v", err)
	}
	ClearCachedLines()

	cacheMu.Lock()
	n := len(cachedLines)
	cacheMu.Unlock()
	if n != 0 {
		t.Fatalf("cache not cleared, %d lines remain", n)
	}
}

func TestStartVoiceCallRequiresRunningService(t *testing.T) {
	if err := StartVoiceCall("+15551234567", "wss://relay", "tok", "hi-IN", "voice", nil); err == nil {
		t.Fatal("expected an error when the service is not running")
	}
}
