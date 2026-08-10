package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Call state machine. Calls only ever move forward through these states.
const (
	// CallStateQueued is a call that has been asked for but not yet dialled, because another call
	// holds the single call slot. See callqueue.go.
	CallStateQueued    = "queued"
	CallStateRinging   = "ringing"
	CallStateAccepted  = "accepted"
	CallStateRejected  = "rejected"
	CallStateEnded     = "ended"
	CallStateFailed    = "failed"
	CallDirectionOut   = "outgoing"
	CallDirectionIn    = "incoming"
	defaultRingSeconds = 45

	// Terminate reasons, as WhatsApp spells them on the wire. Defined here rather than taken from
	// whatsmeow so wacli builds against the upstream module.
	callReasonHangup  = "hangup"
	callReasonTimeout = "timeout"
)

// CallInfo is a single call, incoming or outgoing.
type CallInfo struct {
	// ShortID is a compact handle for this call — "c1", "c2", and so on — assigned when the call
	// enters the registry. WhatsApp's own call IDs are 22-character hex strings, which are awkward to
	// read back and easy to mistype or truncate; every command that takes a call accepts this
	// instead. It is unique for the lifetime of the daemon, which is also the lifetime of the
	// registry: calls are not persisted, so a restart starts again from c1.
	ShortID   string    `json:"short_id"`
	CallID    string    `json:"call_id"`
	PeerJID   string    `json:"peer_jid"`
	PeerName  string    `json:"peer_name,omitempty"`
	Direction string    `json:"direction"`
	State     string    `json:"state"`
	Video     bool      `json:"video"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Reason    string    `json:"reason,omitempty"`

	// RecordPath is the absolute path the other party's voice is being written to, set when the call
	// was placed or answered with --record. Empty means recording was never asked for.
	RecordPath string `json:"record_path,omitempty"`
	// RecordedSeconds is how much inbound audio was actually captured, filled in when the call ends.
	// Zero on a finished call that had a RecordPath means no inbound audio ever arrived.
	RecordedSeconds float64 `json:"recorded_seconds"`

	// CallKey is the hex-encoded e2e media master key. Decrypting it out of the offer is not
	// something upstream whatsmeow does, and the media stack keeps its own key material, so nothing
	// populates this today; it is kept so the field does not vanish from the API.
	CallKey string `json:"call_key,omitempty"`

	// creator is the JID that started the call, which is who terminate/reject stanzas must be
	// addressed to. It's our own JID for outgoing calls.
	creator types.JID
	// offer is the original event for incoming calls, kept for diagnostics.
	offer *events.CallOffer
}

func (c CallInfo) active() bool {
	return c.State == CallStateQueued || c.State == CallStateRinging || c.State == CallStateAccepted
}

// callRegistry tracks in-flight and recently finished calls. Calls are not persisted to the store:
// they're ephemeral, and a restart of the daemon means any call in progress is gone anyway.
type callRegistry struct {
	mu     sync.RWMutex
	calls  map[string]*CallInfo
	cancel map[string]context.CancelFunc
	seq    int
}

func newCallRegistry() *callRegistry {
	return &callRegistry{
		calls:  make(map[string]*CallInfo),
		cancel: make(map[string]context.CancelFunc),
	}
}

func (r *callRegistry) put(info *CallInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Keep the handle a call already has, so re-registering it (an offer arriving twice, a state
	// refresh) doesn't renumber something the user is mid-way through referring to.
	if existing, ok := r.calls[info.CallID]; ok && existing.ShortID != "" {
		info.ShortID = existing.ShortID
	} else if info.ShortID == "" {
		r.seq++
		info.ShortID = fmt.Sprintf("c%d", r.seq)
	}
	r.calls[info.CallID] = info
}

// putQueued registers a call that has no WhatsApp call ID yet, keyed by its handle, and returns
// that placeholder key.
func (r *callRegistry) putQueued(info *CallInfo) string {
	r.mu.Lock()
	r.seq++
	info.ShortID = fmt.Sprintf("c%d", r.seq)
	key := queuedRegistryKey(info.ShortID)
	info.CallID = key
	r.calls[key] = info
	r.mu.Unlock()
	return key
}

// setRecording notes where a call's inbound audio is being written.
func (r *callRegistry) setRecording(callID, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.calls[callID]; ok {
		info.RecordPath = path
	}
}

// setRecorded notes how much inbound audio a finished call captured.
func (r *callRegistry) setRecorded(callID string, seconds float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.calls[callID]; ok {
		info.RecordedSeconds = seconds
	}
}

// remove drops a call from the registry, used when a queued placeholder is replaced by the real one.
func (r *callRegistry) remove(callID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.calls, callID)
	delete(r.cancel, callID)
}

// resolve turns a user-supplied reference into a full call ID. It accepts a short handle ("c2"), a
// full call ID, or any unambiguous prefix of one; an empty ref means the only active call.
//
// Ambiguity is reported rather than guessed — picking one of several matching calls silently would
// end or answer the wrong one.
func (r *callRegistry) resolve(ref string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ref = strings.TrimSpace(ref)
	if ref == "" {
		var active []string
		for id, info := range r.calls {
			if info.active() {
				active = append(active, id)
			}
		}
		switch len(active) {
		case 0:
			return "", errors.New("no active call — pass a call reference")
		case 1:
			return active[0], nil
		default:
			return "", fmt.Errorf("%d calls are active — pass a call reference", len(active))
		}
	}

	for id, info := range r.calls {
		if strings.EqualFold(info.ShortID, ref) || strings.EqualFold(id, ref) {
			return id, nil
		}
	}

	var matches []string
	for id := range r.calls {
		if strings.HasPrefix(strings.ToLower(id), strings.ToLower(ref)) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no call matching %q", ref)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("%q matches %d calls: %s", ref, len(matches), strings.Join(matches, ", "))
	}
}

func (r *callRegistry) get(callID string) (*CallInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.calls[callID]
	if !ok {
		return nil, false
	}
	copied := *info
	return &copied, true
}

// finish moves a call to a terminal state. It's a no-op if the call already finished, so a
// terminate we send and the terminate the server echoes back don't fight each other.
func (r *callRegistry) finish(callID, state, reason string) (*CallInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.calls[callID]
	if !ok || !info.active() {
		return nil, false
	}
	info.State = state
	info.Reason = reason
	info.EndedAt = time.Now()
	if cancel, ok := r.cancel[callID]; ok {
		cancel()
		delete(r.cancel, callID)
	}
	copied := *info
	return &copied, true
}

func (r *callRegistry) setState(callID, state string) (*CallInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.calls[callID]
	if !ok {
		return nil, false
	}
	info.State = state
	copied := *info
	return &copied, true
}

func (r *callRegistry) armRingTimeout(callID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel[callID] = cancel
}

// list returns calls newest first, optionally only the active ones.
func (r *callRegistry) list(activeOnly bool) []CallInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CallInfo, 0, len(r.calls))
	for _, info := range r.calls {
		if activeOnly && !info.active() {
			continue
		}
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// prune drops finished calls older than the cutoff so the registry doesn't grow forever.
func (r *callRegistry) prune(olderThan time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-olderThan)
	for id, info := range r.calls {
		if !info.active() && !info.EndedAt.IsZero() && info.EndedAt.Before(cutoff) {
			delete(r.calls, id)
		}
	}
}

// PlaceCallOptions are the knobs for PlaceCall.
type PlaceCallOptions struct {
	// Video places a video call instead of a voice call.
	Video bool
	// RingFor is how long to keep ringing before hanging up automatically. Zero means 45s.
	// A negative value disables the timeout, leaving the call up until EndCall is called.
	RingFor time.Duration

	// Audio is played into the call once the peer answers and media comes up.
	Audio AudioRequest
}

// PlaceCall rings the given contact, and carries audio if the options ask for it.
//
// The offer goes out through meowcaller rather than whatsmeow's OfferCall: a call we place gets its
// relay allocation in <ack class="call" type="offer">, which whatsmeow drops unhandled, so an offer
// it builds can ring but never carry media. See callmedia.go.
func (s *Service) PlaceCall(ctx context.Context, recipient string, opts PlaceCallOptions) (CallInfo, error) {
	target, err := s.ResolveBestTarget(recipient, "chat", true)
	if err != nil {
		return CallInfo{}, err
	}
	jid, err := types.ParseJID(target.JID)
	if err != nil {
		return CallInfo{}, err
	}
	if jid.Server == types.GroupServer {
		return CallInfo{}, errors.New("group calls are not supported")
	}
	if err := s.ensureAutomationAllowed(jid); err != nil {
		return CallInfo{}, err
	}
	if !s.client.IsConnected() {
		return CallInfo{}, errors.New("WhatsApp client is not connected")
	}
	if s.media == nil {
		return CallInfo{}, errors.New("media stack is not initialised")
	}

	// One call at a time. Overlapping calls from an automated client look exactly like the abuse
	// WhatsApp bans for, so a second request waits its turn rather than dialling in parallel.
	if !s.queue.acquire() {
		return s.queue.enqueue(s, target, jid, opts), nil
	}
	info, err := s.placeNow(ctx, target, jid, opts, "")
	if err != nil {
		s.queue.releaseAndDrain(s)
	}
	return info, err
}

// placeNow dials immediately, assuming the call slot is already held. shortID carries the handle a
// queued call was given at enqueue time so it does not change when the call finally goes out.
func (s *Service) placeNow(
	ctx context.Context,
	target ResolvedTarget,
	jid types.JID,
	opts PlaceCallOptions,
	shortID string,
) (CallInfo, error) {
	call, err := s.media.client.CallWithOptions(ctx, jid.String(),
		meowcaller.CallOptions{Video: opts.Video})
	if err != nil {
		return CallInfo{}, fmt.Errorf("offer call: %w", err)
	}
	s.media.track(s, call)
	start, err := s.media.attachAudio(s, call, opts.Audio, func(msg string) {
		s.log.Infof("call %s: %s", call.ID(), msg)
	})
	if err != nil {
		_ = call.Hangup()
		return CallInfo{}, err
	}
	// Start speaking when they pick up. OnPeerAccept is the only listener here with catch-up
	// semantics — if the accept already landed it fires immediately — so registering it after the
	// offer cannot lose the event.
	call.OnPeerAccept(start)

	ownJID, _ := types.ParseJID(s.CurrentUserJID())
	info := &CallInfo{
		ShortID:    shortID,
		RecordPath: opts.Audio.Record,
		CallID:     call.ID(),
		PeerJID:    jid.String(),
		PeerName:   target.Name,
		Direction:  CallDirectionOut,
		State:      CallStateRinging,
		Video:      opts.Video,
		StartedAt:  time.Now(),
		creator:    ownJID.ToNonAD(),
	}
	s.queue.hold(call.ID())
	s.calls.put(info)
	s.log.Infof("placed %s call %s to %s", callKind(opts.Video), info.CallID, jid)
	s.dispatchWebhook("call.placed", callPayload(*info))

	ringFor := opts.RingFor
	if ringFor == 0 {
		ringFor = defaultRingSeconds * time.Second
	}
	if ringFor > 0 {
		s.startRingTimeout(info.CallID, ringFor)
	}
	return *info, nil
}

// CallStatus reports one call by short handle, full ID, or unique prefix.
//
// It reads through to the media layer as well as the registry: the registry knows the signalling
// state, but only meowcaller knows whether the call is still carrying media, and the two can
// disagree — a call the peer has torn down stays "accepted" in the registry until the terminate
// stanza lands.
func (s *Service) CallStatus(ref string) (CallStatusInfo, error) {
	id, err := s.calls.resolve(ref)
	if err != nil {
		return CallStatusInfo{}, err
	}
	info, ok := s.calls.get(id)
	if !ok {
		return CallStatusInfo{}, fmt.Errorf("no call %s", id)
	}

	status := CallStatusInfo{CallInfo: *info}
	if info.EndedAt.IsZero() {
		status.DurationSeconds = int(time.Since(info.StartedAt).Seconds())
	} else {
		status.DurationSeconds = int(info.EndedAt.Sub(info.StartedAt).Seconds())
	}
	if s.media != nil {
		if _, err := s.media.get(id); err == nil {
			status.MediaLive = true
		}
	}
	return status, nil
}

// CallStatusInfo is a call plus the live media state the registry alone cannot report.
type CallStatusInfo struct {
	CallInfo
	// MediaLive reports whether the media stack still holds this call open.
	MediaLive bool `json:"media_live"`
	// DurationSeconds counts from the call starting, to now or to when it ended.
	DurationSeconds int `json:"duration_seconds"`
}

// startRingTimeout hangs the call up automatically once it has rung for long enough. Without this
// an unanswered call would ring until the peer's client gives up, and the daemon would keep
// thinking it's active.
func (s *Service) startRingTimeout(callID string, ringFor time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	s.calls.armRingTimeout(callID, cancel)
	go func() {
		defer cancel()
		select {
		case <-ctx.Done():
			// Call already ended; nothing to do.
			return
		case <-time.After(ringFor):
		}
		if _, err := s.EndCall(context.Background(), callID, callReasonTimeout); err != nil &&
			!errors.Is(err, errCallNotActive) {
			s.log.Warnf("ring timeout for call %s: %v", callID, err)
		}
	}()
}

var errCallNotActive = errors.New("no active call with that ID")

// EndCall hangs up an ongoing or ringing call. reason may be empty.
func (s *Service) EndCall(ctx context.Context, ref, reason string) (CallInfo, error) {
	callID, err := s.calls.resolve(ref)
	if err != nil {
		return CallInfo{}, err
	}
	info, ok := s.calls.get(callID)
	if !ok || !info.active() {
		return CallInfo{}, errCallNotActive
	}
	// A queued call was never dialled, so there is nothing to terminate — just drop it from the
	// queue before it gets its turn.
	if info.State == CallStateQueued {
		s.queue.cancel(info.ShortID)
		cancelled, _ := s.calls.finish(callID, CallStateEnded, "cancelled before dialling")
		if cancelled == nil {
			return *info, nil
		}
		s.log.Infof("call %s cancelled from the queue", info.ShortID)
		s.dispatchWebhook("call.ended", callPayload(*cancelled))
		return *cancelled, nil
	}
	if !s.client.IsConnected() {
		return CallInfo{}, errors.New("WhatsApp client is not connected")
	}
	if reason == "" {
		reason = callReasonHangup
	}
	// Hang up through the media stack, which owns the call and sends the terminate itself. It also
	// tears the media down, which a bare signalling terminate would leave running.
	call, err := s.media.get(callID)
	if err != nil {
		return CallInfo{}, fmt.Errorf("end call: %w", err)
	}
	if err := call.Hangup(); err != nil {
		return CallInfo{}, fmt.Errorf("terminate call: %w", err)
	}
	ended, _ := s.calls.finish(callID, CallStateEnded, reason)
	if ended == nil {
		return *info, nil
	}
	s.log.Infof("ended call %s (%s)", callID, reason)
	s.dispatchWebhook("call.ended", callPayload(*ended))
	return *ended, nil
}

// RejectIncomingCall declines a call that is currently ringing.
func (s *Service) RejectIncomingCall(ctx context.Context, ref string) (CallInfo, error) {
	callID, err := s.calls.resolve(ref)
	if err != nil {
		return CallInfo{}, err
	}
	info, ok := s.calls.get(callID)
	if !ok || !info.active() {
		return CallInfo{}, errCallNotActive
	}
	if info.Direction != CallDirectionIn {
		return CallInfo{}, errors.New("can only reject incoming calls; use end to hang up an outgoing call")
	}
	if !s.client.IsConnected() {
		return CallInfo{}, errors.New("WhatsApp client is not connected")
	}
	if err := s.client.RejectCall(ctx, info.creator, callID); err != nil {
		return CallInfo{}, fmt.Errorf("reject call: %w", err)
	}
	rejected, _ := s.calls.finish(callID, CallStateRejected, "rejected-locally")
	if rejected == nil {
		return *info, nil
	}
	s.log.Infof("rejected incoming call %s from %s", callID, info.PeerJID)
	s.dispatchWebhook("call.rejected", callPayload(*rejected))
	return *rejected, nil
}

// offerIsVideo reports whether a call offer advertises video, by looking for a <video> descriptor
// in the offer node. Read from the node rather than a parsed field so wacli builds against upstream
// whatsmeow.
func offerIsVideo(node *waBinary.Node) bool {
	if node == nil {
		return false
	}
	for _, child := range node.GetChildren() {
		if child.Tag == "video" {
			return true
		}
		if offerIsVideo(&child) {
			return true
		}
	}
	return false
}

// LatestRingingCall returns the most recent incoming call still ringing, if any.
func (s *Service) LatestRingingCall() (CallInfo, bool) {
	for _, c := range s.calls.list(true) {
		if c.Direction == CallDirectionIn && c.State == CallStateRinging {
			return c, true
		}
	}
	return CallInfo{}, false
}

// ListCalls returns known calls, newest first.
func (s *Service) ListCalls(activeOnly bool) []CallInfo {
	s.calls.prune(time.Hour)
	return s.calls.list(activeOnly)
}

// --- incoming event handling ---

func (s *Service) onCallOffer(evt *events.CallOffer) {
	peer := evt.CallCreator
	if peer.IsEmpty() {
		peer = evt.From
	}
	info := &CallInfo{
		CallID:    evt.CallID,
		PeerJID:   peer.String(),
		PeerName:  s.displayNameFor(peer),
		Direction: CallDirectionIn,
		State:     CallStateRinging,
		Video:     offerIsVideo(evt.Data),
		StartedAt: evt.Timestamp,
		creator:   evt.CallCreator.ToNonAD(),
		offer:     evt,
	}
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now()
	}
	s.calls.put(info)
	s.log.Infof("incoming %s call %s from %s", callKind(info.Video), info.CallID, info.PeerJID)
	s.dispatchWebhook("call.incoming", callPayload(*info))
}

func (s *Service) onCallAccept(evt *events.CallAccept) {
	info, ok := s.calls.setState(evt.CallID, CallStateAccepted)
	if !ok {
		return
	}
	// There is no media layer, so an "accepted" call is going to die shortly. Say so once, loudly,
	// instead of leaving a confusing half-connected call in the list.
	s.log.Warnf("call %s was answered, but wacli has no media support — the call will drop", evt.CallID)
	s.dispatchWebhook("call.accepted", callPayload(*info))
}

func (s *Service) onCallTerminate(evt *events.CallTerminate) {
	info, ok := s.calls.finish(evt.CallID, CallStateEnded, evt.Reason)
	if !ok {
		return
	}
	s.log.Infof("call %s terminated by peer (%s)", evt.CallID, evt.Reason)
	s.dispatchWebhook("call.ended", callPayload(*info))
}

func (s *Service) onCallReject(evt *events.CallReject) {
	info, ok := s.calls.finish(evt.CallID, CallStateRejected, "rejected-by-peer")
	if !ok {
		return
	}
	s.log.Infof("call %s rejected by %s", evt.CallID, info.PeerJID)
	s.dispatchWebhook("call.rejected", callPayload(*info))
}

func (s *Service) displayNameFor(jid types.JID) string {
	target, err := s.ResolveBestTarget(jid.ToNonAD().String(), "chat", true)
	if err != nil {
		return ""
	}
	return target.Name
}

func callKind(video bool) string {
	if video {
		return "video"
	}
	return "voice"
}

func callPayload(info CallInfo) map[string]any {
	return map[string]any{
		"call_id":   info.CallID,
		"peer_jid":  info.PeerJID,
		"peer_name": info.PeerName,
		"direction": info.Direction,
		"state":     info.State,
		"video":     info.Video,
		"reason":    info.Reason,
	}
}
