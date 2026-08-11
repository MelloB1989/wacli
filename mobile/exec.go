package mobile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MelloB1989/wacli/cli"
)

// Exec runs one wacli client command and returns everything it printed.
//
// This is the same command layer the binary runs, so an in-app console gets the real commands with
// the real output and nothing has to be reimplemented or kept in step. `chats --limit 5`,
// `triggers list`, `api GET /calls/status` — all of them, for free.
//
// Command failures are written into the returned transcript rather than returned as an error, the
// way a shell prints to stderr and carries on: a console wants to display them, and a binding that
// threw would lose the output that came before the failure. The error return is reserved for the
// line never running at all — an unparseable line, or no service to run it against.
func Exec(line string) (string, error) {
	args, err := tokenize(line)
	if err != nil {
		return "", fmt.Errorf("wacli: %w", err)
	}
	if len(args) == 0 {
		return "", nil
	}

	mu.Lock()
	running := service != nil
	mu.Unlock()
	if !running {
		return "", fmt.Errorf("wacli: not running; call Start first")
	}

	var buf bytes.Buffer
	env := &cli.Env{
		Out: &buf,
		// No stdin and no store: there is no terminal to read from here, and the service already
		// holds the database, so the commands take their API path.
		Transport: inProcessTransport,
	}
	if runErr := env.Run(args); runErr != nil {
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			buf.WriteByte('\n')
		}
		fmt.Fprintf(&buf, "error: %v\n", runErr)
	}
	return buf.String(), nil
}

// ExecCommands lists the client command names, so a console can offer completion without
// hardcoding a list that would drift from the real one.
func ExecCommands() string {
	return strings.Join(cli.Commands, "\n")
}

// inProcessTransport dispatches straight into the in-process handler, exactly as Request does —
// there is no socket to open here, and no daemon on the other end of one.
func inProcessTransport(method, path string, body any, out any) error {
	payload := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = string(data)
	}
	raw, err := Request(method, path, payload)
	if err != nil {
		return err
	}
	if out == nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

// tokenize splits a command line the way a shell would, so quoting a message with spaces works:
//
//	send --to "Jio Phone" --text 'hello there'
//
// Only the parts that matter for typing a command are supported — single quotes, double quotes and
// backslash escapes. There is no expansion of any kind: no globs, no variables, no substitution.
// Nothing here should be able to reach beyond the argument list it returns.
func tokenize(line string) ([]string, error) {
	var (
		args    []string
		current strings.Builder
		quote   rune // 0, '\'' or '"'
		started bool // distinguishes "" from an absent argument
	)

	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\' && quote != '\'':
			// A backslash escapes the next character everywhere but inside single quotes, which is
			// what makes '\' usable as a literal path separator when single-quoted.
			if i+1 >= len(runes) {
				return nil, fmt.Errorf("trailing backslash")
			}
			i++
			current.WriteRune(runes[i])
			started = true
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
			started = true
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == ' ' || c == '\t' || c == '\n'):
			if started {
				args = append(args, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote", quote)
	}
	if started {
		args = append(args, current.String())
	}
	return args, nil
}
