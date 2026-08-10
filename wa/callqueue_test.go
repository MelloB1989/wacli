package wa

import (
	"strings"
	"testing"
)

// Call references are what every command takes, so the resolver has to accept the handle it printed
// and refuse anything it cannot pin down to exactly one call.
func TestCallRegistryResolve(t *testing.T) {
	r := newCallRegistry()
	a := &CallInfo{CallID: "3EB0AAAA1111", State: CallStateRinging}
	b := &CallInfo{CallID: "3EB0BBBB2222", State: CallStateEnded}
	r.put(a)
	r.put(b)

	if a.ShortID != "c1" || b.ShortID != "c2" {
		t.Fatalf("handles = %q/%q, want c1/c2", a.ShortID, b.ShortID)
	}

	cases := []struct {
		name, ref, want string
	}{
		{"short handle", "c1", a.CallID},
		{"short handle, other", "c2", b.CallID},
		{"handle is case-insensitive", "C1", a.CallID},
		{"full call id", b.CallID, b.CallID},
		{"unique prefix", "3EB0AA", a.CallID},
		{"prefix is case-insensitive", "3eb0bb", b.CallID},
		{"empty picks the only active call", "", a.CallID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := r.resolve(c.ref)
			if err != nil {
				t.Fatalf("resolve(%q): %v", c.ref, err)
			}
			if got != c.want {
				t.Errorf("resolve(%q) = %q, want %q", c.ref, got, c.want)
			}
		})
	}

	// An ambiguous or unknown reference must fail rather than pick one: resolving the wrong call
	// would hang up on, or answer, somebody else.
	if _, err := r.resolve("3EB0"); err == nil {
		t.Error("accepted an ambiguous prefix")
	} else if !strings.Contains(err.Error(), "matches 2 calls") {
		t.Errorf("unhelpful ambiguity error: %v", err)
	}
	if _, err := r.resolve("nope"); err == nil {
		t.Error("accepted an unknown reference")
	}
}

// Re-registering a call must not renumber it — the handle may already be in someone's hands.
func TestCallRegistryKeepsHandleOnRePut(t *testing.T) {
	r := newCallRegistry()
	first := &CallInfo{CallID: "3EB0AAAA1111", State: CallStateRinging}
	r.put(first)
	again := &CallInfo{CallID: "3EB0AAAA1111", State: CallStateAccepted}
	r.put(again)

	if again.ShortID != first.ShortID {
		t.Errorf("handle changed on re-put: %q -> %q", first.ShortID, again.ShortID)
	}
	if r.seq != 1 {
		t.Errorf("counter advanced to %d on re-put", r.seq)
	}
}

// Only one call may be dialling at a time; the rest wait their turn.
func TestCallQueueHoldsOneSlot(t *testing.T) {
	q := &callQueue{}
	if !q.acquire() {
		t.Fatal("could not take a free slot")
	}
	if q.acquire() {
		t.Error("handed out the slot twice — two calls would dial at once")
	}
	if !q.busy() {
		t.Error("slot is held but busy() is false")
	}

	q.hold("3EB0AAAA1111")
	if got := q.activeCall(); got != "3EB0AAAA1111" {
		t.Errorf("activeCall = %q, want the held call", got)
	}
}

// A stale end event from a call that no longer holds the slot must not free it, or it would release
// the slot out from under whichever call took it next.
func TestCallQueueIgnoresStaleFinish(t *testing.T) {
	q := &callQueue{}
	q.acquire()
	q.hold("current")

	q.finished(&Service{}, "an-older-call")
	if !q.busy() {
		t.Error("a stale end event released the slot")
	}
	if got := q.activeCall(); got != "current" {
		t.Errorf("activeCall = %q, want it untouched", got)
	}
}

// The dangerous window: a call has taken the slot but has not yet been assigned its call ID, because
// the ID only exists once the offer is on the wire. A delayed end event from the previous call must
// not free the slot here, or the queue would dial a second call alongside the one already going out.
func TestCallQueueIgnoresStaleFinishBeforeHold(t *testing.T) {
	q := &callQueue{}
	if !q.acquire() {
		t.Fatal("could not take a free slot")
	}
	// acquire() has run, hold() has not — active is still empty.
	if got := q.activeCall(); got != "" {
		t.Fatalf("activeCall = %q, want empty before hold", got)
	}

	q.finished(&Service{}, "a-previous-call")

	if !q.busy() {
		t.Fatal("a stale end event freed a slot that was taken but not yet named — " +
			"a queued call would now dial alongside the one being placed")
	}
	if q.acquire() {
		t.Error("slot was handed out a second time")
	}
}

// Cancelling a queued call drops it before it is ever dialled.
func TestCallQueueCancel(t *testing.T) {
	q := &callQueue{}
	q.waiting = []*queuedCall{{shortID: "c2"}, {shortID: "c3"}}

	if !q.cancel("c2") {
		t.Fatal("cancel reported no such queued call")
	}
	if got := q.pending(); len(got) != 1 || got[0] != "c3" {
		t.Errorf("pending = %v, want [c3]", got)
	}
	if q.cancel("c9") {
		t.Error("cancelled a call that was never queued")
	}
}

// A queued call is registered under a placeholder key so it can be inspected and cancelled before it
// has a WhatsApp call ID at all.
func TestQueuedCallIsResolvableBeforeDialling(t *testing.T) {
	r := newCallRegistry()
	info := &CallInfo{State: CallStateQueued, Direction: CallDirectionOut}
	key := r.putQueued(info)

	if key != queuedRegistryKey(info.ShortID) {
		t.Errorf("registry key = %q, want %q", key, queuedRegistryKey(info.ShortID))
	}
	got, err := r.resolve(info.ShortID)
	if err != nil {
		t.Fatalf("resolve(%q): %v", info.ShortID, err)
	}
	if got != key {
		t.Errorf("resolve(%q) = %q, want %q", info.ShortID, got, key)
	}
	if !info.active() {
		t.Error("a queued call should count as active, or it cannot be listed or cancelled")
	}
}
