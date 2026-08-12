package wa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	meowcaller "github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"
)

// The relay leg of a streaming call.
//
// One WebSocket carries both directions: binary frames are s16le PCM, text frames are JSON control.
// Audio never leaves Go — pushing 60 ms frames across a language boundary to be handed back would
// add latency and jitter to buy nothing, so the host only ever sees state and transcripts.
//
// Why a relay at all, rather than talking to the speech provider directly: the provider key is a
// single account-wide secret with no per-client scoping, and an APK cannot hold one. Once something
// server-side must sit in the path anyway, giving it the whole loop is also the faster arrangement —
// it keeps transcription, reasoning and synthesis on one side of the ocean instead of three.

const (
	// relayReadLimit bounds a single inbound message. Synthesised audio arrives in chunks well
	// under this; the limit is here so a broken relay cannot exhaust the phone's memory.
	relayReadLimit = 1 << 20

	// relayHandshakeTimeout bounds the dial plus hello exchange.
	relayHandshakeTimeout = 10 * time.Second

	// relayWriteTimeout bounds one frame write. Longer than this and the uplink is dead.
	relayWriteTimeout = 5 * time.Second
)

// VoiceStreamOptions configures a streaming call.
type VoiceStreamOptions struct {
	// RelayURL is the wss:// endpoint that runs the conversation.
	RelayURL string
	// Token authorises this one call. Scoped to a user, a peer and a spend ceiling.
	Token string
	// Language and Voice pick the speech models, e.g. "hi-IN" and a speaker id.
	Language string
	Voice    string
	// RingFor is how long to ring before giving up. Zero means the call default.
	RingFor time.Duration

	// CachedLines holds pre-rendered s16le PCM by scripted-line id. Playing one of these costs no
	// network round trip at all, which is most of why a scripted call feels fast.
	CachedLines map[string][]byte

	// OnState, OnTranscript and OnEnded report progress to the host. All may be nil, and all are
	// called from goroutines that are not the host's UI thread.
	OnState      func(state string)
	OnTranscript func(text string, final bool)
	OnEnded      func(reason string)
}

// relayHello opens the session. The relay validates the token before anything else.
type relayHello struct {
	Type     string   `json:"type"`
	Token    string   `json:"token"`
	Peer     string   `json:"peer"`
	Language string   `json:"language"`
	Voice    string   `json:"voice"`
	Cached   []string `json:"cached,omitempty"`
}

// relayEvent is every text frame in either direction.
type relayEvent struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Final  bool   `json:"final,omitempty"`
	State  string `json:"state,omitempty"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// voiceSession owns one call's audio plumbing and its relay connection.
type voiceSession struct {
	svc  *Service
	opts VoiceStreamOptions

	conn *websocket.Conn
	src  *streamSource
	sink *streamSink

	callID string
	cancel context.CancelFunc
	hangup func() error
	closed sync.Once
}

// dialRelay opens and handshakes the relay connection.
//
// This runs before the call is offered, not after it is answered. Ringing is five to fifteen
// seconds of otherwise wasted time, and spending it establishing the socket, the speech streams and
// the model connection is why the first word can land immediately on pickup.
func dialRelay(ctx context.Context, opts VoiceStreamOptions, peer string) (*websocket.Conn, error) {
	if opts.RelayURL == "" {
		return nil, errors.New("relay url is required for a streaming call")
	}
	if opts.Token == "" {
		return nil, errors.New("relay token is required for a streaming call")
	}

	dialCtx, cancel := context.WithTimeout(ctx, relayHandshakeTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, opts.RelayURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}
	conn.SetReadLimit(relayReadLimit)

	cached := make([]string, 0, len(opts.CachedLines))
	for id := range opts.CachedLines {
		cached = append(cached, id)
	}
	hello, err := json.Marshal(relayHello{
		Type:     "hello",
		Token:    opts.Token,
		Peer:     peer,
		Language: opts.Language,
		Voice:    opts.Voice,
		Cached:   cached,
	})
	if err != nil {
		conn.CloseNow()
		return nil, err
	}
	if err := conn.Write(dialCtx, websocket.MessageText, hello); err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("relay handshake: %w", err)
	}
	return conn, nil
}

// PlaceStreamingCall rings the contact and bridges the call to the relay for a live conversation.
//
// Unlike PlaceCall it does not queue behind a busy slot: a scheduled conversation that starts
// minutes late is worse than one that reports it could not start, and the caller can reschedule.
func (s *Service) PlaceStreamingCall(ctx context.Context, recipient string, opts VoiceStreamOptions) (CallInfo, error) {
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
	if !s.queue.acquire() {
		return CallInfo{}, errors.New("another call is already in progress")
	}

	// Warm the relay first so the conversation can start the instant the peer picks up.
	conn, err := dialRelay(ctx, opts, jid.String())
	if err != nil {
		s.queue.releaseAndDrain(s)
		return CallInfo{}, err
	}

	info, err := s.placeStreamNow(ctx, target, jid, opts, conn)
	if err != nil {
		conn.CloseNow()
		s.queue.releaseAndDrain(s)
		return CallInfo{}, err
	}
	return info, nil
}

// placeStreamNow offers the call with the relay already connected.
func (s *Service) placeStreamNow(
	ctx context.Context,
	target ResolvedTarget,
	jid types.JID,
	opts VoiceStreamOptions,
	conn *websocket.Conn,
) (CallInfo, error) {
	call, err := s.media.client.CallWithOptions(ctx, jid.String(), meowcaller.CallOptions{})
	if err != nil {
		return CallInfo{}, fmt.Errorf("offer call: %w", err)
	}
	s.media.track(s, call)

	sessCtx, cancel := context.WithCancel(context.Background())
	sess := &voiceSession{
		svc:    s,
		opts:   opts,
		conn:   conn,
		src:    newStreamSource(),
		sink:   newStreamSink(),
		callID: call.ID(),
		cancel: cancel,
		hangup: call.Hangup,
	}

	// Receive early: the sink only sees frames once media is up, and attaching before then costs
	// nothing while attaching late loses the peer's opening words.
	call.Receive(sess.sink)

	call.OnPeerAccept(func() {
		call.Play(sess.src)
		sess.setState("connected")
		sess.playCached("greeting")
	})
	s.media.onEnd(call.ID(), func() { sess.finish("call ended") })

	go sess.readLoop(sessCtx)
	go sess.writeLoop(sessCtx)

	ownJID, _ := types.ParseJID(s.CurrentUserJID())
	info := &CallInfo{
		CallID:    call.ID(),
		PeerJID:   jid.String(),
		PeerName:  target.Name,
		Direction: CallDirectionOut,
		State:     CallStateRinging,
		StartedAt: time.Now(),
		creator:   ownJID.ToNonAD(),
	}
	s.queue.hold(call.ID())
	s.calls.put(info)
	s.log.Infof("placed streaming call %s to %s", info.CallID, jid)
	s.dispatchWebhook("call.placed", callPayload(*info))

	ringFor := opts.RingFor
	if ringFor == 0 {
		ringFor = defaultRingSeconds * time.Second
	}
	if ringFor > 0 {
		s.startRingTimeout(info.CallID, ringFor)
	}
	sess.setState("ringing")
	return *info, nil
}

// readLoop consumes relay frames until the connection or the call ends.
func (v *voiceSession) readLoop(ctx context.Context) {
	defer v.finish("relay closed")
	for {
		typ, data, err := v.conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				v.svc.log.Warnf("call %s: relay read: %v", v.callID, err)
			}
			return
		}
		switch typ {
		case websocket.MessageBinary:
			v.src.Push(data)
		case websocket.MessageText:
			var ev relayEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				v.svc.log.Warnf("call %s: bad relay event: %v", v.callID, err)
				continue
			}
			v.handle(ev)
		}
	}
}

// handle applies one control event.
func (v *voiceSession) handle(ev relayEvent) {
	switch ev.Type {
	case "barge_in":
		// The peer started talking. Stop mid-word rather than talking over them.
		v.src.Flush()
	case "transcript":
		if v.opts.OnTranscript != nil {
			v.opts.OnTranscript(ev.Text, ev.Final)
		}
		v.svc.notifyObserver("call_transcript", map[string]any{
			"call_id": v.callID, "text": ev.Text, "final": ev.Final,
		})
	case "state":
		v.setState(ev.State)
	case "play_cached":
		v.playCached(ev.ID)
	case "end":
		reason := ev.Reason
		if reason == "" {
			reason = "relay ended the call"
		}
		v.finish(reason)
	}
}

// writeLoop pumps peer audio to the relay.
func (v *voiceSession) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-v.sink.Frames():
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, relayWriteTimeout)
			err := v.conn.Write(wctx, websocket.MessageBinary, frame)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					v.svc.log.Warnf("call %s: relay write: %v", v.callID, err)
				}
				v.finish("relay uplink failed")
				return
			}
		}
	}
}

// playCached queues a pre-rendered line, bypassing the network entirely.
func (v *voiceSession) playCached(id string) {
	pcm, ok := v.opts.CachedLines[id]
	if !ok {
		return
	}
	frames := make([][]float32, 0, len(pcm)/frameBytes+1)
	for off := 0; off+frameBytes <= len(pcm); off += frameBytes {
		frames = append(frames, pcmToFloat(pcm[off:off+frameBytes]))
	}
	v.src.PushFrames(frames)
}

// setState reports a lifecycle change to the host.
func (v *voiceSession) setState(state string) {
	if state == "" {
		return
	}
	if v.opts.OnState != nil {
		v.opts.OnState(state)
	}
	// Also on the normal event bus, so a host driving this through Request alone still sees it.
	v.svc.notifyObserver("call_state", map[string]any{"call_id": v.callID, "state": state})
}

// finish tears the session down exactly once, whichever side ended it first.
func (v *voiceSession) finish(reason string) {
	v.closed.Do(func() {
		v.cancel()
		_ = v.src.Close()
		_ = v.sink.Close()

		// A normal close lets the relay flush its usage record; a dead socket is not worth waiting on.
		_ = v.conn.Close(websocket.StatusNormalClosure, "call ended")

		if v.hangup != nil {
			_ = v.hangup()
		}
		v.setState("ended")
		if v.opts.OnEnded != nil {
			v.opts.OnEnded(reason)
		}
		v.svc.notifyObserver("call_ended", map[string]any{"call_id": v.callID, "reason": reason})
		v.svc.log.Infof("streaming call %s finished: %s", v.callID, reason)
	})
}
