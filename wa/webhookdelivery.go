package wa

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Webhook delivery: retries, replay, test-fire, and a signature that expires.
//
// Delivery used to be one POST. If the consumer was restarting, mid-deploy, or briefly unreachable,
// the event was gone — recorded as failed, with no way to get it back. For a transport whose whole
// job is "tell someone else what happened", that is the interesting failure, so:
//
//   - failures retry with exponential backoff and jitter, up to the webhook's MaxAttempts
//   - every attempt is recorded, so a consumer can be debugged after the fact
//   - any recorded delivery can be replayed by ID
//   - an endpoint can be test-fired without waiting for a real event

const (
	// defaultMaxAttempts is generous because the common failure is a consumer that is briefly down,
	// and the backoff below spreads five attempts across roughly a minute.
	defaultMaxAttempts = 5
	// defaultWebhookTimeout bounds one attempt. A slow consumer should not stall the retry chain.
	defaultWebhookTimeout = 10 * time.Second
	// maxBackoff caps the wait so a long chain cannot park an attempt for minutes.
	maxBackoff = 30 * time.Second
)

// retryBackoff returns how long to wait before attempt n (1-based), growing exponentially.
//
// The jitter is deterministic rather than random — derived from the delivery ID — so several
// webhooks failing at once do not retry in lockstep, while a given delivery stays reproducible.
func retryBackoff(attempt int, deliveryID int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
	if base > maxBackoff {
		base = maxBackoff
	}
	jitter := time.Duration(deliveryID%1000) * time.Millisecond
	return base + jitter
}

// retryableStatus reports whether an HTTP status is worth trying again.
//
// 4xx means the consumer understood and refused: retrying sends the same rejected request. The
// exceptions are 408 (timeout) and 429 (rate limited), which explicitly invite a retry.
func retryableStatus(code int) bool {
	switch {
	case code == http.StatusRequestTimeout, code == http.StatusTooManyRequests:
		return true
	case code >= 500:
		return true
	default:
		return false
	}
}

// signWebhook computes the signature headers for a body.
//
// The timestamp is signed alongside the body so a captured request cannot be replayed indefinitely:
// a consumer rejects anything whose timestamp is too old. X-WACLI-Signature stays body-only for
// consumers written against the previous scheme.
func signWebhook(secret string, body []byte, ts time.Time) (legacy, versioned, stamp string) {
	if secret == "" {
		return "", "", ""
	}
	stamp = strconv.FormatInt(ts.Unix(), 10)

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	legacy = "sha256=" + hex.EncodeToString(mac.Sum(nil))

	v := hmac.New(sha256.New, []byte(secret))
	_, _ = v.Write([]byte(stamp))
	_, _ = v.Write([]byte("."))
	_, _ = v.Write(body)
	versioned = "v1=" + hex.EncodeToString(v.Sum(nil))
	return legacy, versioned, stamp
}

// deliverWebhook posts a body and retries on failure, recording every attempt.
//
// It runs to completion in its own goroutine, so a slow or failing consumer never blocks the event
// that produced it.
func (s *Service) deliverWebhook(webhook WebhookRecord, event string, deliveryID int64, body []byte) {
	attempts := webhook.MaxAttempts
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}
	timeout := time.Duration(webhook.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultWebhookTimeout
	}

	var lastErr string
	var lastStatus int
	for attempt := 1; attempt <= attempts; attempt++ {
		status, respBody, err := s.attemptWebhook(webhook, body, timeout)
		lastStatus = status

		switch {
		case err != nil:
			lastErr = err.Error()
		case status < 300:
			if deliveryID > 0 {
				_ = s.store.FinishWebhookDeliveryAttempt(deliveryID, "delivered", status, "", respBody, attempt)
			}
			return
		default:
			lastErr = fmt.Sprintf("HTTP %d: %s", status, respBody)
		}

		// A refusal the consumer meant is not worth repeating.
		if err == nil && !retryableStatus(status) {
			if deliveryID > 0 {
				_ = s.store.FinishWebhookDeliveryAttempt(deliveryID, "failed", status, lastErr, respBody, attempt)
			}
			s.logWebhookFailure(webhook, event, lastErr, attempt, false)
			return
		}

		if attempt < attempts {
			if deliveryID > 0 {
				_ = s.store.RecordWebhookRetry(deliveryID, attempt, lastErr, lastStatus,
					time.Now().Add(retryBackoff(attempt, deliveryID)))
			}
			time.Sleep(retryBackoff(attempt, deliveryID))
			continue
		}

		if deliveryID > 0 {
			_ = s.store.FinishWebhookDeliveryAttempt(deliveryID, "failed", lastStatus, lastErr, respBody, attempt)
		}
		s.logWebhookFailure(webhook, event, lastErr, attempt, true)
	}
}

// attemptWebhook performs exactly one POST.
func (s *Service) attemptWebhook(webhook WebhookRecord, body []byte, timeout time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook.URL, strings.NewReader(string(body)))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wacli/2")
	if legacy, versioned, stamp := signWebhook(webhook.Secret, body, time.Now()); legacy != "" {
		req.Header.Set("X-WACLI-Signature", legacy)
		req.Header.Set("X-WACLI-Signature-V1", versioned)
		req.Header.Set("X-WACLI-Timestamp", stamp)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, strings.TrimSpace(string(data)), nil
}

// logWebhookFailure records a failed delivery where an operator will find it.
func (s *Service) logWebhookFailure(webhook WebhookRecord, event, detail string, attempts int, exhausted bool) {
	msg := fmt.Sprintf("Webhook %d delivery failed after %d attempt(s)", webhook.ID, attempts)
	if !exhausted {
		msg = fmt.Sprintf("Webhook %d rejected the event", webhook.ID)
	}
	s.log.Warnf("%s: %s", msg, detail)
	_ = s.store.AddAppLog("error", "webhook", msg, map[string]any{
		"webhook_id": webhook.ID,
		"url":        webhook.URL,
		"event":      event,
		"attempts":   attempts,
		"error":      detail,
	})
}

// ReplayDelivery re-sends a recorded delivery, body unchanged.
//
// Useful when a consumer was down, or was fixed after the fact: the event is still on disk, so it
// does not have to be recreated by hand.
func (s *Service) ReplayDelivery(deliveryID int64) (WebhookDeliveryRecord, error) {
	delivery, err := s.store.GetWebhookDelivery(deliveryID)
	if err != nil {
		return WebhookDeliveryRecord{}, err
	}
	if strings.TrimSpace(delivery.RequestBody) == "" {
		return WebhookDeliveryRecord{}, fmt.Errorf("delivery %d has no recorded body to replay", deliveryID)
	}
	webhook, err := s.store.GetWebhook(delivery.WebhookID)
	if err != nil {
		return WebhookDeliveryRecord{}, fmt.Errorf("webhook %d is gone: %w", delivery.WebhookID, err)
	}

	newID, err := s.store.StartWebhookDelivery(webhook.ID, webhook.URL, delivery.Event,
		delivery.ChatJID, delivery.MessageID, delivery.RequestBody)
	if err != nil {
		return WebhookDeliveryRecord{}, err
	}
	// Synchronous: a replay is asked for explicitly, and the caller wants the outcome.
	s.deliverWebhook(webhook, delivery.Event, newID, []byte(delivery.RequestBody))
	return s.store.GetWebhookDelivery(newID)
}

// TestWebhook sends a synthetic event so an endpoint can be verified — reachability, status code and
// signature — without waiting for something real to happen.
func (s *Service) TestWebhook(webhookID int64) (WebhookDeliveryRecord, error) {
	webhook, err := s.store.GetWebhook(webhookID)
	if err != nil {
		return WebhookDeliveryRecord{}, err
	}
	body, err := json.Marshal(map[string]any{
		"event":        "webhook_test",
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"webhook":      map[string]any{"id": webhook.ID, "url": webhook.URL},
		"payload": map[string]any{
			"test": true,
			"note": "Synthetic event from `wacli webhooks test`. Nothing happened on WhatsApp.",
		},
	})
	if err != nil {
		return WebhookDeliveryRecord{}, err
	}
	deliveryID, err := s.store.StartWebhookDelivery(webhook.ID, webhook.URL, "webhook_test", "", "", string(body))
	if err != nil {
		return WebhookDeliveryRecord{}, err
	}
	s.deliverWebhook(webhook, "webhook_test", deliveryID, body)
	return s.store.GetWebhookDelivery(deliveryID)
}
