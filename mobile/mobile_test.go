package mobile

import (
	"net/http"
	"strings"
	"testing"
)

func TestResponseRecorderDefaultsToOK(t *testing.T) {
	// A handler that writes a body without calling WriteHeader must be recorded as 200, or every
	// successful call would look like a failure to Request.
	recorder := &responseRecorder{status: http.StatusOK}
	if _, err := recorder.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if recorder.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.status)
	}
	if got := recorder.buf.String(); got != `{"ok":true}` {
		t.Fatalf("body = %q", got)
	}
}

func TestResponseRecorderKeepsFirstStatus(t *testing.T) {
	// net/http ignores a second WriteHeader, and so must this: wacli's error paths write a status
	// and then a body, and a recorder that let the body reset the status would turn a 400 into 200.
	recorder := &responseRecorder{status: http.StatusOK}
	recorder.WriteHeader(http.StatusBadRequest)
	recorder.WriteHeader(http.StatusTeapot)
	if recorder.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.status)
	}
}

func TestResponseRecorderHeaderIsUsable(t *testing.T) {
	recorder := &responseRecorder{status: http.StatusOK}
	recorder.Header().Set("Content-Type", "application/json")
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestRequestBeforeStart(t *testing.T) {
	// The bindings are a foreign-language boundary; "not running" has to come back as a clean error
	// rather than a nil-pointer panic crossing into Kotlin or Swift.
	if _, err := Request(http.MethodGet, "/status", ""); err == nil {
		t.Fatal("expected an error when the service is not running")
	} else if !strings.Contains(err.Error(), "not running") {
		t.Fatalf("error = %v, want it to mention the service is not running", err)
	}
}

func TestConfigureRejectsEmptyHome(t *testing.T) {
	// Mobile has no home directory to fall back on, so an empty path is a caller bug worth naming
	// rather than something to paper over with a default that would not exist on the device.
	if err := Configure("  "); err == nil {
		t.Fatal("expected an error for an empty home directory")
	}
}

func TestStartPairingLoginRejectsEmptyPhone(t *testing.T) {
	if err := StartPairingLogin(nopLoginHandler{}, "not a phone number"); err == nil {
		t.Fatal("expected an error for a phone number with no digits")
	}
}

type nopLoginHandler struct{}

func (nopLoginHandler) OnQRCode(string)      {}
func (nopLoginHandler) OnPairingCode(string) {}
func (nopLoginHandler) OnStatus(string)      {}
func (nopLoginHandler) OnError(string)       {}
