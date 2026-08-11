// Package cli implements wacli's client commands independently of the process they run in.
//
// These commands were written against os.Stdout, os.Exit and a socket to the daemon. That is
// exactly right for a terminal and exactly wrong inside an app, where stdout is a log sink nobody
// reads and os.Exit takes the whole process down on a typo. Env parameterises the three things
// that actually differ between those worlds — where output goes, how a request reaches the
// service, and whether a local store can be opened — so one set of command bodies serves both the
// binary and the mobile bindings, and neither can drift from the other.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MelloB1989/wacli/wa"
)

// Transport dispatches one API call and decodes the response into out, which may be nil when the
// caller does not care about the body. Implementations differ by host: the binary opens a socket
// to the daemon, the mobile bindings hand the request straight to the in-process handler.
type Transport func(method, path string, body any, out any) error

// StoreOpener opens the local database. A few commands read it directly as a fallback for when no
// daemon is running. Hosts that have no such fallback — mobile, where the service is always
// in-process — leave this nil and the commands report that rather than reaching for a store that
// cannot exist.
type StoreOpener func() (*wa.Store, error)

// Env carries everything a command needs from its host.
type Env struct {
	// Out receives command output. Never os.Stdout directly, so a host can capture it.
	Out io.Writer
	// In supplies stdin for the handful of commands that accept piped JSON. May be nil.
	In io.Reader
	// Transport is required.
	Transport Transport
	// OpenStore is optional; see StoreOpener.
	OpenStore StoreOpener
}

// exitErr is how die unwinds. The commands were written to call die and not return, across 125
// call sites; rather than thread an error return through every one of them, die panics with this
// and Run recovers it. The control flow the command bodies were written against is preserved
// exactly, and the process survives.
type exitErr struct{ msg string }

func (e exitErr) Error() string { return e.msg }

// die reports a fatal command error and does not return.
func (e *Env) die(format string, args ...any) {
	panic(exitErr{msg: fmt.Sprintf(format, args...)})
}

func (e *Env) printf(format string, args ...any) {
	fmt.Fprintf(e.Out, format, args...)
}

func (e *Env) println(args ...any) {
	fmt.Fprintln(e.Out, args...)
}

func (e *Env) printJSON(value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(e.Out, "%v\n", value)
		return
	}
	fmt.Fprintln(e.Out, string(data))
}

func (e *Env) callAPI(method, path string, body any, out any) error {
	if e.Transport == nil {
		return errors.New("no transport configured")
	}
	return e.Transport(method, path, body, out)
}

func (e *Env) callAPIQuery(method, path string, query url.Values, body any, out any) error {
	if len(query) > 0 {
		path = path + "?" + query.Encode()
	}
	return e.callAPI(method, path, body, out)
}

// store opens the local database or dies explaining why it cannot.
func (e *Env) store() *wa.Store {
	if e.OpenStore == nil {
		e.die("this command needs direct access to the local database, which is not available here")
	}
	store, err := e.OpenStore()
	if err != nil {
		e.die("open store: %v", err)
	}
	return store
}

// hasStore reports whether a direct-store fallback is available, letting a command prefer the API
// and degrade gracefully instead of dying.
func (e *Env) hasStore() bool { return e.OpenStore != nil }

func (e *Env) stdin() io.Reader {
	if e.In == nil {
		return strings.NewReader("")
	}
	return e.In
}

// newFlagSet returns a flag set that reports errors instead of exiting. The original code used
// flag.ExitOnError, which calls os.Exit(2) from inside the flag package where die cannot intercept
// it — fine for a shell, fatal for an app.
func (e *Env) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.Out)
	return fs
}

// mustParse parses flags, turning a parse failure into the same fatal path as any other bad input.
func (e *Env) mustParse(fs *flag.FlagSet, args []string) {
	if err := fs.Parse(args); err != nil {
		e.die("%s: %v", fs.Name(), err)
	}
}

// NewSocketTransport returns a Transport that talks to a running daemon over the local API socket.
func NewSocketTransport(timeout time.Duration) Transport {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return func(method, path string, body any, out any) error {
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			payload, err := json.Marshal(body)
			if err != nil {
				return err
			}
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequest(method, "http://"+wa.HTTPAddr+path, reader)
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		client := &http.Client{Timeout: timeout}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			var payload map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil {
				if msg, ok := payload["error"].(string); ok {
					return errors.New(msg)
				}
			}
			return fmt.Errorf("request failed with status %d", resp.StatusCode)
		}
		if out == nil {
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

// Commands are the client command names, in the order the help text lists them. Hosts use this for
// completion and for deciding whether a word is a command at all.
var Commands = []string{
	"status", "sync", "dnd", "chats", "resolve", "messages", "send", "edit", "delete",
	"receipts", "bulk-send", "story", "call", "media", "contacts", "groups", "check",
	"triggers", "auto-replies", "webhooks", "api",
}

// Run executes one client command. It returns an error rather than exiting, so an embedding host
// stays alive; the binary turns that error back into a stderr line and a non-zero exit.
//
// Unknown commands are reported rather than silently ignored — a console needs to say so.
func (e *Env) Run(args []string) (err error) {
	if len(args) == 0 {
		return errors.New("no command given")
	}
	defer func() {
		if r := recover(); r != nil {
			if exit, ok := r.(exitErr); ok {
				err = exit
				return
			}
			panic(r)
		}
	}()

	name, rest := args[0], args[1:]
	switch name {
	case "status":
		e.cmdStatus()
	case "sync":
		e.cmdSync()
	case "dnd":
		e.cmdDND(rest)
	case "chats":
		e.cmdChats(rest)
	case "resolve":
		e.cmdResolve(rest)
	case "messages":
		e.cmdMessages(rest)
	case "send":
		e.cmdSend(rest)
	case "edit":
		e.cmdEdit(rest)
	case "delete":
		e.cmdDelete(rest)
	case "receipts":
		e.cmdReceipts(rest)
	case "bulk-send":
		e.cmdBulkSend(rest)
	case "story":
		e.cmdStory(rest)
	case "call":
		e.cmdCall(rest)
	case "media":
		e.cmdMedia(rest)
	case "contacts":
		e.cmdContacts(rest)
	case "groups", "group":
		e.cmdGroups(rest)
	case "check":
		e.cmdCheckNumbers(rest)
	case "triggers", "trigger":
		e.cmdTriggers(rest)
	case "auto-replies", "autoreplies":
		e.cmdAutoReplies(rest)
	case "webhooks":
		e.cmdWebhooks(rest)
	case "api":
		e.cmdAPI(rest)
	default:
		return fmt.Errorf("%w: %q", ErrUnknownCommand, name)
	}
	return nil
}

// ErrUnknownCommand reports a name that is not a client command, so a host can decide whether to
// print its own usage text or simply say so.
var ErrUnknownCommand = errors.New("unknown command")
