// Package tools exposes wacli's WhatsApp capabilities as karma GoFunctionTools, so an agent can
// send messages, place calls, manage triggers and webhooks, and read history.
//
// The tools drive a running `wacli daemon` over its local HTTP API rather than opening a WhatsApp
// session themselves. That is deliberate: only one process may hold a session, so tools that talk to
// the daemon coexist with the CLI, the TUI and anything else, instead of fighting for the socket.
// Point them at a daemon with New, or embed github.com/MelloB1989/wacli/wa to be the daemon.
//
//	kai := ai.NewKarmaAI(ai.WithGoFunctionTools(tools.All(tools.New(""))))
//
// Every handler returns JSON, since that is what a model consumes best, and errors come back as
// values rather than Go errors where the model can usefully react to them (a failed send is
// information; a malformed request is a bug).
package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/MelloB1989/karma/ai"
	"github.com/MelloB1989/wacli/client"
)

// Tools drives a wacli daemon.
type Tools struct {
	c *client.Client
}

// New builds a tool set against the daemon at baseURL. An empty baseURL uses WACLI_HTTP_ADDR, or the
// default 127.0.0.1:8765.
func New(baseURL string) *Tools {
	return &Tools{c: client.New(baseURL)}
}

// NewWithClient wraps an already-configured client.
func NewWithClient(c *client.Client) *Tools { return &Tools{c: c} }

// Client exposes the underlying client for calls these tools do not wrap.
func (t *Tools) Client() *client.Client { return t.c }

// --- schema helpers ---

// object builds a JSON Schema object for a tool's parameters.
func object(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func str(desc string) map[string]any   { return map[string]any{"type": "string", "description": desc} }
func boolp(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func intp(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }

func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

// arg reads a string argument.
func arg(p ai.FuncParams, key string) string {
	if v, ok := p[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// argBool reads a boolean argument.
func argBool(p ai.FuncParams, key string) bool {
	if v, ok := p[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// argInt reads an integer argument, tolerating the float64 JSON decodes numbers into.
func argInt(p ai.FuncParams, key string) int {
	switch v := p[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

// result renders a handler's outcome as JSON for the model.
func result(v any, err error) (string, error) {
	if err != nil {
		// An operational failure is information the model should see and can act on, not a crash.
		out, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
		return string(out), nil
	}
	out, mErr := json.Marshal(v)
	if mErr != nil {
		return "", mErr
	}
	return string(out), nil
}

// tool is a small constructor so each definition below reads as data.
func tool(name, description string, params map[string]any,
	handler func(context.Context, ai.FuncParams) (string, error)) ai.GoFunctionTool {
	return ai.GoFunctionTool{
		Name:        name,
		Description: description,
		Parameters:  params,
		Handler:     handler,
	}
}
