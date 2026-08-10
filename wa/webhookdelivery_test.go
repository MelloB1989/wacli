package wa

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

// Retrying a refusal the consumer meant is pointless — it re-sends a request already rejected — but
// giving up on a transient failure loses the event. This split is the whole point of the retry loop.
func TestRetryableStatus(t *testing.T) {
	cases := map[int]bool{
		200: false, 201: false, 204: false, // success never retries
		400: false, 401: false, 403: false, 404: false, 422: false, // deliberate refusals
		408: true, 429: true, // explicitly invite a retry
		500: true, 502: true, 503: true, 504: true, // the consumer is broken, not refusing
	}
	for code, want := range cases {
		if got := retryableStatus(code); got != want {
			t.Errorf("retryableStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

// Backoff must grow, and must be capped: an uncapped exponential parks the last attempt for minutes.
func TestRetryBackoffGrowsAndIsCapped(t *testing.T) {
	var prev time.Duration
	for attempt := 1; attempt <= 10; attempt++ {
		got := retryBackoff(attempt, 0)
		if attempt > 1 && got < prev {
			t.Errorf("attempt %d waited %v, less than the previous %v", attempt, got, prev)
		}
		if got > maxBackoff+time.Second {
			t.Errorf("attempt %d waited %v, above the %v cap", attempt, got, maxBackoff)
		}
		prev = got
	}
	if first := retryBackoff(1, 0); first != time.Second {
		t.Errorf("first retry waited %v, want 1s", first)
	}
}

// Jitter spreads simultaneous failures so consumers are not hit by a synchronised thundering herd,
// while staying deterministic per delivery.
func TestRetryBackoffJitterIsPerDeliveryAndStable(t *testing.T) {
	a, b := retryBackoff(3, 1), retryBackoff(3, 2)
	if a == b {
		t.Error("two deliveries share a backoff; they will retry in lockstep")
	}
	if again := retryBackoff(3, 1); again != a {
		t.Errorf("backoff is not deterministic: %v then %v", a, again)
	}
}

// The signature is what a consumer trusts. The versioned form covers the timestamp as well as the
// body, so a captured request cannot be replayed forever.
func TestSignWebhook(t *testing.T) {
	body := []byte(`{"event":"incoming_message"}`)
	ts := time.Unix(1700000000, 0)

	legacy, versioned, stamp := signWebhook("hunter2", body, ts)
	if stamp != "1700000000" {
		t.Errorf("timestamp = %q, want 1700000000", stamp)
	}

	// Legacy stays body-only, so consumers written against the old scheme keep verifying.
	want := hmac.New(sha256.New, []byte("hunter2"))
	want.Write(body)
	if expect := "sha256=" + hex.EncodeToString(want.Sum(nil)); legacy != expect {
		t.Errorf("legacy signature = %q, want %q", legacy, expect)
	}

	// Versioned covers timestamp + "." + body.
	v := hmac.New(sha256.New, []byte("hunter2"))
	v.Write([]byte(stamp))
	v.Write([]byte("."))
	v.Write(body)
	if expect := "v1=" + hex.EncodeToString(v.Sum(nil)); versioned != expect {
		t.Errorf("versioned signature = %q, want %q", versioned, expect)
	}

	// A different timestamp must produce a different signature, or signing it bought nothing.
	_, otherVersioned, _ := signWebhook("hunter2", body, ts.Add(time.Second))
	if otherVersioned == versioned {
		t.Error("signature ignores the timestamp; a captured request would replay forever")
	}

	// No secret, no headers — rather than signing with an empty key, which would look valid.
	if l, v, s := signWebhook("", body, ts); l != "" || v != "" || s != "" {
		t.Errorf("unsecured webhook produced signature headers: %q %q %q", l, v, s)
	}
}

// A consumer has to be able to recompute the signature from the headers alone.
func TestSignatureVerifiesFromHeaders(t *testing.T) {
	secret, body := "s3cret", []byte(`{"payload":{"text":"hi"}}`)
	_, versioned, stamp := signWebhook(secret, body, time.Now())

	unix, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		t.Fatalf("timestamp header is not an integer: %v", err)
	}
	if age := time.Since(time.Unix(unix, 0)); age > time.Minute {
		t.Errorf("timestamp is %v old at signing time", age)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp))
	mac.Write([]byte("."))
	mac.Write(body)
	if !hmac.Equal([]byte(versioned), []byte("v1="+hex.EncodeToString(mac.Sum(nil)))) {
		t.Error("a consumer following the documented scheme cannot verify the signature")
	}
}
