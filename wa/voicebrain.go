package wa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MelloB1989/wacli/wa/sarvam"
	"github.com/coder/websocket"
	meowcaller "github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"
)

// A live conversation on a call, with the voice pipeline HERE and only text
// crossing to the brain.
//
// The first cut of this streamed raw audio to a relay that ran transcription,
// reasoning and synthesis remotely. Once the brain moved onto the same box the
// split stopped paying for itself: audio crossed a socket in both directions to
// buy nothing, and every speech-pipeline change meant coordinating two
// repositories. wacli owns the call, so wacli owns everything that touches
// audio — media, transcription, synthesis, turn-taking, barge-in, buffering —
// and the brain owns exactly one thing: given what was said, what to say back.
//
// The wire between them is three message types each way, all JSON text:
//
//	wacli → brain:  {"type":"start", "call_id","peer","peer_name","language","direction"}
//	                {"type":"utterance", "text"}
//	                {"type":"ended", "reason"}
//	brain → wacli:  {"type":"say", "text"}     — speak this to the caller
//	                {"type":"hangup"}          — end the call
//
// The greeting is simply the first say after start. Anything a future brain
// needs beyond this earns its way in as a new message type, not a new socket.

const (
	brainHandshakeTimeout = 10 * time.Second
	brainWriteTimeout     = 5 * time.Second
	brainReadLimit        = 1 << 20

	// settleFor is how long after a final transcript to wait for more before
	// handing the utterance to the brain. The transcriber ends a segment on
	// every breath, so one sentence arrives as several finals; answering each
	// fragment talks over somebody mid-thought. Every millisecond here is
	// silence the caller hears, so it buys only enough to join a breath.
	settleFor = 200 * time.Millisecond
)

// fillers are spoken the moment an utterance goes to the brain, so the caller
// hears acknowledgement in the second the model needs instead of a dead line.
// Varied so a conversation does not sound like a loop.
var voiceFillers = []string{"Mm-hm.", "Right.", "Okay.", "Sure.", "Got it."}

// VoiceBrainOptions configures a spoken conversation.
type VoiceBrainOptions struct {
	// BrainURL is the ws:// endpoint that decides what to say.
	BrainURL string
	// Language and Voice pick the speech models, e.g. "en-IN" and a speaker id.
	Language string
	Voice    string
	// RingFor is how long to ring before giving up. Zero means the call default.
	RingFor time.Duration
	// NoFiller silences the acknowledgement words for brains that want full
	// control of every syllable.
	NoFiller bool
}

// brainMsg is every message in either direction.
type brainMsg struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id,omitempty"`
	Peer      string `json:"peer,omitempty"`
	PeerName  string `json:"peer_name,omitempty"`
	Language  string `json:"language,omitempty"`
	Direction string `json:"direction,omitempty"`
	Text      string `json:"text,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// voiceSession owns one call's pipeline: media frames, speech both ways, turn
// taking, and the brain connection.
type voiceSession struct {
	svc  *Service
	opts VoiceBrainOptions

	conn *websocket.Conn // brain
	stt  *sarvam.STT
	tts  *sarvam.TTS
	src  *streamSource
	sink *streamSink

	callID   string
	peer     string
	peerName string

	// turns holds at most ONE utterance waiting for the brain, so a caller who
	// keeps talking gets an answer to their latest thought, not a queue of
	// stale ones.
	turns  chan string
	filled int
	// micFrames and sttEvents count only far enough to log the first of each.
	micFrames int
	sttEvents int
	dump      *os.File

	cancel context.CancelFunc
	hangup func() error
	closed sync.Once
	mu     sync.Mutex // guards conn writes
}

// startVoiceSession dials the brain and the speech providers, wires the
// pipeline, and returns the session ready to be attached to a call.
//
// This runs BEFORE the call is offered or answered. Ringing is seconds of
// otherwise wasted time, and spending it opening three sockets is why the first
// word lands the moment the ring stops.
func (s *Service) startVoiceSession(ctx context.Context, opts VoiceBrainOptions, callID, peer, peerName string) (*voiceSession, error) {
	if strings.TrimSpace(opts.BrainURL) == "" {
		return nil, errors.New("brain_url is required for a spoken call")
	}
	key := sarvamAPIKey()
	if key == "" {
		return nil, errors.New("SARVAM_API_KEY is not set; a spoken call needs the speech provider")
	}
	language := strings.TrimSpace(opts.Language)
	if language == "" {
		language = "en-IN"
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, brainHandshakeTimeout)
	defer cancelDial()
	conn, _, err := websocket.Dial(dialCtx, opts.BrainURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial brain: %w", err)
	}
	conn.SetReadLimit(brainReadLimit)

	cfg := sarvam.Config{APIKey: key}
	sessCtx, cancel := context.WithCancel(context.Background())
	stt, err := sarvam.DialSTT(sessCtx, cfg, language)
	if err != nil {
		conn.CloseNow()
		cancel()
		return nil, fmt.Errorf("transcription: %w", err)
	}
	tts, err := sarvam.DialTTS(sessCtx, cfg, language, strings.TrimSpace(opts.Voice), 0)
	if err != nil {
		stt.Close()
		conn.CloseNow()
		cancel()
		return nil, fmt.Errorf("synthesis: %w", err)
	}

	v := &voiceSession{
		svc: s, opts: opts, conn: conn, stt: stt, tts: tts,
		src: newStreamSource(), sink: newStreamSink(),
		callID: callID, peer: peer, peerName: peerName,
		turns: make(chan string, 1), cancel: cancel,
	}
	if path := strings.TrimSpace(os.Getenv("WACLI_VOICE_DUMP")); path != "" {
		if f, err := os.Create(path); err == nil {
			v.dump = f
			s.log.Infof("dumping transcription audio to %s (raw s16le 16kHz mono)", path)
		}
	}
	go v.pumpMic(sessCtx)
	go v.pumpSpeech(sessCtx)
	go v.pumpVoice(sessCtx)
	go v.pumpBrain(sessCtx)
	go v.runTurns(sessCtx)
	return v, nil
}

// begin tells the brain the party is live. Its first say is the greeting.
func (v *voiceSession) begin(direction string) {
	v.send(brainMsg{
		Type: "start", CallID: v.callID, Peer: v.peer, PeerName: v.peerName,
		Language: v.opts.Language, Direction: direction,
	})
}

// pumpMic feeds the caller's audio to the transcriber as it arrives, so by the
// time they stop talking the recogniser is essentially done.
func (v *voiceSession) pumpMic(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-v.sink.Frames():
			if !ok {
				return
			}
			// One line, once. "No reply" has three possible meanings — the call
			// carried no audio, the audio never reached transcription, or
			// transcription returned nothing — and from the outside they look
			// identical. This separates the first two from the third.
			if v.micFrames++; v.micFrames == 1 {
				v.svc.log.Infof("call %s: first audio frame reached transcription (%d bytes)", v.callID, len(frame))
			}
			// WACLI_VOICE_DUMP writes exactly what transcription is fed, so the
			// audio can be listened to and measured instead of reasoned about.
			// Sarvam transcribes a synthetic sample perfectly and this stream not
			// at all, which means the bytes differ from what they should be —
			// wrong rate, wrong scale or silence — and only the bytes can say.
			if v.dump != nil {
				_, _ = v.dump.Write(frame)
			}
			// Every ~30s, not every ~3s: enough to tell a silent line from a
			// working one in a log after the fact, quiet enough to live with.
			if v.micFrames%500 == 0 {
				v.svc.log.Infof("call %s: %d frames to transcription, level %.4f",
					v.callID, v.micFrames, pcmLevel(frame))
			}
			if err := v.stt.Send(ctx, frame); err != nil {
				if ctx.Err() == nil {
					v.svc.log.Warnf("call %s: transcription send: %v", v.callID, err)
				}
				return
			}
		}
	}
}

// pumpSpeech turns transcription into settled utterances.
//
// The FINAL TRANSCRIPT is the utterance boundary, not the VAD's end-of-speech:
// the words arrive ~100–200ms after the end-of-speech signal, and flushing on
// the signal answered everything one utterance late. Barge-in fires on
// speech START — the point is to stop talking over them, and waiting for words
// would talk over them for the length of a phrase.
func (v *voiceSession) pumpSpeech(ctx context.Context) {
	var pending strings.Builder
	var mu sync.Mutex
	var quiet *time.Timer
	defer func() {
		mu.Lock()
		if quiet != nil {
			quiet.Stop()
		}
		mu.Unlock()
	}()
	for ev := range v.stt.Events() {
		if v.sttEvents++; v.sttEvents == 1 {
			v.svc.log.Infof("call %s: transcription is responding (first event kind %v)", v.callID, ev.Kind)
		}
		switch ev.Kind {
		case sarvam.SpeechStart:
			// Stop mid-word rather than talking over them.
			v.src.Flush()

		case sarvam.Transcript:
			if !ev.Final || strings.TrimSpace(ev.Text) == "" {
				continue
			}
			mu.Lock()
			pending.WriteString(strings.TrimSpace(ev.Text))
			pending.WriteByte(' ')
			if quiet != nil {
				quiet.Stop()
			}
			quiet = time.AfterFunc(settleFor, func() {
				mu.Lock()
				utterance := strings.TrimSpace(pending.String())
				pending.Reset()
				mu.Unlock()
				if utterance == "" {
					return
				}
				v.offer(utterance)
			})
			mu.Unlock()

		case sarvam.SpeechEnd:
			// VAD only; the words for this utterance are still in flight.
		}
	}
}

// offer hands the newest utterance to the turn runner, displacing any older one
// that has not started. Answering what the caller has already moved past is how
// a conversation falls permanently one turn behind.
func (v *voiceSession) offer(utterance string) {
	select {
	case <-v.turns:
		v.svc.log.Infof("call %s: dropped an utterance the caller talked over", v.callID)
	default:
	}
	select {
	case v.turns <- utterance:
	default:
	}
}

// runTurns forwards one utterance at a time to the brain, with an immediate
// spoken acknowledgement so the model's second of thinking is not a dead line.
func (v *voiceSession) runTurns(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case utterance, ok := <-v.turns:
			if !ok {
				return
			}
			v.svc.log.Infof("call %s: heard %q", v.callID, utterance)
			if !v.opts.NoFiller {
				v.filled++
				// Flushed immediately: Speak only queues, synthesis happens on
				// Flush, and an unflushed filler fuses into the next reply.
				_ = v.tts.Speak(ctx, voiceFillers[v.filled%len(voiceFillers)])
				_ = v.tts.Flush(ctx)
			}
			v.send(brainMsg{Type: "utterance", Text: utterance})
		}
	}
}

// pumpVoice moves synthesised speech onto the call.
func (v *voiceSession) pumpVoice(ctx context.Context) {
	for chunk := range v.tts.Audio() {
		frames := make([][]float32, 0, len(chunk)/frameBytes+1)
		for off := 0; off+frameBytes <= len(chunk); off += frameBytes {
			frames = append(frames, pcmToFloat(chunk[off:off+frameBytes]))
		}
		v.src.PushFrames(frames)
		_ = ctx
	}
}

// pumpBrain applies the brain's decisions.
func (v *voiceSession) pumpBrain(ctx context.Context) {
	defer v.finish("brain disconnected")
	for {
		_, data, err := v.conn.Read(ctx)
		if err != nil {
			return
		}
		var m brainMsg
		if err := json.Unmarshal(data, &m); err != nil {
			v.svc.log.Warnf("call %s: bad brain message: %v", v.callID, err)
			continue
		}
		switch m.Type {
		case "say":
			if strings.TrimSpace(m.Text) == "" {
				continue
			}
			v.svc.log.Infof("call %s: saying %q", v.callID, m.Text)
			if err := v.tts.Speak(ctx, m.Text); err != nil && ctx.Err() == nil {
				v.svc.log.Warnf("call %s: could not speak: %v", v.callID, err)
				continue
			}
			_ = v.tts.Flush(ctx)
		case "hangup":
			v.finish("brain hung up")
			return
		}
	}
}

// send writes one message to the brain, serialised across goroutines.
func (v *voiceSession) send(m brainMsg) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), brainWriteTimeout)
	defer cancel()
	v.mu.Lock()
	defer v.mu.Unlock()
	_ = v.conn.Write(ctx, websocket.MessageText, data)
}

// finish tears the session down exactly once, whichever side ended it first.
func (v *voiceSession) finish(reason string) {
	v.closed.Do(func() {
		v.send(brainMsg{Type: "ended", Reason: reason})
		v.cancel()
		_ = v.src.Close()
		_ = v.sink.Close()
		_ = v.stt.Close()
		_ = v.tts.Close()
		_ = v.conn.Close(websocket.StatusNormalClosure, "call ended")
		if v.hangup != nil {
			_ = v.hangup()
		}
		v.svc.notifyObserver("call_ended", map[string]any{"call_id": v.callID, "reason": reason})
		v.svc.log.Infof("spoken call %s finished: %s", v.callID, reason)
		// Last, and here rather than at each call site: this is the one place
		// every spoken call passes through on its way out. The plain call path
		// released the slot from its media-end callback and the spoken path
		// never did, so a single spoken call held the queue for the life of the
		// process and every call after it — including answering an incoming one
		// — was refused with "another call is already in progress".
		// releaseAndDrain may start the next queued call, so it goes after the
		// teardown above rather than before it.
		v.svc.queue.finished(v.svc, v.callID)
	})
}

// PlaceSpokenCall rings the contact and holds a live conversation driven by the brain.
func (s *Service) PlaceSpokenCall(ctx context.Context, recipient string, opts VoiceBrainOptions) (CallInfo, error) {
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

	sess, err := s.startVoiceSession(ctx, opts, "", jid.String(), target.Name)
	if err != nil {
		s.queue.releaseAndDrain(s)
		return CallInfo{}, err
	}

	call, err := s.media.client.CallWithOptions(ctx, jid.String(), meowcaller.CallOptions{})
	if err != nil {
		sess.finish("offer failed")
		s.queue.releaseAndDrain(s)
		return CallInfo{}, fmt.Errorf("offer call: %w", err)
	}
	s.media.track(s, call)
	sess.callID = call.ID()
	sess.hangup = call.Hangup

	call.Receive(sess.sink)
	call.OnPeerAccept(func() {
		call.Play(sess.src)
		sess.begin("outgoing")
	})
	s.media.onEnd(call.ID(), func() { sess.finish("call ended") })

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
	s.log.Infof("placed spoken call %s to %s", info.CallID, jid)
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

// AnswerSpokenCall accepts a ringing call into a live conversation.
//
// The pipeline is opened BEFORE the call is answered, so the greeting starts
// the moment the ring stops; the sink attaches before Answer so the caller's
// opening words are not lost; and there is no peer-accept to wait for — we are
// the acceptor. The inbound direction also sidesteps the flakiest part of
// placing: the peer placed this call, so their phone is already transmitting.
func (s *Service) AnswerSpokenCall(ctx context.Context, ref string, opts VoiceBrainOptions) (CallInfo, error) {
	if s.media == nil {
		return CallInfo{}, errors.New("media stack is not initialised")
	}
	callID := ref
	if resolved, err := s.calls.resolve(ref); err == nil {
		callID = resolved
	}
	// The offer fans out to two handlers: one announces the call over the
	// webhook, the other decrypts the key and prepares media before registering
	// it as live. A consumer that answers the moment the webhook lands arrives
	// here in about a millisecond and loses that race; the ring lasts seconds.
	var call *meowcaller.Call
	var err error
	for waited := time.Duration(0); ; waited += 100 * time.Millisecond {
		call, err = s.media.get(callID)
		if err == nil {
			break
		}
		if waited >= 3*time.Second {
			return CallInfo{}, err
		}
		select {
		case <-ctx.Done():
			return CallInfo{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !s.queue.acquire() {
		return CallInfo{}, fmt.Errorf("another call is active (%s) — end it first", s.queue.activeCall())
	}
	s.queue.hold(call.ID())

	peerName := ""
	if info, ok := s.calls.get(call.ID()); ok {
		peerName = info.PeerName
	}
	sess, err := s.startVoiceSession(ctx, opts, call.ID(), call.Peer().String(), peerName)
	if err != nil {
		s.queue.finished(s, call.ID())
		return CallInfo{}, err
	}
	sess.hangup = call.Hangup

	call.Receive(sess.sink)
	s.media.onEnd(call.ID(), func() { sess.finish("call ended") })

	if err := call.Answer(); err != nil {
		sess.finish("answer failed")
		s.queue.finished(s, call.ID())
		return CallInfo{}, fmt.Errorf("answer: %w", err)
	}
	call.Play(sess.src)
	sess.begin("incoming")

	s.log.Infof("answered spoken call %s from %s", call.ID(), call.Peer())
	if info, ok := s.calls.setState(call.ID(), CallStateAccepted); ok {
		s.dispatchWebhook("call.accepted", callPayload(*info))
		return *info, nil
	}
	return CallInfo{CallID: call.ID(), State: CallStateAccepted, Direction: CallDirectionIn}, nil
}

// sarvamAPIKey is read at call time rather than held, so a key added after the
// daemon started is picked up by the next call.
func sarvamAPIKey() string { return strings.TrimSpace(os.Getenv("SARVAM_API_KEY")) }

// pcmLevel is the RMS of a 16-bit little-endian PCM buffer, normalised to 0..1.
// Near-zero means the caller's audio never really arrived, whatever the frame
// counts say.
func pcmLevel(pcm []byte) float64 {
	if len(pcm) < 2 {
		return 0
	}
	var sum float64
	n := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		sum += v * v
		n++
	}
	if n == 0 {
		return 0
	}
	return math.Sqrt(sum/float64(n)) / 32768
}
