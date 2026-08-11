package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stubTransport records what a command asked for and replies with canned JSON, standing in for
// both a daemon socket and the in-process handler.
type stubTransport struct {
	calls     []string
	responses map[string]string
	fail      error
}

func (s *stubTransport) transport(method, path string, body any, out any) error {
	s.calls = append(s.calls, method+" "+path)
	if s.fail != nil {
		return s.fail
	}
	if out == nil {
		return nil
	}
	payload, ok := s.responses[method+" "+path]
	if !ok {
		payload = "{}"
	}
	return json.Unmarshal([]byte(payload), out)
}

func newTestEnv(responses map[string]string) (*Env, *bytes.Buffer, *stubTransport) {
	var buf bytes.Buffer
	stub := &stubTransport{responses: responses}
	// No OpenStore: this is the mobile shape, where every command must take its API path.
	return &Env{Out: &buf, Transport: stub.transport}, &buf, stub
}

func TestRunDispatchesToTheAPI(t *testing.T) {
	// The filter default travels with the request, so the expected path carries it.
	const want = "GET /chats?filter=all&limit=5"
	env, out, stub := newTestEnv(map[string]string{want: `{"chats":[]}`})
	if err := env.Run([]string{"chats", "--limit", "5"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stub.calls) != 1 || stub.calls[0] != want {
		t.Fatalf("calls = %v, want one %s", stub.calls, want)
	}
	if !strings.Contains(out.String(), "No chats found") {
		t.Errorf("output = %q, want it to report an empty list", out.String())
	}
}

// The whole point of the refactor: a command that would have called os.Exit must come back as an
// error instead, leaving the caller alive to print it.
func TestDieBecomesAnErrorRatherThanExiting(t *testing.T) {
	env, _, stub := newTestEnv(nil)
	err := env.Run([]string{"send"}) // no --to, no --text
	if err == nil {
		t.Fatal("expected an error from a command with missing required flags")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("error = %v, want the usage message", err)
	}
	if len(stub.calls) != 0 {
		t.Errorf("calls = %v, want none — it should fail before reaching the API", stub.calls)
	}
}

// flag.ExitOnError used to call os.Exit(2) from inside the flag package, where die could not
// intercept it. ContinueOnError plus mustParse is what closes that hole.
func TestUnparseableFlagDoesNotExit(t *testing.T) {
	env, _, _ := newTestEnv(nil)
	if err := env.Run([]string{"chats", "--definitely-not-a-flag"}); err == nil {
		t.Fatal("expected an error from an unknown flag")
	}
}

func TestAPIErrorSurfacesToTheCaller(t *testing.T) {
	env, _, stub := newTestEnv(nil)
	stub.fail = errors.New("chat is locked")
	err := env.Run([]string{"send", "--to", "someone", "--text", "hi"})
	if err == nil {
		t.Fatal("expected the transport error to surface")
	}
	if !strings.Contains(err.Error(), "chat is locked") {
		t.Errorf("error = %v, want wacli's own message preserved", err)
	}
}

func TestUnknownCommandIsDistinguishable(t *testing.T) {
	env, _, _ := newTestEnv(nil)
	err := env.Run([]string{"frobnicate"})
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("error = %v, want it to wrap ErrUnknownCommand", err)
	}
}

func TestNoCommand(t *testing.T) {
	env, _, _ := newTestEnv(nil)
	if err := env.Run(nil); err == nil {
		t.Fatal("expected an error for an empty argument list")
	}
}

// A command needing the database must say so rather than panic when the host has none.
func TestStoreOnlyPathReportsCleanlyWithoutAStore(t *testing.T) {
	env, _, stub := newTestEnv(nil)
	stub.fail = errors.New("connection refused")
	err := env.Run([]string{"status"})
	if err == nil {
		t.Fatal("expected an error when the API is unreachable and there is no store")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %v, want the underlying API failure reported", err)
	}
}

func TestCommandsListIsDispatchable(t *testing.T) {
	env, _, _ := newTestEnv(nil)
	for _, name := range Commands {
		if err := env.Run([]string{name}); errors.Is(err, ErrUnknownCommand) {
			t.Errorf("Commands lists %q but Run does not dispatch it", name)
		}
	}
}
