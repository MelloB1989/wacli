package wa

import "testing"

func dm(jid string) ChatRecord    { return ChatRecord{JID: jid, IsGroup: false} }
func group(jid string) ChatRecord { return ChatRecord{JID: jid, IsGroup: true} }
func msg(text string) MessageRecord {
	return MessageRecord{Content: text, SenderJID: "1@s.whatsapp.net"}
}

// A trigger fires only when event, scope, direction and pattern all agree — getting any of them
// wrong means replying to the wrong chat, or to ourselves.
func TestTriggerMatches(t *testing.T) {
	inbound := false
	base := Trigger{
		Enabled: true, Events: []string{EventIncomingMessage},
		MatchType: MatchContains, Pattern: "hello", Scope: ScopeAll, FromMe: &inbound,
		Actions: []TriggerAction{{Type: ActionSendText, Text: "hi"}},
	}

	cases := []struct {
		name  string
		mut   func(*Trigger)
		event string
		chat  ChatRecord
		m     MessageRecord
		want  bool
	}{
		{"matches", nil, EventIncomingMessage, dm("a@s.whatsapp.net"), msg("well hello there"), true},
		{"case-insensitive", nil, EventIncomingMessage, dm("a@s.whatsapp.net"), msg("HELLO"), true},
		{"pattern absent", nil, EventIncomingMessage, dm("a@s.whatsapp.net"), msg("goodbye"), false},
		{"wrong event", nil, EventOutgoingMessage, dm("a@s.whatsapp.net"), msg("hello"), false},
		{"disabled", func(x *Trigger) { x.Enabled = false }, EventIncomingMessage, dm("a@s.whatsapp.net"), msg("hello"), false},
		{"dms only, in a group", func(x *Trigger) { x.Scope = ScopeDMs }, EventIncomingMessage, group("g@g.us"), msg("hello"), false},
		{"groups only, in a group", func(x *Trigger) { x.Scope = ScopeGroups }, EventIncomingMessage, group("g@g.us"), msg("hello"), true},
		{"list scope, listed", func(x *Trigger) { x.Scope = ScopeList; x.ChatJIDs = []string{"a@s.whatsapp.net"} },
			EventIncomingMessage, dm("a@s.whatsapp.net"), msg("hello"), true},
		{"list scope, not listed", func(x *Trigger) { x.Scope = ScopeList; x.ChatJIDs = []string{"z@s.whatsapp.net"} },
			EventIncomingMessage, dm("a@s.whatsapp.net"), msg("hello"), false},
		{"always matches any text", func(x *Trigger) { x.MatchType = MatchAlways; x.Pattern = "" },
			EventIncomingMessage, dm("a@s.whatsapp.net"), msg("anything"), true},
		{"regex", func(x *Trigger) { x.MatchType = MatchRegex; x.Pattern = `^ping \d+$` },
			EventIncomingMessage, dm("a@s.whatsapp.net"), msg("ping 42"), true},
		{"wildcard event", func(x *Trigger) { x.Events = []string{"*"} },
			EventCallIncoming, dm("a@s.whatsapp.net"), msg("hello"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			trig := base
			if c.mut != nil {
				c.mut(&trig)
			}
			if got := trig.Matches(c.event, c.chat, c.m); got != c.want {
				t.Errorf("Matches = %v, want %v", got, c.want)
			}
		})
	}
}

// Our own messages must not fire an inbound rule, or a reply triggers itself forever.
func TestTriggerDirectionFilter(t *testing.T) {
	inbound, outbound := false, true
	trig := Trigger{
		Enabled: true, MatchType: MatchAlways, Scope: ScopeAll,
		Actions: []TriggerAction{{Type: ActionSendText, Text: "hi"}},
	}
	own := MessageRecord{Content: "hi", IsFromMe: true}
	theirs := MessageRecord{Content: "hi"}

	trig.FromMe = &inbound
	if trig.Matches(EventIncomingMessage, dm("a"), own) {
		t.Error("an inbound-only trigger fired on our own message — that is a reply loop")
	}
	if !trig.Matches(EventIncomingMessage, dm("a"), theirs) {
		t.Error("an inbound-only trigger did not fire on their message")
	}

	trig.FromMe = &outbound
	if !trig.Matches(EventIncomingMessage, dm("a"), own) {
		t.Error("an outbound-only trigger did not fire on our own message")
	}

	trig.FromMe = nil
	if !trig.Matches(EventIncomingMessage, dm("a"), own) || !trig.Matches(EventIncomingMessage, dm("a"), theirs) {
		t.Error("an unfiltered trigger should fire on both directions")
	}
}

func TestTriggerValidate(t *testing.T) {
	ok := Trigger{Name: "n", MatchType: MatchContains, Pattern: "x", Scope: ScopeAll,
		Actions: []TriggerAction{{Type: ActionSendText, Text: "hi"}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid trigger rejected: %v", err)
	}
	if len(ok.Events) == 0 || ok.Events[0] != EventIncomingMessage {
		t.Errorf("Events defaulted to %v, want [incoming_message]", ok.Events)
	}

	bad := []struct {
		name string
		t    Trigger
	}{
		{"no name", Trigger{MatchType: MatchAlways, Actions: []TriggerAction{{Type: ActionSendText, Text: "x"}}}},
		{"no actions", Trigger{Name: "n", MatchType: MatchAlways}},
		{"unknown action", Trigger{Name: "n", MatchType: MatchAlways, Actions: []TriggerAction{{Type: "nope"}}}},
		{"pattern required", Trigger{Name: "n", MatchType: MatchContains, Actions: []TriggerAction{{Type: ActionSendText, Text: "x"}}}},
		{"bad regex", Trigger{Name: "n", MatchType: MatchRegex, Pattern: "([", Actions: []TriggerAction{{Type: ActionSendText, Text: "x"}}}},
		{"list scope with no chats", Trigger{Name: "n", MatchType: MatchAlways, Scope: ScopeList, Actions: []TriggerAction{{Type: ActionSendText, Text: "x"}}}},
		{"send_text with no text", Trigger{Name: "n", MatchType: MatchAlways, Actions: []TriggerAction{{Type: ActionSendText}}}},
		{"react with no emoji", Trigger{Name: "n", MatchType: MatchAlways, Actions: []TriggerAction{{Type: ActionReact}}}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			trig := c.t
			if err := trig.Validate(); err == nil {
				t.Error("accepted an invalid trigger")
			}
		})
	}
}

func TestExpandPlaceholders(t *testing.T) {
	chat := ChatRecord{JID: "g@g.us", Name: "Team"}
	m := MessageRecord{Content: "ping", SenderJID: "1@s.whatsapp.net"}
	got := expandPlaceholders("{{chat_name}}/{{chat}}: {{sender}} said {{text}}", chat, m)
	want := "Team/g@g.us: 1@s.whatsapp.net said ping"
	if got != want {
		t.Errorf("expandPlaceholders = %q, want %q", got, want)
	}
	// An unnamed chat falls back to its JID rather than rendering an empty string.
	if got := expandPlaceholders("{{chat_name}}", ChatRecord{JID: "x@s.whatsapp.net"}, m); got != "x@s.whatsapp.net" {
		t.Errorf("unnamed chat rendered %q", got)
	}
}

func TestNormalizeLegacyMatch(t *testing.T) {
	for in, want := range map[string]string{
		"": MatchAlways, "always": MatchAlways, "exact": MatchExact,
		"prefix": MatchPrefix, "starts_with": MatchPrefix,
		"suffix": MatchSuffix, "ends_with": MatchSuffix,
		"regex": MatchRegex, "contains": MatchContains, "anything else": MatchContains,
	} {
		if got := normalizeLegacyMatch(in); got != want {
			t.Errorf("normalizeLegacyMatch(%q) = %q, want %q", in, got, want)
		}
	}
}
