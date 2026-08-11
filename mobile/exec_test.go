package mobile

import (
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"blanks only", "   \t ", nil},
		{"simple", "chats --limit 5", []string{"chats", "--limit", "5"}},
		{"collapses runs of space", "chats    --limit\t5", []string{"chats", "--limit", "5"}},
		{"double quotes hold a space", `send --to "Jio Phone"`, []string{"send", "--to", "Jio Phone"}},
		{"single quotes hold a space", `send --text 'hello there'`, []string{"send", "--text", "hello there"}},
		{"quotes inside a word", `--text="hello there"`, []string{"--text=hello there"}},
		{"empty string argument", `send --text ""`, []string{"send", "--text", ""}},
		{"escaped space", `send --text hello\ there`, []string{"send", "--text", "hello there"}},
		{"escaped quote", `send --text "say \"hi\""`, []string{"send", "--text", `say "hi"`}},
		{"backslash is literal in single quotes", `api GET '/a\b'`, []string{"api", "GET", `/a\b`}},
		// Bare double quotes are shell quoting, here as anywhere else, so an unquoted JSON body
		// loses them and arrives malformed. Matching the shell is the point — the console runs the
		// documented commands with the documented syntax — so this is pinned rather than special-cased.
		{"bare json loses its quotes, as in any shell", `api POST /dnd {"enabled":true}`, []string{"api", "POST", "/dnd", `{enabled:true}`}},
		{"single-quoted json body survives intact", `api POST /dnd '{"enabled":true}'`, []string{"api", "POST", "/dnd", `{"enabled":true}`}},
		{"escaped-quote json body survives intact", `api POST /dnd {\"enabled\":true}`, []string{"api", "POST", "/dnd", `{"enabled":true}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenize(tc.in)
			if err != nil {
				t.Fatalf("tokenize(%q): unexpected error %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("tokenize(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("tokenize(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestTokenizeRejectsUnbalanced(t *testing.T) {
	for _, in := range []string{`send --text "unclosed`, `send --text 'unclosed`, `trailing\`} {
		if _, err := tokenize(in); err == nil {
			t.Errorf("tokenize(%q): expected an error, got none", in)
		}
	}
}

// Exec must refuse cleanly rather than panic when nothing is running, since that is the state an
// app is in before Start and the console will be typed into regardless.
func TestExecWithoutServiceReportsNotRunning(t *testing.T) {
	out, err := Exec("status")
	if err == nil {
		t.Fatalf("expected an error with no service running, got output %q", out)
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %v, want it to mention not running", err)
	}
}

func TestExecEmptyLineIsANoop(t *testing.T) {
	out, err := Exec("   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
}

func TestExecCommandsListsRealCommands(t *testing.T) {
	list := ExecCommands()
	for _, want := range []string{"chats", "send", "api", "triggers"} {
		if !strings.Contains(list, want) {
			t.Errorf("ExecCommands() is missing %q; got %q", want, list)
		}
	}
}
