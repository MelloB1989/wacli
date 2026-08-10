package tools

import (
	"strings"
	"testing"
)

// Every tool must be usable by a model: a unique name in the shape providers accept, a description
// that says when to reach for it, and a well-formed JSON Schema.
func TestAllToolsAreWellFormed(t *testing.T) {
	all := All(New(""))
	if len(all) < 25 {
		t.Fatalf("only %d tools; the point is to cover WhatsApp, not a subset", len(all))
	}

	seen := map[string]bool{}
	for _, tool := range all {
		t.Run(tool.Name, func(t *testing.T) {
			if seen[tool.Name] {
				t.Fatal("duplicate tool name")
			}
			seen[tool.Name] = true

			// Providers constrain tool names to ^[a-zA-Z0-9_-]{1,128}$.
			if len(tool.Name) == 0 || len(tool.Name) > 128 {
				t.Errorf("name length %d is out of range", len(tool.Name))
			}
			for _, r := range tool.Name {
				if !(r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
					t.Errorf("name contains %q, which providers reject", r)
				}
			}
			if !strings.HasPrefix(tool.Name, "whatsapp_") {
				t.Errorf("name %q is not namespaced; it will collide with other tool sets", tool.Name)
			}
			if len(tool.Description) < 30 {
				t.Errorf("description is too thin for a model to choose on: %q", tool.Description)
			}
			if tool.Handler == nil {
				t.Fatal("no handler")
			}

			params := map[string]any(tool.Parameters)
			if params["type"] != "object" {
				t.Errorf(`parameters type = %v, want "object"`, params["type"])
			}
			props, ok := params["properties"].(map[string]any)
			if !ok {
				t.Fatal("properties is not an object")
			}
			required, ok := params["required"].([]string)
			if !ok {
				t.Fatal("required is not a string list")
			}
			// A required field that isn't declared is a schema the model cannot satisfy.
			for _, r := range required {
				if _, exists := props[r]; !exists {
					t.Errorf("required field %q is not in properties", r)
				}
			}
			for name, raw := range props {
				prop, ok := raw.(map[string]any)
				if !ok {
					t.Errorf("property %q is not an object", name)
					continue
				}
				if prop["type"] == nil {
					t.Errorf("property %q has no type", name)
				}
				if desc, _ := prop["description"].(string); strings.TrimSpace(desc) == "" && prop["type"] != "array" {
					t.Errorf("property %q has no description", name)
				}
			}
		})
	}
}

// The groups should partition the whole set, so All() cannot silently drop a category.
func TestAllCoversEveryGroup(t *testing.T) {
	tl := New("")
	sum := len(tl.MessagingTools()) + len(tl.ChatTools()) + len(tl.CallTools()) +
		len(tl.TriggerTools()) + len(tl.WebhookTools()) + len(tl.SystemTools())
	if got := len(All(tl)); got != sum {
		t.Errorf("All() returned %d tools, groups sum to %d", got, sum)
	}
}
