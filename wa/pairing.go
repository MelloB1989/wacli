package wa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
)

// Linking this device by pairing code rather than QR.
//
// WhatsApp offers two ways to link a companion, and only one of them works when the person doing
// the linking is holding the phone being linked. A QR code has to be photographed by that phone's
// camera, which it cannot do to its own screen — fine at a desk beside a laptop, useless when the
// whole conversation is happening inside WhatsApp on the one device. A pairing code is eight
// characters somebody types, so the screen showing it and the phone accepting it can be the same.
//
// The order here is fixed by whatsmeow and none of it is guessable: the channel must be opened
// BEFORE connecting, PairPhone is only valid once that channel has produced its first event, and
// the same channel then reports whether anybody actually typed the code. Each of those, done in the
// wrong order, fails as something that looks like network trouble.

const (
	// pairClientDisplayName goes to WhatsApp verbatim and is validated against the client type.
	// A wacli-branded string here is rejected with "info query returned status 400: bad request",
	// which reads like a malformed request rather than a refused name. Matched to
	// PairClientChrome below; change one and you must change the other.
	pairClientDisplayName = "Chrome (Linux)"

	// pairCodeWait bounds the wait for WhatsApp to reach the point where a code can be requested.
	// This is a handshake, not a person doing anything, so it is short.
	pairCodeWait = 30 * time.Second

	// pairWindow bounds the whole attempt. WhatsApp expires codes on its own schedule and says so
	// on the channel; this only stops an abandoned attempt holding the slot forever.
	pairWindow = 5 * time.Minute
)

var (
	// ErrAlreadyPaired refuses to link a device that already holds a session. Pairing over a live
	// session discards it, and a session is the one thing here that cannot be rebuilt without the
	// account holder's phone in hand.
	ErrAlreadyPaired = errors.New("wacli: already linked; log out before linking again")

	// ErrPairingInProgress refuses a second concurrent attempt. Two attempts race for one socket.
	ErrPairingInProgress = errors.New("wacli: a pairing attempt is already in progress")

	// ErrPairingTimedOut is what Done carries when nobody typed the code in time.
	ErrPairingTimedOut = errors.New("wacli: pairing timed out; ask for a new code")
)

// PairingSession is one link attempt in progress.
type PairingSession struct {
	// Code is the eight characters to type under Linked Devices → Link with phone number.
	Code string

	// Done reports how the attempt finished: nil on success, an error otherwise. It carries
	// exactly one value and is then closed, so it is safe to select on and safe to ignore.
	Done <-chan error
}

// pairingState is the attempt the service is currently running, guarded by Service.mu.
type pairingState struct {
	code   string
	done   chan error
	cancel context.CancelFunc
}

// IsPaired reports whether this device already holds a WhatsApp session.
func (s *Service) IsPaired() bool {
	return s != nil && s.client != nil && s.client.Store.ID != nil
}

// PairingCode returns the code of the attempt in progress, or "" when there is none.
func (s *Service) PairingCode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pairing == nil {
		return ""
	}
	return s.pairing.code
}

// CancelPairing abandons the attempt in progress. Safe to call when there is none.
func (s *Service) CancelPairing() {
	s.mu.Lock()
	st := s.pairing
	s.mu.Unlock()
	if st != nil && st.cancel != nil {
		st.cancel()
	}
}

// StartPairing links this device to phone and returns the code to type.
//
// It returns as soon as the code exists; the login itself completes in the background and is
// reported on Done. ctx bounds only the wait for that code — cancelling it afterwards does not
// abandon the attempt, because the caller is typically an HTTP request that is about to return
// while the person it is talking to has not started typing yet.
func (s *Service) StartPairing(ctx context.Context, phone string) (*PairingSession, error) {
	number := NormalizePhone(phone)
	if number == "" {
		return nil, errors.New("wacli: a phone number in international format is required")
	}
	if s.IsPaired() {
		return nil, ErrAlreadyPaired
	}

	// Claim the slot before doing anything slow, so two callers cannot both pass the check above
	// and then race for the same socket.
	s.mu.Lock()
	if s.pairing != nil {
		s.mu.Unlock()
		return nil, ErrPairingInProgress
	}
	st := &pairingState{done: make(chan error, 1)}
	s.pairing = st
	s.mu.Unlock()

	release := func() {
		s.mu.Lock()
		if s.pairing == st {
			s.pairing = nil
		}
		s.mu.Unlock()
	}

	// The attempt outlives the call that started it, so it gets its own deadline rather than
	// borrowing a request's.
	attemptCtx, cancel := context.WithTimeout(context.Background(), pairWindow)
	st.cancel = cancel

	abandon := func(err error) (*PairingSession, error) {
		cancel()
		s.client.Disconnect()
		release()
		return nil, err
	}

	// Before Connect, not after. whatsmeow wires this channel into the socket as it opens; asked
	// for afterwards it hands back a channel that never produces anything and no error to say so.
	events, err := s.client.GetQRChannel(attemptCtx)
	if err != nil {
		cancel()
		release()
		return nil, fmt.Errorf("wacli: open pairing channel: %w", err)
	}
	if err := s.client.Connect(); err != nil {
		cancel()
		release()
		return nil, fmt.Errorf("wacli: connect: %w", err)
	}

	// PairPhone is only valid once the channel has produced its first event. Called before that it
	// races the handshake and fails as though the number were bad.
	select {
	case evt, ok := <-events:
		if !ok {
			return abandon(errors.New("wacli: pairing channel closed before a code could be requested"))
		}
		if evt.Event != whatsmeow.QRChannelEventCode {
			return abandon(fmt.Errorf("wacli: unexpected pairing event %q", evt.Event))
		}
	case <-ctx.Done():
		return abandon(ctx.Err())
	case <-time.After(pairCodeWait):
		return abandon(errors.New("wacli: timed out waiting for WhatsApp to accept a pairing request"))
	}

	code, err := s.client.PairPhone(attemptCtx, number, false, whatsmeow.PairClientChrome, pairClientDisplayName)
	if err != nil {
		return abandon(fmt.Errorf("wacli: request pairing code: %w", err))
	}

	s.mu.Lock()
	st.code = code
	s.mu.Unlock()

	go func() {
		defer cancel()
		defer release()
		err := s.awaitPairing(attemptCtx, events)
		if err != nil {
			// Nothing was linked, so leave no half-open socket behind for the next attempt to
			// collide with.
			s.client.Disconnect()
		}
		st.done <- err
		close(st.done)
	}()

	return &PairingSession{Code: code, Done: st.done}, nil
}

// awaitPairing drains the channel until the login resolves one way or the other.
func (s *Service) awaitPairing(ctx context.Context, events <-chan whatsmeow.QRChannelItem) error {
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				return errors.New("wacli: pairing channel closed before the login completed")
			}
			switch evt.Event {
			case whatsmeow.QRChannelEventCode:
				// A QR refresh. Irrelevant to a code pairing — the typed code stays valid, and
				// treating this as progress is how the CLI used to request a second code.
				continue
			case "success":
				return nil
			case "timeout":
				return ErrPairingTimedOut
			default:
				if evt.Error != nil {
					return fmt.Errorf("wacli: pairing failed: %w", evt.Error)
				}
				return fmt.Errorf("wacli: pairing ended with %q", evt.Event)
			}
		case <-ctx.Done():
			return ErrPairingTimedOut
		}
	}
}
