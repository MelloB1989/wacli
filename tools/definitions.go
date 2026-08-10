package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MelloB1989/karma/ai"
	"github.com/MelloB1989/wacli/client"
)

// All returns every tool. Register them with karma:
//
//	kai := ai.NewKarmaAI(ai.WithGoFunctionTools(tools.All(tools.New(""))))
func All(t *Tools) []ai.GoFunctionTool {
	var out []ai.GoFunctionTool
	out = append(out, t.MessagingTools()...)
	out = append(out, t.ChatTools()...)
	out = append(out, t.CallTools()...)
	out = append(out, t.TriggerTools()...)
	out = append(out, t.GroupTools()...)
	out = append(out, t.PresenceTools()...)
	out = append(out, t.WebhookTools()...)
	out = append(out, t.SystemTools()...)
	return out
}

// MessagingTools send and inspect messages.
func (t *Tools) MessagingTools() []ai.GoFunctionTool {
	return []ai.GoFunctionTool{
		tool("whatsapp_send_message",
			"Send a WhatsApp message to a person or group. Accepts a phone number, a JID, or a "+
				"contact/group name — resolve ambiguous names with whatsapp_resolve first. Can attach a "+
				"file and can reply to a specific message.",
			object(map[string]any{
				"to":       str("Recipient: phone number, JID, or contact/group name."),
				"text":     str("Message body. Optional when sending media."),
				"media":    str("Absolute path to a file to attach, readable by the daemon."),
				"reply_to": str("Message ID to quote in reply."),
			}, "to"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				if arg(p, "text") == "" && arg(p, "media") == "" {
					return result(nil, fmt.Errorf("provide text, media, or both"))
				}
				return result(t.c.SendMessage(ctx, arg(p, "to"), arg(p, "text"), arg(p, "media"), arg(p, "reply_to")))
			}),

		tool("whatsapp_search_messages",
			"Search this account's message history: by chat, by sender, by text, or any combination. "+
				"Use it to read a conversation before replying.",
			object(map[string]any{
				"chat":       str("Limit to one chat: JID, phone, or name."),
				"sender":     str("Limit to messages from one sender."),
				"query":      str("Text to search for."),
				"limit":      intp("Maximum messages to return. Default 50."),
				"media_only": boolp("Only messages carrying media."),
				"from_me":    enum("Direction filter.", "true", "false", ""),
			}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				limit := argInt(p, "limit")
				if limit == 0 {
					limit = 50
				}
				return result(t.c.SearchMessages(ctx, client.MessageQuery{
					Chat: arg(p, "chat"), Sender: arg(p, "sender"), Query: arg(p, "query"),
					Limit: limit, MediaOnly: argBool(p, "media_only"), FromMe: arg(p, "from_me"),
				}))
			}),

		tool("whatsapp_edit_message",
			"Edit a message this account already sent. Only your own messages can be edited.",
			object(map[string]any{
				"chat":       str("Chat the message is in."),
				"message_id": str("ID of the message to edit."),
				"text":       str("Replacement text."),
			}, "chat", "message_id", "text"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.EditMessage(ctx, arg(p, "chat"), arg(p, "message_id"), arg(p, "text")))
			}),

		tool("whatsapp_delete_message",
			"Delete (revoke) a message for everyone in the chat.",
			object(map[string]any{
				"chat":       str("Chat the message is in."),
				"message_id": str("ID of the message to delete."),
			}, "chat", "message_id"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.DeleteMessage(ctx, arg(p, "chat"), arg(p, "message_id")))
			}),

		tool("whatsapp_message_receipts",
			"Check whether a message was delivered and read, and by whom.",
			object(map[string]any{"message_id": str("Message ID to check.")}, "message_id"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.MessageReceipts(ctx, arg(p, "message_id")))
			}),

		tool("whatsapp_bulk_send",
			"Send messages to many chats in one operation, paced by the daemon to avoid rate limits. "+
				"Each item may carry its own text and media.",
			object(map[string]any{
				"items": map[string]any{
					"type":        "array",
					"description": "Destinations and what to send to each.",
					"items": object(map[string]any{
						"to":    str("Recipient: phone, JID, or name."),
						"text":  str("Message body."),
						"media": str("Absolute path to attach."),
					}, "to"),
				},
				"interval_ms": intp("Milliseconds between sends. Default 1000."),
			}, "items"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				raw, err := json.Marshal(p["items"])
				if err != nil {
					return result(nil, err)
				}
				var items []client.BulkItem
				if err := json.Unmarshal(raw, &items); err != nil {
					return result(nil, fmt.Errorf("items is not a list of {to,text,media}: %w", err))
				}
				interval := argInt(p, "interval_ms")
				if interval == 0 {
					interval = 1000
				}
				return result(t.c.BulkSend(ctx, items, interval))
			}),

		tool("whatsapp_send_story",
			"Post to this account's WhatsApp status (story).",
			object(map[string]any{
				"text":  str("Status text."),
				"media": str("Absolute path to an image or video."),
			}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.SendStory(ctx, arg(p, "text"), arg(p, "media")))
			}),

		tool("whatsapp_download_media",
			"Download a message's attachment to the daemon's filesystem and return its path.",
			object(map[string]any{
				"chat":       str("Chat the message is in."),
				"message_id": str("Message carrying the attachment."),
			}, "chat", "message_id"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.DownloadMedia(ctx, arg(p, "chat"), arg(p, "message_id")))
			}),
	}
}

// ChatTools read chats and contacts.
func (t *Tools) ChatTools() []ai.GoFunctionTool {
	return []ai.GoFunctionTool{
		tool("whatsapp_resolve",
			"Turn a loose reference — a name, part of a name, a phone number — into concrete WhatsApp "+
				"JIDs. Call this before sending when the target came from a human, since it reports "+
				"several candidates rather than guessing which 'John' was meant.",
			object(map[string]any{
				"ref":   str("What to resolve: name, phone, or partial."),
				"kind":  enum("Restrict to a kind.", "chat", "contact", "group", ""),
				"limit": intp("Maximum candidates. Default 10."),
			}, "ref"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.Resolve(ctx, arg(p, "ref"), arg(p, "kind"), argInt(p, "limit")))
			}),

		tool("whatsapp_list_chats",
			"List chats: recent conversations, groups, or DMs.",
			object(map[string]any{
				"filter": enum("Which chats.", "all", "locked", "unlocked", "groups", "dms"),
				"query":  str("Filter by name."),
				"limit":  intp("Maximum chats. Default 100."),
			}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				limit := argInt(p, "limit")
				if limit == 0 {
					limit = 100
				}
				chats, err := t.c.ListChats(ctx, client.ChatListOptions{
					Filter: arg(p, "filter"), Query: arg(p, "query"), Limit: limit,
				})
				return result(map[string]any{"chats": chats}, err)
			}),

		tool("whatsapp_get_chat",
			"Get one chat with its recent messages.",
			object(map[string]any{"chat": str("Chat: JID, phone, or name.")}, "chat"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.GetChat(ctx, arg(p, "chat")))
			}),

		tool("whatsapp_list_contacts",
			"List or search the address book.",
			object(map[string]any{
				"query": str("Filter by name or number."),
				"limit": intp("Maximum contacts. Default 100."),
			}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.ListContacts(ctx, arg(p, "query"), argInt(p, "limit")))
			}),

		tool("whatsapp_get_contact",
			"Look up one contact by phone, JID or name.",
			object(map[string]any{"ref": str("Contact reference.")}, "ref"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.GetContact(ctx, arg(p, "ref")))
			}),

		tool("whatsapp_set_chat_locked",
			"Lock or unlock a chat. Locked chats are hidden from automation, which is how this account "+
				"keeps private conversations out of reach of tools like these.",
			object(map[string]any{
				"chat":   str("Chat JID."),
				"locked": boolp("True to lock, false to unlock."),
			}, "chat", "locked"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				err := t.c.SetChatLocked(ctx, arg(p, "chat"), argBool(p, "locked"))
				return result(map[string]any{"ok": err == nil, "chat": arg(p, "chat")}, err)
			}),
	}
}

// CallTools place, answer and inspect voice calls.
func (t *Tools) CallTools() []ai.GoFunctionTool {
	return []ai.GoFunctionTool{
		tool("whatsapp_place_call",
			"Place a real WhatsApp voice call. It can speak text aloud when the person answers, play an "+
				"audio file, and record what the other party says. Only one call runs at a time; further "+
				"calls queue automatically.",
			object(map[string]any{
				"to":     str("Who to call: phone number, JID, or contact name."),
				"say":    str("Text to speak once they answer."),
				"audio":  str("Absolute path to a .wav/.mp3/.opus to play instead of speaking."),
				"record": str("Absolute path to write the other party's voice to, as a WAV."),
				"repeat": boolp("Loop the audio instead of hanging up when it finishes."),
				"voice":  str("Speech synthesiser voice for 'say'."),
			}, "to"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.PlaceCall(ctx, client.CallRequest{
					To: arg(p, "to"), Say: arg(p, "say"), Voice: arg(p, "voice"),
					Audio: arg(p, "audio"), Record: arg(p, "record"), Repeat: argBool(p, "repeat"),
				}))
			}),

		tool("whatsapp_answer_call",
			"Answer a ringing call, optionally speaking into it and recording the caller.",
			object(map[string]any{
				"call_id": str("Call to answer: short handle (c1), full ID, or omit for the only ringing call."),
				"say":     str("Text to speak into the call."),
				"audio":   str("Audio file to play instead of speaking."),
				"record":  str("Path to record the caller's voice to."),
				"repeat":  boolp("Loop the audio."),
			}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.AnswerCall(ctx, arg(p, "call_id"), arg(p, "say"),
					arg(p, "audio"), arg(p, "record"), argBool(p, "repeat")))
			}),

		tool("whatsapp_end_call",
			"Hang up a call, or cancel one still queued.",
			object(map[string]any{
				"call_id": str("Short handle (c1), full ID, or unique prefix. Omit for the active call."),
				"reason":  str("Why it ended. Default 'hangup'."),
			}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.EndCall(ctx, arg(p, "call_id"), arg(p, "reason")))
			}),

		tool("whatsapp_reject_call",
			"Decline an incoming call without answering.",
			object(map[string]any{"call_id": str("Call to reject.")}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.RejectCall(ctx, arg(p, "call_id")))
			}),

		tool("whatsapp_call_status",
			"Check a call: state, duration, whether media is live, and whether anything was recorded.",
			object(map[string]any{
				"call_id": str("Short handle (c1), full ID, or prefix. Omit for the active call."),
			}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.CallStatus(ctx, arg(p, "call_id")))
			}),

		tool("whatsapp_list_calls",
			"List recent and active calls with their short handles.",
			object(map[string]any{"active_only": boolp("Only calls still in progress.")}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.ListCalls(ctx, argBool(p, "active_only")))
			}),
	}
}

// TriggerTools manage the rule engine.
func (t *Tools) TriggerTools() []ai.GoFunctionTool {
	return []ai.GoFunctionTool{
		tool("whatsapp_list_triggers",
			"List automation rules and the event kinds they can listen for. A trigger matches an event "+
				"and runs actions — replying, reacting, forwarding, calling a webhook.",
			object(map[string]any{}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.ListTriggers(ctx))
			}),

		tool("whatsapp_create_trigger",
			"Create an automation rule: when a matching event arrives, run actions. Use this to set up "+
				"auto-replies, keyword alerts, forwarding, or webhook notifications without writing code.",
			object(map[string]any{
				"name":       str("Human-readable rule name."),
				"match_type": enum("How to match message text.", "always", "contains", "exact", "prefix", "suffix", "regex"),
				"pattern":    str("Text or regex to match. Not needed when match_type is 'always'."),
				"scope":      enum("Which chats this applies to.", "all", "dms", "groups", "list"),
				"chat_jids": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Chat JIDs when scope is 'list'.",
				},
				"events": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Event kinds to listen for. Defaults to incoming_message.",
				},
				"actions": map[string]any{
					"type":        "array",
					"description": "What to do when it matches, in order.",
					"items": object(map[string]any{
						"type":       enum("Action kind.", "send_text", "send_media", "react", "mark_read", "webhook", "forward"),
						"text":       str("Message body. Supports {{text}}, {{sender}}, {{chat}}, {{chat_name}}."),
						"media_path": str("Absolute file path for send_media."),
						"emoji":      str("Emoji for react."),
						"url":        str("Endpoint for webhook."),
						"to":         str("Destination chat for forward."),
					}, "type"),
				},
				"priority":         intp("Evaluation order, lowest first. Default 100."),
				"cooldown_seconds": intp("Minimum seconds between firings per chat. Guards against reply loops."),
				"stop_on_match":    boolp("Stop evaluating lower-priority rules once this fires. Default true."),
			}, "name", "actions"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				body := map[string]any{"enabled": true, "stop_on_match": true}
				for _, k := range []string{"name", "match_type", "pattern", "scope", "chat_jids",
					"events", "actions", "priority", "cooldown_seconds", "stop_on_match"} {
					if v, ok := p[k]; ok {
						body[k] = v
					}
				}
				return result(t.c.CreateTrigger(ctx, body))
			}),

		tool("whatsapp_set_trigger_enabled",
			"Turn an automation rule on or off without deleting it.",
			object(map[string]any{
				"id":      intp("Trigger ID."),
				"enabled": boolp("True to enable, false to disable."),
			}, "id", "enabled"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.SetTriggerEnabled(ctx, int64(argInt(p, "id")), argBool(p, "enabled")))
			}),

		tool("whatsapp_delete_trigger",
			"Delete an automation rule permanently. To stop a rule temporarily without losing it, use whatsapp_set_trigger_enabled instead.",
			object(map[string]any{"id": intp("Trigger ID.")}, "id"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.DeleteTrigger(ctx, int64(argInt(p, "id"))))
			}),

		tool("whatsapp_test_trigger",
			"Dry-run a rule against a hypothetical message: reports whether it would match and what it "+
				"would do, without sending anything. Use this before enabling a rule.",
			object(map[string]any{
				"id":   intp("Trigger ID."),
				"chat": str("Chat to test against."),
				"text": str("Message text to test."),
			}, "id"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.TestTrigger(ctx, int64(argInt(p, "id")), arg(p, "chat"), arg(p, "text")))
			}),
	}
}

// WebhookTools manage event delivery to external endpoints.
func (t *Tools) WebhookTools() []ai.GoFunctionTool {
	return []ai.GoFunctionTool{
		tool("whatsapp_list_webhooks",
			"List webhook subscriptions: which endpoints receive which events.",
			object(map[string]any{}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				hooks, err := t.c.ListWebhooks(ctx)
				return result(map[string]any{"webhooks": hooks}, err)
			}),

		tool("whatsapp_create_webhook",
			"Subscribe an HTTP endpoint to WhatsApp events, so an external service is notified as things "+
				"happen instead of polling.",
			object(map[string]any{
				"url":    str("Endpoint to POST events to."),
				"secret": str("Shared secret; events are signed with it."),
				"events": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Event kinds to deliver, e.g. incoming_message, call.incoming.",
				},
				"scope": enum("Which chats to deliver.", "all", "dms", "groups", "list"),
				"chat_jids": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Chat JIDs when scope is 'list'.",
				},
				"include_mentions": boolp("Also deliver messages from outside the scope that @-mention this account."),
				"enabled":          boolp("Whether the subscription is live. Default true."),
			}, "url"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				body := map[string]any{"enabled": true}
				for _, k := range []string{"url", "secret", "events", "scope", "chat_jids", "include_mentions", "enabled"} {
					if v, ok := p[k]; ok {
						body[k] = v
					}
				}
				return result(t.c.CreateWebhook(ctx, body))
			}),

		tool("whatsapp_delete_webhook",
			"Remove a webhook subscription.",
			object(map[string]any{"id": intp("Webhook ID.")}, "id"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.DeleteWebhook(ctx, int64(argInt(p, "id"))))
			}),

		tool("whatsapp_test_webhook",
			"Fire a synthetic event at a webhook to check the endpoint is reachable and the signature "+
				"verifies, without waiting for a real WhatsApp event. Use it right after creating one.",
			object(map[string]any{"id": intp("Webhook ID.")}, "id"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.TestWebhook(ctx, int64(argInt(p, "id"))))
			}),

		tool("whatsapp_replay_webhook_delivery",
			"Re-send a recorded webhook delivery, unchanged. Use it when the consumer was down or broken "+
				"at the time and has since been fixed \u2014 the event is still on disk.",
			object(map[string]any{"id": intp("Delivery ID from whatsapp_webhook_deliveries.")}, "id"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.ReplayDelivery(ctx, int64(argInt(p, "id"))))
			}),

		tool("whatsapp_webhook_deliveries",
			"Inspect recent webhook delivery attempts, to debug an endpoint that is not receiving events.",
			object(map[string]any{"limit": intp("Maximum records. Default 50.")}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.WebhookDeliveries(ctx, argInt(p, "limit")))
			}),
	}
}

// SystemTools cover connection state and housekeeping.
func (t *Tools) SystemTools() []ai.GoFunctionTool {
	return []ai.GoFunctionTool{
		tool("whatsapp_status",
			"Check whether wacli is connected to WhatsApp, which account it is, and how much history it "+
				"has. Call this first if anything else fails unexpectedly.",
			object(map[string]any{}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.Status(ctx))
			}),

		tool("whatsapp_sync",
			"Pull recent history and contacts from WhatsApp into the local database.",
			object(map[string]any{}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.Sync(ctx))
			}),

		tool("whatsapp_set_dnd",
			"Turn automation on or off. Automation is gated behind DND mode, so tools that send messages "+
				"refuse to act while it is off — this is the master switch.",
			object(map[string]any{"enabled": boolp("True to allow automation.")}, "enabled"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				enabled, err := t.c.SetDND(ctx, argBool(p, "enabled"))
				return result(map[string]any{"ok": err == nil, "enabled": enabled}, err)
			}),

		tool("whatsapp_logs",
			"Read wacli's own log, to see what it did or why something failed.",
			object(map[string]any{
				"level":    str("Filter by level, e.g. error."),
				"category": str("Filter by category."),
				"query":    str("Filter by text."),
				"limit":    intp("Maximum entries. Default 100."),
			}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				limit := argInt(p, "limit")
				if limit == 0 {
					limit = 100
				}
				logs, err := t.c.ListLogs(ctx, client.LogQuery{
					Level: arg(p, "level"), Category: arg(p, "category"),
					Query: arg(p, "query"), Limit: limit,
				})
				return result(map[string]any{"logs": logs}, err)
			}),
	}
}
