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
	"sync/atomic"
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
//	                {"type":"utterance", "id", "text", "interrupted"}
//	                {"type":"ended", "reason"}
//	brain → wacli:  {"type":"say", "text", "for"}   — speak this to the caller
//	                {"type":"hangup"}               — end the call, once said
//
// The greeting is simply the first say after start. "id" numbers utterances
// and "for" names the one a say answers, so a reply to something the caller
// has already moved past can be recognised and dropped instead of spoken.
// "interrupted" tells the brain its previous reply was cut off or never
// heard, so it can pick up rather than assume it landed. A say with no "for"
// is the brain speaking on its own — a task finishing mid-call — and is
// always welcome. Anything a future brain needs beyond this earns its way in
// as a new field, not a new socket.

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

	// bargeConfirm is how long a sound from the caller has to last before it
	// counts as an interruption. Playback PAUSES the instant the detector
	// fires — a caller who starts talking hears KARMAX stop at once — but the
	// queued speech is only DISCARDED once the sound has lasted this long. A
	// cough, a laugh, a chair scraping: the detector fires, nothing follows,
	// and playback resumes from the same frame with nothing lost. Real
	// interruptions ("wait", "no", "hang on") are longer than this.
	bargeConfirm = 250 * time.Millisecond

	// hangupDrain bounds how long a hangup waits for the goodbye to finish
	// playing. Cutting "bye" in half is worse than a second's delay.
	hangupDrain = 8 * time.Second

	// holdForCaller bounds how long a reply waits for the caller to stop
	// talking before it is spoken anyway. Not speaking over somebody is the
	// rule; a caller who never pauses gets answered regardless.
	holdForCaller = 4 * time.Second
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
	// ID numbers an utterance; For names the utterance a say answers.
	ID  int64 `json:"id,omitempty"`
	For int64 `json:"for,omitempty"`
	// Interrupted marks an utterance spoken before the previous reply was
	// heard in full.
	Interrupted bool `json:"interrupted,omitempty"`
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

	// Turn-taking state, all safe across the pumps.
	//
	// utterSeq numbers utterances; latest is the newest one sent to the brain,
	// which is the only one a say may still answer. talking is the detector's
	// view of the caller. cutReply records that the last reply was interrupted
	// or dropped unheard, and rides out on the next utterance. discard drops
	// synthesis output that belongs to a reply the caller cut off — the
	// synthesiser keeps producing after a barge-in, and without this the
	// interrupted sentence came back a second later as if nothing happened.
	utterSeq atomic.Int64
	latest   atomic.Int64
	talking  atomic.Bool
	cutReply atomic.Bool
	discard  atomic.Bool
	// bargeMu guards the pause-then-confirm timer.
	bargeMu    sync.Mutex
	bargeTimer *time.Timer
	// ttsMu guards tts, which is replaced after a confirmed barge-in so
	// nothing of the interrupted reply survives on the wire.
	ttsMu sync.RWMutex
	// lastSay is when text was last handed to synthesis and lastAudio when
	// its audio last arrived; together they say whether we are mid-reply,
	// since the synthesiser reports no completion of its own. replyPlaying
	// tells a cut reply from a cut filler: interrupting "mm-hm" is not
	// interrupting an answer, and must not be reported to the brain as one.
	sayMu        sync.Mutex
	lastSay      time.Time
	lastAudio    time.Time
	replyPlaying bool

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

// pumpSpeech turns transcripts into utterances and detector events into
// barge-in.
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
			v.talking.Store(true)
			v.onCallerSound()

		case sarvam.SpeechEnd:
			v.talking.Store(false)
			v.onCallerQuiet()

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
		}
	}
}

// onCallerSound is the detector reporting the caller began making a sound.
//
// If nothing is playing there is nothing to interrupt. Otherwise playback
// pauses immediately and a timer decides what the sound was: still going at
// bargeConfirm and it is speech, so the reply is discarded; over before then
// and it was a noise, so the reply resumes where it stopped.
func (v *voiceSession) onCallerSound() {
	if !v.speakingNow() {
		return
	}
	v.src.Pause()
	v.bargeMu.Lock()
	defer v.bargeMu.Unlock()
	if v.bargeTimer != nil {
		v.bargeTimer.Stop()
	}
	v.bargeTimer = time.AfterFunc(bargeConfirm, func() {
		// Still talking, and we were still mid-sentence: an interruption.
		// Anything else — the sound stopped, or the reply finished on its
		// own during the wait — and playback just carries on.
		if !v.talking.Load() || !v.speakingNow() {
			v.src.Resume()
			return
		}
		v.interrupt()
	})
}

// onCallerQuiet is the detector reporting the sound stopped. If it stopped
// before the confirm timer fired, it was not an interruption.
func (v *voiceSession) onCallerQuiet() {
	v.bargeMu.Lock()
	timer := v.bargeTimer
	v.bargeTimer = nil
	v.bargeMu.Unlock()
	if timer != nil && timer.Stop() {
		v.src.Resume()
	}
}

// interrupt is a confirmed barge-in: the caller is talking over a reply.
//
// The queue is flushed and synthesis output is discarded until the next thing
// we deliberately say — the synthesiser is still producing the rest of the
// reply, and letting that through played the interrupted sentence back a
// second later. Then the synthesis socket is replaced outright, so nothing of
// the old reply can arrive on it at all: a discard flag alone leaves a window
// where the tail of the old reply and the head of the new one share a stream.
func (v *voiceSession) interrupt() {
	played, _, _ := v.src.Stats()
	v.src.Flush()
	v.discard.Store(true)
	// Only a cut REPLY is reported to the brain. A cut filler is nothing —
	// the caller kept talking through "mm-hm", which is what fillers are for.
	if v.isReplyPlaying() {
		v.cutReply.Store(true)
	}
	v.svc.log.Infof("call %s: caller interrupted after %d frames; dropping the rest", v.callID, played)
	go v.replaceTTS()
}

// speakingNow reports whether KARMAX is in the middle of saying something:
// audio queued, audio still arriving, or text handed to synthesis whose first
// audio has not come back yet. The queue alone is not enough — it is
// momentarily empty between chunks of a reply that is very much still being
// spoken.
func (v *voiceSession) speakingNow() bool {
	if v.src.Depth() > 0 {
		return true
	}
	v.sayMu.Lock()
	defer v.sayMu.Unlock()
	if time.Since(v.lastAudio) < 400*time.Millisecond {
		return true
	}
	return time.Since(v.lastSay) < 2*time.Second && v.lastAudio.Before(v.lastSay)
}

// synthesising is speakingNow with the queue left out — used where the queue
// has already been checked.
func (v *voiceSession) synthesising() bool {
	v.sayMu.Lock()
	defer v.sayMu.Unlock()
	if time.Since(v.lastAudio) < 400*time.Millisecond {
		return true
	}
	return time.Since(v.lastSay) < 2*time.Second && v.lastAudio.Before(v.lastSay)
}

func (v *voiceSession) markSay(reply bool) {
	v.sayMu.Lock()
	defer v.sayMu.Unlock()
	v.lastSay = time.Now()
	v.replyPlaying = reply
}

func (v *voiceSession) markAudio() {
	v.sayMu.Lock()
	defer v.sayMu.Unlock()
	v.lastAudio = time.Now()
}

func (v *voiceSession) isReplyPlaying() bool {
	v.sayMu.Lock()
	defer v.sayMu.Unlock()
	return v.replyPlaying
}

// replaceTTS swaps in a fresh synthesis socket. Anything still arriving on the
// old one belonged to a reply the caller cut off.
func (v *voiceSession) replaceTTS() {
	language := strings.TrimSpace(v.opts.Language)
	if language == "" {
		language = "en-IN"
	}
	ctx, cancel := context.WithTimeout(context.Background(), brainHandshakeTimeout)
	defer cancel()
	fresh, err := sarvam.DialTTS(ctx, sarvam.Config{APIKey: sarvamAPIKey()}, language, strings.TrimSpace(v.opts.Voice), 0)
	if err != nil {
		// The old socket stays; the discard flag still protects the caller
		// from the interrupted reply, just less airtightly.
		v.svc.log.Warnf("call %s: could not replace the synthesiser after an interruption: %v", v.callID, err)
		return
	}
	v.ttsMu.Lock()
	old := v.tts
	v.tts = fresh
	v.ttsMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

func (v *voiceSession) currentTTS() *sarvam.TTS {
	v.ttsMu.RLock()
	defer v.ttsMu.RUnlock()
	return v.tts
}

// speak is the one way anything is said. It ends any discard in force —
// this is deliberately new speech — and flushes so it is synthesised now
// rather than fused into whatever comes next.
func (v *voiceSession) speak(ctx context.Context, text string, reply bool) error {
	v.discard.Store(false)
	v.markSay(reply)
	t := v.currentTTS()
	if t == nil {
		return errors.New("no synthesiser")
	}
	if err := t.Speak(ctx, text); err != nil {
		return err
	}
	return t.Flush(ctx)
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
			id := v.utterSeq.Add(1)
			v.latest.Store(id)
			interrupted := v.cutReply.Swap(false)
			v.svc.log.Infof("call %s: heard %q", v.callID, utterance)
			if !v.opts.NoFiller {
				v.filled++
				_ = v.speak(ctx, voiceFillers[v.filled%len(voiceFillers)], false)
			}
			v.send(brainMsg{Type: "utterance", ID: id, Text: utterance, Interrupted: interrupted})
		}
	}
}

// pumpVoice moves synthesised speech onto the call.
//
// Push, not PushFrames: synthesis chunks are not frame-aligned — measured, not
// one in a hundred was — and PushFrames dropped each chunk's tail. The most
// common chunk was one frame plus forty milliseconds, so two fifths of every
// reply was simply missing, heard as a voice that never quite came through.
// Push carries the remainder into the next chunk and loses nothing.
//
// The synthesiser can be replaced mid-call (see interrupt), so this follows
// whichever socket is current rather than the one it started with.
func (v *voiceSession) pumpVoice(ctx context.Context) {
	for {
		t := v.currentTTS()
		if t == nil {
			return
		}
		for chunk := range t.Audio() {
			if v.discard.Load() {
				continue
			}
			v.markAudio()
			v.src.Push(chunk)
		}
		if ctx.Err() != nil {
			return
		}
		// The channel closed. If the socket was replaced there is a new one
		// to follow; if it simply died, dial another rather than go mute for
		// the rest of the call.
		if v.currentTTS() == t {
			v.replaceTTS()
			if v.currentTTS() == t {
				return
			}
		}
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
			// A reply to an utterance the caller has already talked past is
			// dropped, and the brain is told on the next turn that it went
			// unheard. Replies with no "for" are the brain speaking of its
			// own accord and are always delivered.
			if m.For != 0 && m.For != v.latest.Load() {
				v.svc.log.Infof("call %s: dropped a stale reply to utterance %d (caller is on %d)", v.callID, m.For, v.latest.Load())
				v.cutReply.Store(true)
				continue
			}
			v.waitForCallerPause(ctx)
			v.svc.log.Infof("call %s: saying %q", v.callID, m.Text)
			if err := v.speak(ctx, m.Text, true); err != nil && ctx.Err() == nil {
				v.svc.log.Warnf("call %s: could not speak: %v", v.callID, err)
			}
		case "hangup":
			v.drainThenFinish("brain hung up")
			return
		}
	}
}

// waitForCallerPause holds a reply while the caller is mid-sentence. Speaking
// the moment a reply is ready, over the top of somebody, is the one thing every
// caller notices; waiting a beat for them to finish is what a person does.
func (v *voiceSession) waitForCallerPause(ctx context.Context) {
	if !v.talking.Load() {
		return
	}
	deadline := time.After(holdForCaller)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
		case <-tick.C:
			if !v.talking.Load() {
				// A breath's worth of gap so the reply lands after their
				// last word rather than on top of it.
				time.Sleep(150 * time.Millisecond)
				return
			}
		}
	}
}

// drainThenFinish lets whatever is being said finish before the line drops.
// The brain hangs up right after its goodbye; hanging up the moment the
// message arrives cut the goodbye in half every time.
func (v *voiceSession) drainThenFinish(reason string) {
	deadline := time.Now().Add(hangupDrain)
	// Synthesis needs a moment to deliver the goodbye before the queue means
	// anything: an empty queue at t=0 reads as "nothing left to say".
	time.Sleep(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		if v.src.Depth() > 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if !v.synthesising() {
			break
		}
		// Recently speaking, nothing queued: between chunks, or done. Give it
		// one more beat before deciding it is done.
		time.Sleep(250 * time.Millisecond)
		if v.src.Depth() == 0 {
			break
		}
	}
	// One more frame so the last queued frame actually plays out.
	time.Sleep(120 * time.Millisecond)
	v.finish(reason)
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
		if t := v.currentTTS(); t != nil {
			_ = t.Close()
		}
		_ = v.conn.Close(websocket.StatusNormalClosure, "call ended")
		if v.hangup != nil {
			_ = v.hangup()
		}
		if v.dump != nil {
			_ = v.dump.Close()
		}
		played, underruns, dropped := v.src.Stats()
		v.svc.notifyObserver("call_ended", map[string]any{"call_id": v.callID, "reason": reason})
		// The numbers a caller's experience is made of, in the one line
		// anybody reads after a call: underruns are stutters, drops are skips.
		v.svc.log.Infof("spoken call %s finished: %s (played %d frames, %d underruns, %d dropped)",
			v.callID, reason, played, underruns, dropped)
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
