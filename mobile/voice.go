package mobile

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/MelloB1989/wacli/wa"
)

// Streaming voice calls, bound for the host.
//
// Everything else in this package goes through Request, and a streaming call could too — the route
// is POST /calls/stream. It gets its own binding for one reason: the cached scripted lines are raw
// PCM, hundreds of kilobytes of it, and pushing that through a JSON string as base64 on every call
// means encoding and decoding megabytes on a phone to move bytes that []byte already carries.
//
// Audio itself never appears here. The Go side owns the relay socket end to end; the host receives
// state and transcripts, which is all it can usefully act on.

// VoiceHandler receives the progress of a streaming call. Implemented by the host in Kotlin/Swift.
//
// Callbacks arrive on Go goroutines, so hop to the UI thread before touching UI.
type VoiceHandler interface {
	// OnState reports the call lifecycle: ringing, connected, listening, thinking, speaking, ended.
	OnState(state string)
	// OnTranscript reports what the peer said. Partial results arrive with final=false.
	OnTranscript(text string, final bool)
	// OnEnded fires exactly once, whatever ended the call.
	OnEnded(reason string)
}

var (
	cacheMu     sync.Mutex
	cachedLines = map[string][]byte{}
)

// AddCachedLine stores pre-rendered speech for a scripted line.
//
// pcm is signed 16-bit little-endian, 16 kHz, mono — the format the call carries natively, so
// playing it costs no conversion and no network round trip. Call this before StartVoiceCall.
func AddCachedLine(id string, pcm []byte) error {
	if id == "" {
		return errors.New("cached line id is required")
	}
	if len(pcm)%2 != 0 {
		return errors.New("pcm must be 16-bit samples: odd byte count")
	}
	// Copy: the host's array may be a JNI view that is reused or freed after this returns.
	buf := make([]byte, len(pcm))
	copy(buf, pcm)

	cacheMu.Lock()
	defer cacheMu.Unlock()
	cachedLines[id] = buf
	return nil
}

// ClearCachedLines drops every cached line. Call it between contacts — one grandmother's greeting
// played to another is a bug the user will notice immediately.
func ClearCachedLines() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cachedLines = map[string][]byte{}
}

// StartVoiceCall rings the contact and bridges the call to the relay for a live conversation.
//
// to is a phone number in international format or any reference Request accepts. relayURL and token
// come from the backend, which mints the token after checking quota. language is a Sarvam code such
// as "hi-IN"; voice is a speaker id. handler may be nil.
//
// It returns as soon as the call is offered. Everything after that arrives on the handler.
func StartVoiceCall(to, relayURL, token, language, voice string, handler VoiceHandler) error {
	mu.Lock()
	svc := service
	mu.Unlock()
	if svc == nil {
		return errors.New("wacli is not running: call Start first")
	}

	cacheMu.Lock()
	lines := make(map[string][]byte, len(cachedLines))
	for id, pcm := range cachedLines {
		lines[id] = pcm
	}
	cacheMu.Unlock()

	opts := wa.VoiceStreamOptions{
		RelayURL:    relayURL,
		Token:       token,
		Language:    language,
		Voice:       voice,
		CachedLines: lines,
	}
	if handler != nil {
		opts.OnState = func(state string) { safeVoice(func() { handler.OnState(state) }) }
		opts.OnTranscript = func(text string, final bool) {
			safeVoice(func() { handler.OnTranscript(text, final) })
		}
		opts.OnEnded = func(reason string) { safeVoice(func() { handler.OnEnded(reason) }) }
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := svc.PlaceStreamingCall(ctx, to, opts)
	return err
}

// EndVoiceCall hangs up the call in progress. Safe to call when there is none.
func EndVoiceCall(reason string) error {
	mu.Lock()
	svc := service
	mu.Unlock()
	if svc == nil {
		return errors.New("wacli is not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := svc.EndCall(ctx, "", reason)
	return err
}

// safeVoice keeps a panic crossing back from the host bridge from killing the call goroutine.
func safeVoice(fn func()) {
	defer func() { _ = recover() }()
	fn()
}
