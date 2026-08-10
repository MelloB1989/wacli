package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// One call at a time, with the rest queued.
//
// Two calls in flight at once from an automated client is a good way to get the account banned: it
// is exactly the pattern bulk-dialling abuse produces, and it is not something a human WhatsApp
// client can even do. So the daemon holds a single call slot. A request that finds it taken is
// queued and dialled when the slot frees, rather than rejected — the caller gets a handle back
// immediately and can follow it with `wacli call status`.
//
// Only *outgoing* calls queue. Answering is refused outright when the slot is busy: a queued answer
// would fire long after the caller gave up.

// callQueue serialises outgoing calls behind a single active-call slot.
type callQueue struct {
	mu      sync.Mutex
	active  string // full call ID holding the slot; "" when free but reserved is set
	held    bool   // slot is taken (possibly before the call ID is known)
	waiting []*queuedCall
}

// queuedCall is a call waiting for the slot.
type queuedCall struct {
	shortID    string
	target     ResolvedTarget
	jid        types.JID
	opts       PlaceCallOptions
	enqueued   time.Time
	registryID string // synthetic registry key while it has no real call ID
}

// acquire takes the slot if it is free.
func (q *callQueue) acquire() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.held {
		return false
	}
	q.held = true
	return true
}

// hold records the call ID now occupying the slot, once dialling has produced one.
func (q *callQueue) hold(callID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active = callID
	q.held = true
}

// activeCall reports the call currently holding the slot.
func (q *callQueue) activeCall() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.active
}

// busy reports whether a call holds the slot.
func (q *callQueue) busy() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.held
}

// finished releases the slot if callID is the one holding it, then dials the next in line.
//
// The match must be exact. Treating an empty active as "nobody owns it, so release" opens a real
// race: acquire() and hold() cannot be atomic, because the call ID only exists once the offer has
// gone out, so the slot spends the whole dial sitting held with no ID attached. A delayed end event
// from the *previous* call landing in that window would free a slot the new call had already taken,
// and a queued call would then dial alongside it — exactly the concurrency this queue exists to
// prevent.
//
// Releasing a slot that has no call ID yet is only ever correct for the goroutine that acquired it
// and then failed, which uses releaseAndDrain directly.
func (q *callQueue) finished(s *Service, callID string) {
	q.mu.Lock()
	if q.active == "" || q.active != callID {
		q.mu.Unlock()
		return
	}
	q.mu.Unlock()
	q.releaseAndDrain(s)
}

// releaseAndDrain frees the slot and starts the next queued call, if any.
func (q *callQueue) releaseAndDrain(s *Service) {
	q.mu.Lock()
	q.active = ""
	q.held = false
	if len(q.waiting) == 0 {
		q.mu.Unlock()
		return
	}
	next := q.waiting[0]
	q.waiting = q.waiting[1:]
	q.held = true // reserve for `next` before releasing the lock
	q.mu.Unlock()

	// Drop the placeholder registry entry; placeNow re-registers under the real call ID, keeping the
	// handle so anything already referring to it still resolves.
	s.calls.remove(next.registryID)
	s.log.Infof("call %s: dialling from the queue (waited %s)",
		next.shortID, time.Since(next.enqueued).Round(time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := s.placeNow(ctx, next.target, next.jid, next.opts, next.shortID); err != nil {
		s.log.Errorf("call %s: queued call failed: %v", next.shortID, err)
		// Its slot reservation would otherwise strand everything behind it.
		q.releaseAndDrain(s)
	}
}

// enqueue parks a call behind the active one and registers it so it can be inspected and cancelled.
func (q *callQueue) enqueue(s *Service, target ResolvedTarget, jid types.JID, opts PlaceCallOptions) CallInfo {
	info := &CallInfo{
		// A queued call has no WhatsApp call ID yet, so the registry keys it by its own handle until
		// it is dialled. put() assigns the handle, and the key is patched to match right after.
		CallID:    "",
		PeerJID:   jid.String(),
		PeerName:  target.Name,
		Direction: CallDirectionOut,
		State:     CallStateQueued,
		Video:     opts.Video,
		StartedAt: time.Now(),
	}
	registryID := s.calls.putQueued(info)

	q.mu.Lock()
	q.waiting = append(q.waiting, &queuedCall{
		shortID:    info.ShortID,
		target:     target,
		jid:        jid,
		opts:       opts,
		enqueued:   time.Now(),
		registryID: registryID,
	})
	position := len(q.waiting)
	q.mu.Unlock()

	s.log.Infof("call %s queued at position %d (a call is already active)", info.ShortID, position)
	return *info
}

// pending returns the queued calls in dial order.
func (q *callQueue) pending() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, 0, len(q.waiting))
	for _, w := range q.waiting {
		out = append(out, w.shortID)
	}
	return out
}

// cancel drops a queued call before it is dialled.
func (q *callQueue) cancel(shortID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, w := range q.waiting {
		if w.shortID == shortID {
			q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
			return true
		}
	}
	return false
}

// QueueStatus describes the call slot and everything waiting on it.
type QueueStatus struct {
	ActiveCallID string   `json:"active_call_id,omitempty"`
	Busy         bool     `json:"busy"`
	Waiting      []string `json:"waiting"`
}

// CallQueueStatus reports the current slot and queue.
func (s *Service) CallQueueStatus() QueueStatus {
	return QueueStatus{
		ActiveCallID: s.queue.activeCall(),
		Busy:         s.queue.busy(),
		Waiting:      s.queue.pending(),
	}
}

// queuedRegistryKey is the placeholder registry key for a call that has no WhatsApp ID yet.
func queuedRegistryKey(shortID string) string { return fmt.Sprintf("queued:%s", shortID) }
