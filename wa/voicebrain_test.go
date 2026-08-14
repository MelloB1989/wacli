package wa

import (
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestOfferKeepsOnlyTheNewestUtterance(t *testing.T) {
	// Observed live before this existed: "Hi", "Hi", then a question — the
	// reply to the first arrived after the second was spoken, and the question
	// went unanswered. A queue is what made the conversation run a turn behind;
	// a caller who keeps talking wants an answer to their latest thought.
	v := &voiceSession{svc: &Service{log: waLog.Noop}, turns: make(chan string, 1)}
	v.offer("Hi")
	v.offer("Hi again")
	v.offer("what are my pending tasks?")

	if got := <-v.turns; got != "what are my pending tasks?" {
		t.Errorf("runner should get the newest utterance, got %q", got)
	}
	select {
	case extra := <-v.turns:
		t.Errorf("nothing stale should remain, found %q", extra)
	default:
	}
}

func TestOfferNeverBlocks(t *testing.T) {
	// The transcription goroutine calls this; blocking it stalls the call.
	v := &voiceSession{svc: &Service{log: waLog.Noop}, turns: make(chan string, 1)}
	for i := 0; i < 50; i++ {
		v.offer("utterance")
	}
}
