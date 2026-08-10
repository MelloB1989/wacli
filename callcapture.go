package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	waBinary "go.mau.fi/whatsmeow/binary"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Call stanza capture.
//
// whatsmeow hands every stanza it reads or writes to a logger before doing anything else with it
// (client.go: recvLog.Debugf("%s", node) / sendLog.Debugf("%s", &node)). Those calls happen
// regardless of log level — the logger itself decides whether to print. So wrapping the logger and
// watching for <call> stanzas gives a complete, unmodified view of call signaling in both
// directions without patching whatsmeow at all.
//
// This is deliberately narrow: only call signaling is recorded. Message traffic never touches the
// capture file.
//
// "Call signaling" means more than <call>. A connected call reaches relays that the <call> offer
// never names — fhyd11c02 appears only in <relaylatency>, and the real client sends wa-0x800 to it
// with a 193-byte token that no <call> stanza carries. That token has to arrive somewhere, and the
// only traffic we were not recording was everything outside <call>. <iq> stanzas mentioning relay
// or voip are therefore captured too, so the token's source can be found rather than guessed at.
//
// The capture file is JSON Lines. Each record holds the raw node as whatsmeow's own JSON encoding,
// which round-trips back through waBinary.Node.UnmarshalJSON — so captures can be replayed and
// re-analyzed offline.

const captureFileName = "call-capture.jsonl"

// CaptureRecord is one captured call stanza.
type CaptureRecord struct {
	At        time.Time       `json:"at"`
	Direction string          `json:"direction"` // "recv" or "send"
	Node      json.RawMessage `json:"node"`
	XML       string          `json:"xml"`
}

// callCapture appends call stanzas to a file. The zero value is disabled.
type callCapture struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	enabled bool
}

func newCallCapture(path string) *callCapture {
	return &callCapture{path: path}
}

// Enable opens the capture file for appending. Captures accumulate across daemon restarts.
func (c *callCapture) Enable() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enabled {
		return nil
	}
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open capture file: %w", err)
	}
	c.file = f
	c.enabled = true
	return nil
}

func (c *callCapture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = false
	if c.file == nil {
		return nil
	}
	// record() does not sync per stanza, so force the capture to disk here — this is where a capture
	// is finished with and about to be read.
	_ = c.file.Sync()
	err := c.file.Close()
	c.file = nil
	return err
}

// record writes one stanza. Failures are swallowed: a broken capture must never take down call
// handling or the socket read loop.
func (c *callCapture) record(direction string, node *waBinary.Node) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled || c.file == nil {
		return
	}
	raw, err := json.Marshal(node)
	if err != nil {
		return
	}
	line, err := json.Marshal(CaptureRecord{
		At:        time.Now().UTC(),
		Direction: direction,
		Node:      raw,
		XML:       node.String(),
	})
	if err != nil {
		return
	}
	// Deliberately not synced per stanza. This runs inside whatsmeow's logging path, which is the
	// socket receive loop — an fsync per stanza puts disk latency directly in front of reading the
	// next frame, and a capture is on precisely when traffic is heaviest. The write itself still
	// reaches the page cache immediately, so `wacli call dump` sees every record; only a machine
	// crash could lose the tail. Close flushes to disk.
	_, _ = c.file.Write(append(line, '\n'))
}

// captureLogger wraps a waLog.Logger and taps the Recv/Send submodules for call stanzas. Every
// other logging call passes straight through.
type captureLogger struct {
	inner   waLog.Logger
	capture *callCapture
	// direction is "recv"/"send" once this logger is the Recv/Send submodule, empty otherwise.
	direction string
}

func newCaptureLogger(inner waLog.Logger, capture *callCapture) waLog.Logger {
	return &captureLogger{inner: inner, capture: capture}
}

func (l *captureLogger) Sub(module string) waLog.Logger {
	direction := l.direction
	switch module {
	case "Recv":
		direction = "recv"
	case "Send":
		direction = "send"
	}
	return &captureLogger{inner: l.inner.Sub(module), capture: l.capture, direction: direction}
}

func (l *captureLogger) Warnf(msg string, args ...any)  { l.inner.Warnf(msg, args...) }
func (l *captureLogger) Errorf(msg string, args ...any) { l.inner.Errorf(msg, args...) }
func (l *captureLogger) Infof(msg string, args ...any)  { l.inner.Infof(msg, args...) }

// worthCapturing reports whether a stanza belongs in the call capture.
//
// <call> is the obvious case. <iq> is included when it mentions relay/voip machinery, because the
// relay token family the real client uses for its preferred relay appears in no <call> stanza at
// all and has to be delivered somewhere.
// captureEverything records every stanza regardless of tag (WACLI_CAPTURE=2).
//
// Needed to tell "no relay-token exchange happens" apart from "our filter never matches it" — an
// empty result from a narrow filter proves nothing about traffic it would have skipped anyway.
var captureEverything = os.Getenv("WACLI_CAPTURE") == "2"

func worthCapturing(node *waBinary.Node) bool {
	if captureEverything {
		return true
	}
	if node.Tag == "call" {
		return true
	}
	// An outbound call's own relay allocation arrives in <ack class="call" type="offer">, and
	// whatsmeow has no ack handler at all — client.go drops the node without even a debug line about
	// it. It is still logged on the way in (recvLog, before the drop), which is the only reason this
	// hook can see it. Without capturing it a call we place can never bring media up, because nothing
	// else ever tells us which relay to allocate on.
	if node.Tag != "iq" && node.Tag != "ack" {
		return false
	}
	return mentionsRelay(node, 0)
}

func mentionsRelay(node *waBinary.Node, depth int) bool {
	if depth > 6 {
		return false
	}
	switch node.Tag {
	case "relay", "token", "auth_token", "voip_settings", "te", "te2", "relaylatency":
		return true
	}
	for _, attr := range node.Attrs {
		if s, ok := attr.(string); ok {
			l := strings.ToLower(s)
			if strings.Contains(l, "voip") || strings.Contains(l, "relay") {
				return true
			}
		}
	}
	for _, child := range node.GetChildren() {
		if mentionsRelay(&child, depth+1) {
			return true
		}
	}
	return false
}

func (l *captureLogger) Debugf(msg string, args ...any) {
	if l.direction != "" && len(args) == 1 {
		if node := asNode(args[0]); node != nil && worthCapturing(node) {
			l.capture.record(l.direction, node)
		}
	}
	l.inner.Debugf(msg, args...)
}

func asNode(v any) *waBinary.Node {
	switch typed := v.(type) {
	case *waBinary.Node:
		return typed
	case waBinary.Node:
		return &typed
	default:
		return nil
	}
}

// --- offline analysis ---

// LoadCaptures reads the capture file. Records that fail to parse are skipped.
func LoadCaptures(path string) ([]CaptureRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []CaptureRecord
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec CaptureRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// decodeCapturedNode turns a recorded stanza back into a node.
//
// It strips a null Content before handing the JSON to whatsmeow. waBinary.Node marshals a
// content-less node as `"Content":null`, but its UnmarshalJSON only treats *absent* content as
// empty — a literal null is non-empty and matches neither of the forms it accepts, so it fails with
// "node content must be an array of nodes or a base64 string". Plenty of real stanzas are
// content-less (an <ack/>, a bare <offer/>), so without this a capture cannot be read back.
func decodeCapturedNode(raw json.RawMessage) (waBinary.Node, error) {
	var node waBinary.Node
	if err := json.Unmarshal(raw, &node); err == nil {
		return node, nil
	}

	// Content-less nodes appear at any depth — a <call> is fine while the <audio/> leaf three levels
	// down is not — so the whole tree has to be scrubbed, not just the root.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep attribute numbers as written rather than round-tripping through float64
	var loose any
	if err := dec.Decode(&loose); err != nil {
		return node, err
	}
	cleaned, err := json.Marshal(stripNullContent(loose))
	if err != nil {
		return node, err
	}
	err = json.Unmarshal(cleaned, &node)
	return node, err
}

// stripNullContent removes every null Content field in a decoded node tree.
func stripNullContent(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "Content" && val == nil {
				delete(t, k)
				continue
			}
			t[k] = stripNullContent(val)
		}
	case []any:
		for i := range t {
			t[i] = stripNullContent(t[i])
		}
	}
	return v
}

// DescribeCapture renders one captured stanza as an indented tree, with binary content shown as hex
// plus an ASCII gloss. This is the view you want when working out what an unknown node means: it
// makes no assumptions about the protocol and hides nothing.
func DescribeCapture(rec CaptureRecord) string {
	node, err := decodeCapturedNode(rec.Node)
	if err != nil {
		return fmt.Sprintf("%s  %s  <unparseable: %v>", rec.At.Format(time.RFC3339), rec.Direction, err)
	}
	var sb strings.Builder
	arrow := "<--"
	if rec.Direction == "send" {
		arrow = "-->"
	}
	fmt.Fprintf(&sb, "%s %s\n", rec.At.Local().Format("15:04:05.000"), arrow)
	describeNode(&sb, &node, 1)
	return sb.String()
}

func describeNode(sb *strings.Builder, node *waBinary.Node, depth int) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(sb, "%s<%s", indent, node.Tag)
	keys := make([]string, 0, len(node.Attrs))
	for k := range node.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(sb, " %s=%v", k, node.Attrs[k])
	}
	switch content := node.Content.(type) {
	case nil:
		fmt.Fprintf(sb, "/>\n")
	case []waBinary.Node:
		fmt.Fprintf(sb, ">\n")
		for i := range content {
			describeNode(sb, &content[i], depth+1)
		}
		fmt.Fprintf(sb, "%s</%s>\n", indent, node.Tag)
	case []byte:
		fmt.Fprintf(sb, "> %d bytes\n", len(content))
		for _, line := range hexDumpLines(content, indent+"  ") {
			fmt.Fprintln(sb, line)
		}
		fmt.Fprintf(sb, "%s</%s>\n", indent, node.Tag)
	default:
		fmt.Fprintf(sb, ">%v</%s>\n", content, node.Tag)
	}
}

// hexDumpLines renders bytes 16 per line as hex plus printable ASCII.
func hexDumpLines(data []byte, indent string) []string {
	var lines []string
	for off := 0; off < len(data); off += 16 {
		end := min(off+16, len(data))
		chunk := data[off:end]
		hexPart := hex.EncodeToString(chunk)
		spaced := make([]string, 0, len(chunk))
		for i := 0; i < len(hexPart); i += 2 {
			spaced = append(spaced, hexPart[i:i+2])
		}
		var ascii strings.Builder
		for _, b := range chunk {
			if b >= 0x20 && b < 0x7f {
				ascii.WriteByte(b)
			} else {
				ascii.WriteByte('.')
			}
		}
		lines = append(lines, fmt.Sprintf("%s%04x  %-47s  |%s|", indent, off, strings.Join(spaced, " "), ascii.String()))
	}
	return lines
}
