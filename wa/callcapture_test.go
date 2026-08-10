package wa

import (
	"path/filepath"
	"strings"
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// sampleOffer mirrors the shape of a real incoming call offer closely enough to exercise every
// branch of the capture path: nested children, JID attrs, int attrs and binary content.
func sampleOffer() waBinary.Node {
	from, _ := types.ParseJID("15551234567@s.whatsapp.net")
	return waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": from, "id": "3EB0ABC", "t": 1700000000},
		Content: []waBinary.Node{{
			Tag:   "offer",
			Attrs: waBinary.Attrs{"call-id": "DEADBEEF", "call-creator": from},
			Content: []waBinary.Node{
				{Tag: "audio", Attrs: waBinary.Attrs{"enc": "opus", "rate": 16000}},
				{Tag: "net", Attrs: waBinary.Attrs{"medium": 3}},
				{Tag: "capability", Attrs: waBinary.Attrs{"ver": "1"}, Content: []byte{1, 4, 255, 131, 207, 4}},
			},
		}},
	}
}

func TestCaptureRecordsCallStanzas(t *testing.T) {
	path := filepath.Join(t.TempDir(), captureFileName)
	capture := newCallCapture(path)
	if err := capture.Enable(); err != nil {
		t.Fatalf("enable capture: %v", err)
	}
	defer capture.Close()

	log := newCaptureLogger(waLog.Noop, capture)
	recv := log.Sub("Recv")
	send := log.Sub("Send")

	offer := sampleOffer()
	recv.Debugf("%s", &offer)
	send.Debugf("%s", &waBinary.Node{Tag: "call", Attrs: waBinary.Attrs{"id": "out-1"}})

	// Traffic that isn't a call must never be recorded.
	recv.Debugf("%s", &waBinary.Node{Tag: "message", Attrs: waBinary.Attrs{"id": "secret"}})
	log.Sub("Other").Debugf("%s", &offer)

	records, err := LoadCaptures(path)
	if err != nil {
		t.Fatalf("load captures: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 captured stanzas, got %d", len(records))
	}
	if records[0].Direction != "recv" || records[1].Direction != "send" {
		t.Errorf("directions wrong: %q, %q", records[0].Direction, records[1].Direction)
	}
	for _, rec := range records {
		if strings.Contains(string(rec.Node), "secret") {
			t.Error("non-call traffic leaked into the capture file")
		}
	}
}

// The capture format must round-trip through whatsmeow's own node JSON decoding, otherwise offline
// replay and re-analysis of captures is impossible.
func TestCaptureRoundTripsThroughNodeJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), captureFileName)
	capture := newCallCapture(path)
	if err := capture.Enable(); err != nil {
		t.Fatalf("enable capture: %v", err)
	}
	offer := sampleOffer()
	capture.record("recv", &offer)
	capture.Close()

	records, err := LoadCaptures(path)
	if err != nil {
		t.Fatalf("load captures: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	out := DescribeCapture(records[0])
	for _, want := range []string{
		"<call",
		"<offer",
		"call-id=DEADBEEF",
		`enc=opus`,
		"rate=16000",
		"medium=3",
		// Binary content must survive as bytes, not as a mangled printable string.
		"01 04 ff 83 cf 04",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump missing %q\n--- dump ---\n%s", want, out)
		}
	}
}

func TestCaptureDisabledWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), captureFileName)
	capture := newCallCapture(path)
	offer := sampleOffer()
	capture.record("recv", &offer)
	if _, err := LoadCaptures(path); err == nil {
		t.Error("capture file was created while capture was disabled")
	}
}
