// Package mobile is the gomobile binding surface for wacli — the API the Android and iOS bindings
// are generated from, and the only Go the Expo module talks to.
//
// # Why the API looks like this
//
// gomobile can only bind a narrow slice of Go: signed integers, floats, string, bool, []byte,
// error in the final return position, and interfaces declared in the bound package. No maps, no
// slices of structs, no channels. So the whole of wacli's rich HTTP surface is exposed through one
// string-in/string-out call, Request, and everything asynchronous is delivered through callback
// interfaces the host implements in Kotlin or Swift.
//
// # No socket
//
// The daemon normally serves its API on 127.0.0.1:8765 and the CLI talks to it over TCP. Embedded
// in an app, both halves live in one process, so Request dispatches straight into the same
// http.Handler the daemon serves and never opens a listening socket. That matters beyond tidiness:
// binding a port would collide with a second copy of the app, and on iOS it would trip the local
// network permission prompt for no reason. It also means the API's lack of authentication is not a
// concern here the way it would be over a network — nothing outside the process can reach it.
//
// # Threading
//
// Every exported function is safe to call from any thread. Callbacks arrive on Go goroutines, which
// on Android means a thread with no Looper and on iOS a thread that is not the main queue; hosts
// must hop to their own UI thread before touching UI.
package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/MelloB1989/wacli/wa"
	"go.mau.fi/whatsmeow"
)

// LoginHandler receives the progress of a pairing attempt. Implemented by the host in Kotlin/Swift.
type LoginHandler interface {
	// OnQRCode delivers the raw QR payload to render. Called repeatedly: WhatsApp rotates the code
	// every ~20 seconds and each rotation supersedes the last.
	OnQRCode(code string)
	// OnPairingCode delivers the 8-character code the user types into WhatsApp on their phone.
	// Only fires for StartPairingLogin.
	OnPairingCode(code string)
	// OnStatus reports a lifecycle transition: "connecting", "success", "timeout", or "cancelled".
	OnStatus(status string)
	// OnError reports a failure that ended the attempt.
	OnError(message string)
}

// EventHandler receives live events while the service runs: incoming_message, outgoing_message,
// connection_state and sync_complete. Implemented by the host.
type EventHandler interface {
	// OnEvent hands over the event name and its payload as a JSON object string. The payload is the
	// same shape a webhook subscriber receives, minus the delivery envelope.
	OnEvent(event string, payloadJSON string)
}

var (
	mu      sync.Mutex
	store   *wa.Store
	service *wa.Service

	handlerMu    sync.RWMutex
	eventHandler EventHandler

	loginMu     sync.Mutex
	loginCancel context.CancelFunc
)

// Configure points wacli at a state directory and must be called before Start or any login, since
// neither Android nor iOS has a home directory for the default ~/.wacli to resolve against.
//
// Pass the app's private data directory — Context.getFilesDir() on Android, Application Support on
// iOS. Both are inside the app sandbox, which is what keeps the session keys and message history
// out of reach of other apps. Do not pass a path on external storage.
func Configure(homeDir string) error {
	mu.Lock()
	defer mu.Unlock()
	if strings.TrimSpace(homeDir) == "" {
		return fmt.Errorf("wacli: home directory is required")
	}
	return wa.Configure(homeDir)
}

// IsPaired reports whether a WhatsApp session already exists, i.e. whether the app should call
// Start or send the user through a login flow. Safe to call before Start.
func IsPaired() bool {
	mu.Lock()
	defer mu.Unlock()
	if service != nil {
		return service.Client().Store.ID != nil
	}
	// Opening a throwaway service is the only way to answer this before Start, since the answer
	// lives in the session database. It is cheap: no connection is made.
	tmpStore, tmpService, err := open()
	if err != nil {
		return false
	}
	paired := tmpService.Client().Store.ID != nil
	_ = tmpService.Close()
	_ = tmpStore.Close()
	return paired
}

// IsRunning reports whether the service is started.
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return service != nil
}

// IsLoggingIn reports whether a login attempt is in flight: a QR or pairing-code channel is open and
// waiting for the user to confirm from WhatsApp.
//
// This exists because a login is indistinguishable from a live session through IsRunning — beginLogin
// installs its service the same way Start does — and the two want opposite treatment when the host
// app leaves the foreground. Pairing by code has to be typed into WhatsApp on the same phone, so
// backgrounding is a step *inside* the flow rather than the end of it. A host that stops the service
// there closes the socket the pairing would have completed over, and the attempt cannot recover:
// the ephemeral key and pairing ref live on that client.
func IsLoggingIn() bool {
	loginMu.Lock()
	defer loginMu.Unlock()
	return loginCancel != nil
}

// IsConnected reports whether the WhatsApp socket is currently up. It can be false while IsRunning
// is true — during a reconnect, or when the device has no network.
func IsConnected() bool {
	mu.Lock()
	defer mu.Unlock()
	return service != nil && service.IsConnected()
}

// Start opens the local databases, connects to WhatsApp and begins serving the API to Request.
// It is a no-op if already running.
//
// Requires an existing session: check IsPaired first and run a login flow if it returns false.
func Start() error {
	mu.Lock()
	defer mu.Unlock()
	if service != nil {
		return nil
	}
	newStore, newService, err := open()
	if err != nil {
		return err
	}
	if newService.Client().Store.ID == nil {
		_ = newService.Close()
		_ = newStore.Close()
		return fmt.Errorf("wacli: no WhatsApp session; run a login first")
	}
	if err := newService.Connect(); err != nil {
		_ = newService.Close()
		_ = newStore.Close()
		return fmt.Errorf("wacli: connect: %w", err)
	}
	// The watchdog force-reconnects a socket that still claims to be connected but has stopped
	// delivering. On mobile that is the common case rather than the exotic one: the OS suspends the
	// process on backgrounding and the socket is usually dead by the time it resumes.
	newService.StartConnectionWatchdog()
	attachObserver(newService)
	store, service = newStore, newService
	return nil
}

// Stop disconnects and closes the databases. Safe to call when not running.
//
// Call it when the host tears the service down — Android's Service.onDestroy, or an iOS app heading
// for the background — so SQLite closes its write-ahead log cleanly instead of leaving the next
// start to recover it.
func Stop() error {
	mu.Lock()
	defer mu.Unlock()
	return stopLocked()
}

func stopLocked() error {
	if service == nil {
		return nil
	}
	service.SetEventObserver(nil)
	service.Disconnect()
	serviceErr := service.Close()
	storeErr := store.Close()
	service, store = nil, nil
	if serviceErr != nil {
		return serviceErr
	}
	return storeErr
}

// SetEventHandler installs the listener for live events, replacing any previous one. Pass nil to
// stop receiving them. May be called before or after Start.
func SetEventHandler(handler EventHandler) {
	handlerMu.Lock()
	eventHandler = handler
	handlerMu.Unlock()
}

// Request dispatches a call into the wacli HTTP API in-process and returns the response body.
//
// method is "GET"/"POST"/"PUT"/"DELETE", path is an API path with optional query string
// ("/chats?limit=50"), and body is a JSON string or "" for none. The full route list is in
// docs/ai-harness-reference.md; this is the same handler the daemon serves, so anything the CLI can
// do is reachable here.
//
// A response outside 2xx comes back as an error carrying the status and the body, so the host can
// surface wacli's own message ("chat is locked", "DND mode is off") rather than a generic failure.
func Request(method, path, body string) (string, error) {
	mu.Lock()
	current := service
	mu.Unlock()
	if current == nil {
		return "", fmt.Errorf("wacli: not running; call Start first")
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodGet
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	// The host is only meaningful to the ServeMux, which routes on the path. Nothing resolves it.
	req, err := http.NewRequest(method, "http://wacli.local"+path, reader)
	if err != nil {
		return "", fmt.Errorf("wacli: build request: %w", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := &responseRecorder{status: http.StatusOK}
	wa.NewHTTPHandler(current).ServeHTTP(recorder, req)
	payload := strings.TrimSpace(recorder.buf.String())
	if recorder.status >= 300 {
		if payload == "" {
			return "", fmt.Errorf("wacli: %s %s failed with HTTP %d", method, path, recorder.status)
		}
		return "", fmt.Errorf("wacli: %s", payload)
	}
	return payload, nil
}

// StartLogin begins QR pairing and streams progress to the handler until the attempt succeeds,
// times out, errors or is cancelled by CancelLogin.
//
// On success the service is left running, exactly as Start would leave it, so the host can go
// straight to Request without starting anything else.
//
// It returns as soon as the attempt is under way; everything after that arrives on the handler.
func StartLogin(handler LoginHandler) error {
	return beginLogin(handler, "")
}

// StartPairingLogin begins pairing with an 8-character code instead of a QR, for the phone number
// given in international format ("+15551234567").
//
// This is usually the better flow on mobile: the QR is meant to be scanned by the phone running
// WhatsApp, and when wacli is embedded in an app on that same phone there is nothing to scan it
// with. The code is delivered to LoginHandler.OnPairingCode; the user types it into WhatsApp under
// Linked Devices → Link with phone number.
func StartPairingLogin(handler LoginHandler, phone string) error {
	normalized := wa.NormalizePhone(phone)
	if normalized == "" {
		return fmt.Errorf("wacli: a phone number in international format is required")
	}
	return beginLogin(handler, normalized)
}

func beginLogin(handler LoginHandler, pairPhone string) error {
	if handler == nil {
		return fmt.Errorf("wacli: a login handler is required")
	}
	mu.Lock()
	defer mu.Unlock()
	// A live session holds the session database open, and pairing needs to write a new identity
	// into it. Tear it down first so the attempt starts from a clean slate.
	if err := stopLocked(); err != nil {
		return err
	}
	newStore, newService, err := open()
	if err != nil {
		return err
	}
	// A stale session — one whose device was unlinked from the phone — otherwise makes GetQRChannel
	// fail with "already logged in" and leaves the user stuck with no way back.
	if newService.Client().Store.ID != nil {
		_ = newService.Close()
		_ = newStore.Close()
		wa.ClearSession()
		if newStore, newService, err = open(); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	qrChan, err := newService.Client().GetQRChannel(ctx)
	if err != nil {
		cancel()
		_ = newService.Close()
		_ = newStore.Close()
		return fmt.Errorf("wacli: open QR channel: %w", err)
	}
	if err := newService.Client().Connect(); err != nil {
		cancel()
		_ = newService.Close()
		_ = newStore.Close()
		return fmt.Errorf("wacli: connect: %w", err)
	}

	loginMu.Lock()
	loginCancel = cancel
	loginMu.Unlock()

	store, service = newStore, newService
	attachObserver(newService)
	handler.OnStatus("connecting")
	go runLogin(ctx, cancel, qrChan, handler, newService, pairPhone)
	return nil
}

func runLogin(
	ctx context.Context,
	cancel context.CancelFunc,
	qrChan <-chan whatsmeow.QRChannelItem,
	handler LoginHandler,
	svc *wa.Service,
	pairPhone string,
) {
	defer cancel()
	defer func() {
		loginMu.Lock()
		loginCancel = nil
		loginMu.Unlock()
	}()

	pairRequested := false
	for evt := range qrChan {
		switch evt.Event {
		case "code":
			if pairPhone == "" {
				handler.OnQRCode(evt.Code)
				continue
			}
			// WhatsApp keeps issuing QR rotations even on the pairing-code path; the code itself is
			// requested once and stays valid for the whole attempt.
			if pairRequested {
				continue
			}
			// "Chrome (Linux)", not a wacli-branded string: this goes to WhatsApp verbatim as
			// companion_platform_display, and it has to agree with the platform declared alongside
			// it — PairClientChrome here, which whatsmeow otherwise derives from
			// store.DeviceProps.PlatformType. A display that does not match the platform is
			// rejected, and the rejection arrives as a bare "info query returned status 400".
			// The string is what WhatsApp shows under Linked Devices, so it is cosmetic to us and
			// load-bearing to them.
			code, err := svc.Client().PairPhone(ctx, pairPhone, false, whatsmeow.PairClientChrome, "Chrome (Linux)")
			if err != nil {
				handler.OnError(fmt.Sprintf("pair phone: %v", err))
				return
			}
			pairRequested = true
			handler.OnPairingCode(code)
		case "success":
			// The watchdog is deliberately started only here. Before pairing completes there is no
			// session for it to keep alive, and it would fight the login connection.
			svc.StartConnectionWatchdog()
			handler.OnStatus("success")
			// A first sync fills the chat and contact tables, without which the app opens on an
			// empty list. It is slow enough to be worth doing off the caller's thread.
			go func() {
				if _, err := Request(http.MethodPost, "/sync", "{}"); err != nil {
					handler.OnError(fmt.Sprintf("initial sync: %v", err))
				}
			}()
			return
		case "timeout":
			handler.OnStatus("timeout")
			return
		case "err-client-outdated", "err-scanned-without-multidevice":
			handler.OnError(evt.Event)
			return
		default:
			if evt.Error != nil {
				handler.OnError(evt.Error.Error())
				return
			}
			handler.OnStatus(evt.Event)
		}
	}
	// The channel closing without a terminal event means the context was cancelled.
	if ctx.Err() != nil {
		handler.OnStatus("cancelled")
	}
}

// CancelLogin aborts a login attempt in progress. Safe to call when none is.
func CancelLogin() {
	loginMu.Lock()
	cancel := loginCancel
	loginCancel = nil
	loginMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Logout unlinks this device from WhatsApp, stops the service and erases the local session, so the
// next launch starts unpaired. The message history in the app database is left intact.
//
// Unlinking first is the part that matters. Deleting the local files only makes *this copy* forget:
// the device stays listed under WhatsApp's Linked Devices, and any session exported elsewhere — a
// host that hands the session to a server between calls has one by definition — goes on working.
// A logout that leaves something else placing calls is not a logout.
//
// A failure to reach WhatsApp is returned rather than swallowed, and nothing local is erased in
// that case. Clearing anyway would leave the user with no way to revoke a device they can no longer
// see, which is worse than an error they can retry.
func Logout() error {
	mu.Lock()
	defer mu.Unlock()
	if err := unlinkLocked(); err != nil {
		return err
	}
	if err := stopLocked(); err != nil {
		return err
	}
	wa.ClearSession()
	return nil
}

// unlinkLocked tells WhatsApp to revoke this device. It connects if nothing is running, since the
// common case is a host that keeps the daemon stopped and only starts it around a call.
func unlinkLocked() error {
	svc := service
	if svc == nil {
		newStore, newService, err := open()
		if err != nil {
			return err
		}
		defer func() {
			_ = newService.Close()
			_ = newStore.Close()
		}()
		// Not paired: there is nothing to revoke, and the local clear below is the whole job.
		if newService.Client().Store.ID == nil {
			return nil
		}
		if err := newService.Connect(); err != nil {
			return fmt.Errorf("wacli: connect to log out: %w", err)
		}
		svc = newService
	}
	if svc.Client().Store.ID == nil {
		return nil
	}
	if err := svc.Client().Logout(context.Background()); err != nil {
		return fmt.Errorf("wacli: unlink from WhatsApp: %w", err)
	}
	return nil
}

// Version reports the wacli version this binding was built from.
func Version() string { return version }

// version is overridden at build time with -ldflags "-X github.com/MelloB1989/wacli/mobile.version=...".
var version = "dev"

func open() (*wa.Store, *wa.Service, error) {
	if wa.InitErr != nil {
		return nil, nil, fmt.Errorf("wacli: %w (call Configure first)", wa.InitErr)
	}
	newStore, err := wa.OpenStore(wa.AppDBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("wacli: open store: %w", err)
	}
	newService, err := wa.NewService(newStore)
	if err != nil {
		_ = newStore.Close()
		return nil, nil, fmt.Errorf("wacli: start service: %w", err)
	}
	return newStore, newService, nil
}

func attachObserver(svc *wa.Service) {
	svc.SetEventObserver(func(event string, payload map[string]any) {
		handlerMu.RLock()
		handler := eventHandler
		handlerMu.RUnlock()
		if handler == nil {
			return
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return
		}
		handler.OnEvent(event, string(encoded))
	})
}

// responseRecorder is an http.ResponseWriter that keeps the response in memory.
//
// net/http/httptest has one of these, but importing it drags the testing flag set into a production
// binary — which on Android means an app that registers -test.* flags. It is a dozen lines to
// avoid that.
type responseRecorder struct {
	status  int
	buf     bytes.Buffer
	headers http.Header
	written bool
}

func (r *responseRecorder) Header() http.Header {
	if r.headers == nil {
		r.headers = make(http.Header)
	}
	return r.headers
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	r.written = true
	return r.buf.Write(p)
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.written {
		return
	}
	r.status = status
	r.written = true
}

var _ http.ResponseWriter = (*responseRecorder)(nil)
