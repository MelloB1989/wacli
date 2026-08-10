package wa

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Triggers: match an event, run actions.
//
// This replaces the old auto-reply rules, which could only answer an inbound message with one canned
// reply. A trigger is the general form of that — it selects events by kind, chat scope and content,
// then runs a list of actions — and an auto-reply is just a trigger on incoming_message with a single
// send_text. Existing auto_replies rows are migrated on first open.
//
// Everything is data, not code: a trigger is rows in SQLite, editable over the API or CLI at runtime.
// That is deliberate, since the alternative is people forking wacli to change a reply.

// Trigger match types.
const (
	MatchAlways   = "always"
	MatchContains = "contains"
	MatchExact    = "exact"
	MatchPrefix   = "prefix"
	MatchSuffix   = "suffix"
	MatchRegex    = "regex"
)

// Trigger chat scopes.
const (
	ScopeAll    = "all"
	ScopeDMs    = "dms"
	ScopeGroups = "groups"
	ScopeList   = "list"
)

// Trigger action types.
const (
	ActionSendText  = "send_text"
	ActionSendMedia = "send_media"
	ActionReact     = "react"
	ActionMarkRead  = "mark_read"
	ActionWebhook   = "webhook"
	ActionForward   = "forward"
)

// TriggerAction is one thing to do when a trigger fires.
//
// Text fields expand a small set of placeholders — {{text}}, {{sender}}, {{sender_name}}, {{chat}},
// {{chat_name}} — so a rule can answer with something derived from what arrived.
type TriggerAction struct {
	Type string `json:"type"`
	// Text is the message body for send_text/send_media, or the caption for media.
	Text string `json:"text,omitempty"`
	// MediaPath is an absolute path for send_media.
	MediaPath string `json:"media_path,omitempty"`
	// Emoji is the reaction for react.
	Emoji string `json:"emoji,omitempty"`
	// URL is the endpoint for webhook.
	URL string `json:"url,omitempty"`
	// To is the destination chat for forward, as a JID or a resolvable reference.
	To string `json:"to,omitempty"`
}

// Trigger is one rule: what to match, and what to do about it.
type Trigger struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Priority orders evaluation, lowest first. Ties break by ID.
	Priority int `json:"priority"`

	// Events selects which event kinds fire this trigger. Empty means incoming_message, which is
	// what almost every rule wants.
	Events []string `json:"events"`
	// MatchType and Pattern filter on message text.
	MatchType string `json:"match_type"`
	Pattern   string `json:"pattern,omitempty"`
	// Scope limits which chats apply; ChatJIDs is the allow-list when Scope is "list".
	Scope    string   `json:"scope"`
	ChatJIDs []string `json:"chat_jids,omitempty"`
	// FromMe filters on message direction: nil matches either, false only inbound, true only our own.
	FromMe *bool `json:"from_me,omitempty"`

	// Actions run in order when the trigger matches.
	Actions []TriggerAction `json:"actions"`

	// StopOnMatch prevents lower-priority triggers from running once this one fires. Auto-replies
	// behaved this way, so it defaults to true on migrated rules.
	StopOnMatch bool `json:"stop_on_match"`
	// CooldownSeconds suppresses re-firing for the same chat within the window, so a chatty rule
	// cannot turn into a message loop.
	CooldownSeconds int `json:"cooldown_seconds"`

	FireCount   int64     `json:"fire_count"`
	LastFiredAt time.Time `json:"last_fired_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Validate reports whether the trigger is coherent enough to store.
func (t *Trigger) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("trigger name is required")
	}
	switch t.MatchType {
	case MatchAlways, MatchContains, MatchExact, MatchPrefix, MatchSuffix:
	case MatchRegex:
		if _, err := regexp.Compile(t.Pattern); err != nil {
			return fmt.Errorf("pattern is not a valid regexp: %w", err)
		}
	case "":
		t.MatchType = MatchAlways
	default:
		return fmt.Errorf("unknown match_type %q", t.MatchType)
	}
	if t.MatchType != MatchAlways && strings.TrimSpace(t.Pattern) == "" {
		return fmt.Errorf("match_type %q needs a pattern", t.MatchType)
	}
	switch t.Scope {
	case ScopeAll, ScopeDMs, ScopeGroups:
	case ScopeList:
		if len(t.ChatJIDs) == 0 {
			return fmt.Errorf(`scope "list" needs at least one chat_jid`)
		}
	case "":
		t.Scope = ScopeAll
	default:
		return fmt.Errorf("unknown scope %q", t.Scope)
	}
	if len(t.Actions) == 0 {
		return fmt.Errorf("a trigger needs at least one action")
	}
	for i, a := range t.Actions {
		switch a.Type {
		case ActionSendText:
			if strings.TrimSpace(a.Text) == "" {
				return fmt.Errorf("action %d: send_text needs text", i)
			}
		case ActionSendMedia:
			if strings.TrimSpace(a.MediaPath) == "" {
				return fmt.Errorf("action %d: send_media needs media_path", i)
			}
		case ActionReact:
			if strings.TrimSpace(a.Emoji) == "" {
				return fmt.Errorf("action %d: react needs an emoji", i)
			}
		case ActionWebhook:
			if strings.TrimSpace(a.URL) == "" {
				return fmt.Errorf("action %d: webhook needs a url", i)
			}
		case ActionForward:
			if strings.TrimSpace(a.To) == "" {
				return fmt.Errorf("action %d: forward needs a destination", i)
			}
		case ActionMarkRead:
		default:
			return fmt.Errorf("action %d: unknown type %q", i, a.Type)
		}
	}
	if len(t.Events) == 0 {
		t.Events = []string{EventIncomingMessage}
	}
	return nil
}

// Event kinds a trigger can select. These are the same strings webhooks are subscribed to, so a
// trigger and a webhook can react to exactly the same things.
const (
	EventIncomingMessage = "incoming_message"
	EventOutgoingMessage = "outgoing_message"
	EventCallIncoming    = "call.incoming"
	EventCallPlaced      = "call.placed"
	EventCallAccepted    = "call.accepted"
	EventCallEnded       = "call.ended"
	EventCallRejected    = "call.rejected"
	EventConnectionState = "connection_state"
	EventSyncComplete    = "sync_complete"
	EventTriggerFired    = "trigger_fired"
)

// KnownEvents lists every event kind wacli emits, for validation and for tooling that wants to
// present the choices.
func KnownEvents() []string {
	return []string{
		EventIncomingMessage, EventOutgoingMessage,
		EventCallIncoming, EventCallPlaced, EventCallAccepted, EventCallEnded, EventCallRejected,
		EventConnectionState, EventSyncComplete, EventTriggerFired,
	}
}

// matchesEvent reports whether this trigger listens for the given event kind.
func (t *Trigger) matchesEvent(event string) bool {
	if len(t.Events) == 0 {
		return event == EventIncomingMessage
	}
	for _, e := range t.Events {
		if e == "*" || strings.EqualFold(e, event) {
			return true
		}
	}
	return false
}

// matchesChat reports whether the trigger's scope covers this chat.
func (t *Trigger) matchesChat(chat ChatRecord) bool {
	switch t.Scope {
	case ScopeDMs:
		return !chat.IsGroup
	case ScopeGroups:
		return chat.IsGroup
	case ScopeList:
		for _, jid := range t.ChatJIDs {
			if strings.EqualFold(strings.TrimSpace(jid), chat.JID) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// matchesText reports whether the message body satisfies the trigger's pattern.
func (t *Trigger) matchesText(text string) bool {
	body := strings.TrimSpace(text)
	pattern := strings.TrimSpace(t.Pattern)
	switch t.MatchType {
	case MatchAlways, "":
		return true
	case MatchExact:
		return strings.EqualFold(body, pattern)
	case MatchPrefix:
		return strings.HasPrefix(strings.ToLower(body), strings.ToLower(pattern))
	case MatchSuffix:
		return strings.HasSuffix(strings.ToLower(body), strings.ToLower(pattern))
	case MatchRegex:
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		return re.MatchString(body)
	default: // contains
		return strings.Contains(strings.ToLower(body), strings.ToLower(pattern))
	}
}

// Matches reports whether the trigger fires for this event, chat and message.
//
// Exported so a caller can dry-run a rule against a message without sending anything — which is what
// the /triggers/test endpoint and the trigger_test tool use.
func (t *Trigger) Matches(event string, chat ChatRecord, message MessageRecord) bool {
	if !t.Enabled || !t.matchesEvent(event) || !t.matchesChat(chat) {
		return false
	}
	if t.FromMe != nil && *t.FromMe != message.IsFromMe {
		return false
	}
	return t.matchesText(message.Content)
}

// expandPlaceholders substitutes the values a rule can refer to in its text.
func expandPlaceholders(text string, chat ChatRecord, message MessageRecord) string {
	r := strings.NewReplacer(
		"{{text}}", message.Content,
		"{{sender}}", message.SenderJID,
		"{{sender_name}}", message.SenderJID,
		"{{chat}}", chat.JID,
		"{{chat_name}}", nonEmptyString(chat.Name, chat.JID),
	)
	return r.Replace(text)
}

func nonEmptyString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// encodeStrings/decodeStrings store list columns as JSON, matching how webhooks persist theirs.
func encodeStrings(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func decodeStrings(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func encodeActions(actions []TriggerAction) string {
	raw, err := json.Marshal(actions)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func decodeActions(raw string) []TriggerAction {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []TriggerAction
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
