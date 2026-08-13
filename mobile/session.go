package mobile

import (
	"context"
	"errors"
	"time"

	"github.com/MelloB1989/wacli/wa"
)

// Handing a session to another machine.
//
// A WhatsApp linked device is not a bearer token. whatsmeow keeps Signal double-ratchet state per
// contact, prekeys and app-state sync versions, and two copies that both connect advance that state
// independently until they diverge — after which messages stop decrypting or the link is dropped
// outright, and neither is recoverable without pairing again. Two devices presenting one identity
// from two places is also the pattern account bans are made of.
//
// So a handover is not a copy: it is a move. Stop, export, hand over, and do not reconnect until
// the other side hands it back. The host is expected to hold a lease around all of this; these
// functions deliberately refuse to run while the daemon is connected, because that is the one
// mistake with no cheap recovery.

// ExportSession stops the daemon and returns the session as a portable blob.
//
// It carries whatsmeow's tables only — the device identity, the ratchet, the keys. Message history
// stays on this device: nothing on the other side needs it, it is the bulk of the store, and it is
// the most sensitive thing that could be put on a network unnecessarily.
//
// Stopping first is not politeness. Exporting a database that is being written produces a file
// whose ratchet state is a guess, and a guess is indistinguishable from a working session until the
// first message fails to decrypt.
func ExportSession() ([]byte, error) {
	mu.Lock()
	defer mu.Unlock()

	if service != nil {
		if err := stopLocked(); err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return wa.ExportSession(ctx, wa.SessionDBPath)
}

// ImportSession replaces this device's session with one handed back from elsewhere.
//
// Replacing rather than merging is the only safe choice: two versions of a ratchet cannot be
// reconciled, and the incoming copy is by definition the one that has been advancing.
func ImportSession(blob []byte) error {
	mu.Lock()
	defer mu.Unlock()

	if service != nil {
		// Importing under a live connection would swap the store beneath it mid-write.
		return errors.New("wacli: stop the service before importing a session")
	}
	if len(blob) == 0 {
		return errors.New("wacli: session blob is empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return wa.ImportSession(ctx, blob, wa.SessionDBPath)
}

// HasSession reports whether this device currently holds a paired session.
//
// Distinct from IsPaired only in that it answers from the store on disk without opening the
// service, so a host can ask after handing the session away.
func HasSession() bool {
	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return wa.SessionSanityCheck(ctx, wa.SessionDBPath) == nil
}

// ReleaseSession stops the daemon and erases the local session after it has been handed over.
//
// The host calls this once the server has acknowledged the upload. Leaving the copy behind is what
// makes a later accidental reconnect possible, and an accidental reconnect is the divergence this
// whole dance exists to prevent.
func ReleaseSession() error {
	mu.Lock()
	defer mu.Unlock()

	if service != nil {
		if err := stopLocked(); err != nil {
			return err
		}
	}
	return wa.RemoveSessionStore(wa.SessionDBPath)
}
