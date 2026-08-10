package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// The rest of the daemon's surface, so a Go caller can do anything the CLI can.
//
// Everything here is a thin typed wrapper over one HTTP endpoint. Results come back as map[string]any
// rather than fully-typed structs where the daemon's shape is a loose envelope — the endpoints return
// {"ok":true,"thing":{...}} and pinning every one to a struct would make this file churn every time a
// field is added, for no benefit to callers who mostly forward the JSON on.

// Raw performs an arbitrary request against the daemon, for endpoints this package has not wrapped.
func (c *Client) Raw(ctx context.Context, method, path string, body any) (map[string]any, error) {
	var out map[string]any
	if err := c.do(ctx, method, path, nil, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- messaging ---

// SendMessage sends text, media, or a reply. mediaPath must be readable by the daemon.
func (c *Client) SendMessage(ctx context.Context, to, text, mediaPath, replyTo string) (map[string]any, error) {
	body := map[string]any{"to": to, "text": text}
	if mediaPath != "" {
		body["media"] = mediaPath
	}
	if replyTo != "" {
		body["reply_to"] = replyTo
	}
	return c.Raw(ctx, http.MethodPost, "/send", body)
}

// EditMessage rewrites a message this account sent.
func (c *Client) EditMessage(ctx context.Context, chat, messageID, text string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/messages/edit",
		map[string]any{"chat": chat, "message_id": messageID, "text": text})
}

// DeleteMessage revokes a message for everyone.
func (c *Client) DeleteMessage(ctx context.Context, chat, messageID string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/messages/delete",
		map[string]any{"chat": chat, "message_id": messageID})
}

// MessageReceipts reports delivery and read state for one message.
func (c *Client) MessageReceipts(ctx context.Context, messageID string) (map[string]any, error) {
	q := url.Values{"message_id": {messageID}}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/messages/receipts", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SearchMessages queries the local message history.
func (c *Client) SearchMessages(ctx context.Context, opts MessageQuery) (map[string]any, error) {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("chat_ref", opts.Chat)
	set("sender_ref", opts.Sender)
	set("query", opts.Query)
	set("from_me", opts.FromMe)
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.MediaOnly {
		q.Set("media_only", "true")
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/messages", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MessageQuery filters a message search.
type MessageQuery struct {
	Chat      string
	Sender    string
	Query     string
	Limit     int
	MediaOnly bool
	// FromMe is "true", "false", or empty for either.
	FromMe string
}

// BulkSend delivers the same or different messages to many chats, paced by the daemon.
func (c *Client) BulkSend(ctx context.Context, items []BulkItem, intervalMS int) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/bulk_send",
		map[string]any{"items": items, "interval_ms": intervalMS})
}

// BulkItem is one destination in a bulk send.
type BulkItem struct {
	To    string `json:"to"`
	Text  string `json:"text,omitempty"`
	Media string `json:"media,omitempty"`
}

// SendStory posts to this account's status/story.
func (c *Client) SendStory(ctx context.Context, text, mediaPath string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/stories", map[string]any{"text": text, "media": mediaPath})
}

// DownloadMedia fetches a message's attachment to a local path on the daemon's filesystem.
func (c *Client) DownloadMedia(ctx context.Context, chat, messageID string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/media/download",
		map[string]any{"chat": chat, "message_id": messageID})
}

// --- chats and contacts ---

// GetChat returns one chat with its recent messages.
func (c *Client) GetChat(ctx context.Context, ref string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodGet, "/chats/"+url.PathEscape(ref), nil)
}

// ListContacts returns the address book, optionally filtered.
func (c *Client) ListContacts(ctx context.Context, query string, limit int) (map[string]any, error) {
	q := url.Values{}
	if query != "" {
		q.Set("query", query)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/contacts", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetContact looks up one contact by phone, JID or name.
func (c *Client) GetContact(ctx context.Context, ref string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodGet, "/contacts/"+url.PathEscape(ref), nil)
}

// Resolve turns a loose reference — a name, a phone number, part of either — into candidate JIDs.
// Worth calling before acting on user-supplied text, since it reports ambiguity instead of guessing.
func (c *Client) Resolve(ctx context.Context, ref, kind string, limit int) (map[string]any, error) {
	q := url.Values{"ref": {ref}}
	if kind != "" {
		q.Set("kind", kind)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/resolve", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- calls ---

// PlaceCall rings a contact, optionally speaking text or playing a file, and optionally recording
// the other party.
func (c *Client) PlaceCall(ctx context.Context, opts CallRequest) (map[string]any, error) {
	body := map[string]any{"to": opts.To, "video": opts.Video}
	if opts.Say != "" {
		body["say"] = opts.Say
	}
	if opts.Voice != "" {
		body["voice"] = opts.Voice
	}
	if opts.Audio != "" {
		body["audio"] = opts.Audio
	}
	if opts.Record != "" {
		body["record"] = opts.Record
	}
	if opts.Repeat {
		body["repeat"] = true
	}
	if opts.RingForSeconds > 0 {
		body["ring_for_seconds"] = opts.RingForSeconds
	}
	return c.Raw(ctx, http.MethodPost, "/calls", body)
}

// CallRequest describes an outgoing call.
type CallRequest struct {
	To             string
	Say            string
	Voice          string
	Audio          string
	Record         string
	Repeat         bool
	Video          bool
	RingForSeconds int
}

// AnswerCall accepts a ringing call, optionally speaking into it and recording the caller.
func (c *Client) AnswerCall(ctx context.Context, callID, say, audio, record string, repeat bool) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/calls/answer", map[string]any{
		"call_id": callID, "say": say, "audio": audio, "record": record, "repeat": repeat,
	})
}

// EndCall hangs up, by short handle (c1), full call ID, or unique prefix.
func (c *Client) EndCall(ctx context.Context, ref, reason string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/calls/end", map[string]any{"call_id": ref, "reason": reason})
}

// RejectCall declines a ringing call.
func (c *Client) RejectCall(ctx context.Context, ref string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/calls/reject", map[string]any{"call_id": ref})
}

// CallStatus reports one call: state, duration, media, recording, and the queue.
func (c *Client) CallStatus(ctx context.Context, ref string) (map[string]any, error) {
	q := url.Values{"ref": {ref}}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/calls/status", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListCalls returns recent and active calls.
func (c *Client) ListCalls(ctx context.Context, activeOnly bool) (map[string]any, error) {
	q := url.Values{}
	if activeOnly {
		q.Set("active", "true")
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/calls", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CallQueue reports the active call and everything waiting behind it.
func (c *Client) CallQueue(ctx context.Context) (map[string]any, error) {
	return c.Raw(ctx, http.MethodGet, "/calls/queue", nil)
}

// --- triggers ---

// ListTriggers returns every rule, in evaluation order, with the event kinds available.
func (c *Client) ListTriggers(ctx context.Context) (map[string]any, error) {
	return c.Raw(ctx, http.MethodGet, "/triggers", nil)
}

// CreateTrigger stores a new rule. See wa.Trigger for the shape.
func (c *Client) CreateTrigger(ctx context.Context, trigger any) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/triggers", trigger)
}

// UpdateTrigger replaces a rule's definition.
func (c *Client) UpdateTrigger(ctx context.Context, id int64, trigger any) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPut, fmt.Sprintf("/triggers/%d", id), trigger)
}

// SetTriggerEnabled toggles a rule without rewriting it.
func (c *Client) SetTriggerEnabled(ctx context.Context, id int64, enabled bool) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPatch, fmt.Sprintf("/triggers/%d", id), map[string]any{"enabled": enabled})
}

// DeleteTrigger removes a rule.
func (c *Client) DeleteTrigger(ctx context.Context, id int64) (map[string]any, error) {
	return c.Raw(ctx, http.MethodDelete, fmt.Sprintf("/triggers/%d", id), nil)
}

// TestTrigger dry-runs a rule against a hypothetical message, sending nothing.
func (c *Client) TestTrigger(ctx context.Context, id int64, chat, text string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/triggers/test",
		map[string]any{"id": id, "chat": chat, "text": text})
}

// --- webhooks ---

// CreateWebhook subscribes an endpoint to events.
func (c *Client) CreateWebhook(ctx context.Context, hook any) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/webhooks", hook)
}

// UpdateWebhook replaces a subscription.
func (c *Client) UpdateWebhook(ctx context.Context, id int64, hook any) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPut, fmt.Sprintf("/webhooks/%d", id), hook)
}

// DeleteWebhook removes a subscription.
func (c *Client) DeleteWebhook(ctx context.Context, id int64) (map[string]any, error) {
	return c.Raw(ctx, http.MethodDelete, fmt.Sprintf("/webhooks/%d", id), nil)
}

// WebhookDeliveries reports recent delivery attempts, for debugging a consumer that is not receiving.
func (c *Client) WebhookDeliveries(ctx context.Context, limit int) (map[string]any, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/webhook_deliveries", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TestWebhook fires a synthetic event at a subscription, to check reachability and signing without
// waiting for something real to happen.
func (c *Client) TestWebhook(ctx context.Context, id int64) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/webhooks/test", map[string]any{"id": id})
}

// ReplayDelivery re-sends a recorded delivery by ID, body unchanged.
func (c *Client) ReplayDelivery(ctx context.Context, id int64) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/webhook_deliveries/replay", map[string]any{"id": id})
}

// --- groups, presence, reachability ---

// ListGroups returns every group this account belongs to.
func (c *Client) ListGroups(ctx context.Context) (map[string]any, error) {
	return c.Raw(ctx, http.MethodGet, "/groups", nil)
}

// GroupInfo returns one group with its participants.
func (c *Client) GroupInfo(ctx context.Context, ref string) (map[string]any, error) {
	q := url.Values{"ref": {ref}}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/groups", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateGroup creates a group with the given members.
func (c *Client) CreateGroup(ctx context.Context, name string, participants []string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/groups",
		map[string]any{"name": name, "participants": participants})
}

// UpdateGroupParticipants adds, removes, promotes or demotes members.
func (c *Client) UpdateGroupParticipants(ctx context.Context, group, action string, participants []string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/groups/participants",
		map[string]any{"group": group, "action": action, "participants": participants})
}

// SetGroupName renames a group.
func (c *Client) SetGroupName(ctx context.Context, group, name string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/groups/update", map[string]any{"group": group, "name": name})
}

// SetGroupTopic sets a group's description.
func (c *Client) SetGroupTopic(ctx context.Context, group, topic string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/groups/update", map[string]any{"group": group, "topic": topic})
}

// LeaveGroup leaves a group.
func (c *Client) LeaveGroup(ctx context.Context, group string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/groups/update", map[string]any{"group": group, "leave": true})
}

// GroupInviteLink returns a group's invite link, optionally revoking the previous one.
func (c *Client) GroupInviteLink(ctx context.Context, group string, reset bool) (map[string]any, error) {
	q := url.Values{"group": {group}}
	if reset {
		q.Set("reset", "true")
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/groups/invite", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// JoinGroup joins a group from an invite link, or previews it without joining.
func (c *Client) JoinGroup(ctx context.Context, link string, preview bool) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/groups/invite",
		map[string]any{"link": link, "preview": preview})
}

// SetTyping shows or clears the typing indicator in a chat.
func (c *Client) SetTyping(ctx context.Context, chat string, typing, recording bool) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/presence",
		map[string]any{"chat": chat, "typing": typing, "recording": recording})
}

// SetPresence announces this account as available or unavailable.
func (c *Client) SetPresence(ctx context.Context, available bool) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/presence", map[string]any{"available": available})
}

// CheckOnWhatsApp reports which phone numbers are reachable on WhatsApp.
func (c *Client) CheckOnWhatsApp(ctx context.Context, phones []string) (map[string]any, error) {
	return c.Raw(ctx, http.MethodPost, "/contacts/check", map[string]any{"phones": phones})
}
