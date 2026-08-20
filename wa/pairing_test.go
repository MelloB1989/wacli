package wa

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

func TestStartPairingNeedsANumber(t *testing.T) {
	s := &Service{}
	if _, err := s.StartPairing(context.Background(), "   "); err == nil {
		t.Fatal("pairing with no number should fail")
	}
	// The slot is claimed only after the number is accepted; a rejected call that left it held
	// would lock out every later attempt for the life of the process.
	if got := s.PairingCode(); got != "" {
		t.Fatalf("a rejected attempt left the slot held: %q", got)
	}
}

func TestAwaitPairingSuccess(t *testing.T) {
	events := make(chan whatsmeow.QRChannelItem, 1)
	events <- whatsmeow.QRChannelItem{Event: "success"}

	if err := (&Service{}).awaitPairing(context.Background(), events); err != nil {
		t.Fatalf("success reported as %v", err)
	}
}

func TestAwaitPairingIgnoresQRRefresh(t *testing.T) {
	// The channel keeps emitting fresh QR codes throughout a code pairing. Treating one as an
	// outcome ends the attempt while the person is still typing; treating it as a reason to
	// request another code is how you get two live codes and a confused user.
	events := make(chan whatsmeow.QRChannelItem, 3)
	events <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "one"}
	events <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "two"}
	events <- whatsmeow.QRChannelItem{Event: "success"}

	if err := (&Service{}).awaitPairing(context.Background(), events); err != nil {
		t.Fatalf("refreshes should not end the attempt, got %v", err)
	}
}

func TestAwaitPairingTimeout(t *testing.T) {
	events := make(chan whatsmeow.QRChannelItem, 1)
	events <- whatsmeow.QRChannelItem{Event: "timeout"}

	err := (&Service{}).awaitPairing(context.Background(), events)
	if !errors.Is(err, ErrPairingTimedOut) {
		t.Fatalf("want ErrPairingTimedOut, got %v", err)
	}
}

func TestAwaitPairingDeadlineIsATimeout(t *testing.T) {
	// A caller that gave up looks the same to the person holding the phone as a code that
	// expired, and the useful thing to tell them is identical: ask for another one.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := (&Service{}).awaitPairing(ctx, make(chan whatsmeow.QRChannelItem))
	if !errors.Is(err, ErrPairingTimedOut) {
		t.Fatalf("want ErrPairingTimedOut, got %v", err)
	}
}

func TestAwaitPairingSurfacesTheError(t *testing.T) {
	boom := errors.New("multidevice not enabled")
	events := make(chan whatsmeow.QRChannelItem, 1)
	events <- whatsmeow.QRChannelItem{Event: "err-client-outdated", Error: boom}

	err := (&Service{}).awaitPairing(context.Background(), events)
	if !errors.Is(err, boom) {
		t.Fatalf("want the underlying error wrapped, got %v", err)
	}
}

func TestAwaitPairingClosedChannel(t *testing.T) {
	events := make(chan whatsmeow.QRChannelItem)
	close(events)

	err := (&Service{}).awaitPairing(context.Background(), events)
	if err == nil {
		t.Fatal("a closed channel is not a successful login")
	}
	if errors.Is(err, ErrPairingTimedOut) {
		t.Fatal("a closed channel is a fault, not an expiry; the two need different advice")
	}
}

func TestAwaitPairingUnknownEventEnds(t *testing.T) {
	// Anything unrecognised must terminate rather than spin: this loop has no other exit, and a
	// silent spin here burns a Lambda's whole budget looking healthy.
	events := make(chan whatsmeow.QRChannelItem, 1)
	events <- whatsmeow.QRChannelItem{Event: "something-new"}

	done := make(chan error, 1)
	go func() { done <- (&Service{}).awaitPairing(context.Background(), events) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an unknown event is not success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("awaitPairing spun on an unknown event instead of returning")
	}
}

func TestPairClientDisplayNameMatchesClientType(t *testing.T) {
	// WhatsApp validates the display name against the client type and rejects a mismatch with
	// "info query returned status 400: bad request" — which reads as a malformed request, not a
	// refused name, and cost a day the last time it was hit. If PairClientChrome changes here,
	// this string has to change with it.
	if pairClientDisplayName != "Chrome (Linux)" {
		t.Fatalf("display name %q no longer matches PairClientChrome", pairClientDisplayName)
	}
}
