# wacli AI Harness Reference

`wacli` exposes a local WhatsApp automation service for AI harnesses such as Codex or Claude Code.

The same `wacli` binary is both:

- the daemon
- the daemon client

This document is the operational contract for the harness.

## Purpose

The daemon keeps a local SQLite-backed mirror of:

- chats
- messages
- contacts
- contact memory fields
- webhook definitions
- auto-reply definitions
- chat lock state
- DND mode

It also exposes a localhost HTTP API and matching CLI commands so an AI harness can:

- inspect synced WhatsApp state
- resolve names, phone numbers, chat IDs, and JIDs
- send text and media
- bulk-send messages
- search messages by chat, sender, content, media-only, and time range
- download received media
- upload stories
- manage contact memory
- configure webhooks, triggers, and auto-replies
- place, answer, and end calls with synthesised or recorded audio
- manage groups and set typing/online presence

## Core Rules

The harness must respect these rules:

1. `DND mode` is the hard automation switch.
2. If `DND mode` is `false`, webhooks do not fire and automation sends are blocked.
3. A `locked` chat is off-limits for AI interaction.
4. Existing chats are locked during the first access-configuration step after login.
5. The human selects which existing chats the AI may interact with.
6. New chats discovered after that first configuration are automatically created as `unlocked`.
7. The harness should never assume sync is current unless it has checked `/status` or triggered `/sync`.

## Local Runtime

Default daemon address:

```text
http://127.0.0.1:8765
```

The daemon should normally be started by the user via:

```bash
systemctl --user enable --now wacli
```

Useful operator commands:

```bash
wacli login
wacli access configure
wacli sync
wacli status
systemctl --user restart wacli
journalctl --user -u wacli -f
```

Useful harness-facing commands:

```bash
wacli status
wacli resolve "Lyzn AI | Early access"
wacli messages --chat "Jio Phone" --limit 20
wacli contacts lookup "Jio Phone"
wacli contacts update --ref "Jio Phone" --memory "prefers short replies"
wacli send --to "Jio Phone" --text "hello"
wacli bulk-send --item '{"to":"Jio Phone","text":"hello"}'
wacli media download --chat "Jio Phone" --message-id <id>
wacli webhooks list
wacli auto-replies list
wacli api GET /status
```

## First-Time Human Setup

Expected one-time flow:

1. Human runs `wacli login`.
2. WhatsApp session is linked by QR or pairing code.
3. `wacli` performs an initial sync of chats, messages, and contacts.
4. Human is shown all synced chats as `LOCKED`.
5. Human chooses which existing chats the AI may access.
6. After that point, newly discovered chats auto-unlock.

If the bootstrap sync has not populated chats yet, access configuration should not be considered complete yet.

## Harness Startup Checklist

When the harness begins a session, it should:

1. Call `wacli status` or `GET /status`.
2. Confirm `connected=true`.
3. Confirm `dnd_mode=true` before attempting automation.
4. Read `initial_access_configured=true`.
5. If the local mirror may be stale, call `wacli sync` or `POST /sync`.
6. Use `wacli chats --filter unlocked` or `GET /chats?filter=unlocked` before planning actions.

## Recommended Harness Loop

Safe control loop:

1. Read `/status`.
2. If not connected, stop and ask the human to restart or log in again.
3. If `dnd_mode` is off, do not send messages and do not rely on webhooks.
4. Resolve the target from human terms first with `wacli resolve ...` or `GET /resolve`.
5. Read `/chats?filter=unlocked`.
6. Read `/messages?chat_ref=<name|phone|jid>&limit=<n>` for context.
7. Read or update contact memory with `wacli contacts lookup/update`.
8. Send via `/send` or `wacli send` only after confirming the chat is unlocked.

## CLI Contract

The harness should prefer the CLI when it is simpler than constructing HTTP.

Key commands:

- `wacli resolve [--kind any|chat|contact] [--limit N] [--allow-direct=true|false] <reference>`
- `wacli messages [--chat <ref>] [--sender <ref>] [--query <text>] [--media-only] [--from-me yes|no] [--before RFC3339] [--after RFC3339]`
- `wacli contacts list [--query <text>] [--limit N]`
- `wacli contacts lookup <reference>`
- `wacli contacts update --ref <reference> [--bio ...] [--notes ...] [--memory ...] [--metadata-json ...]`
- `wacli send --to <reference> [--text ...] [--media ...]`
- `wacli bulk-send --item '{"to":"...","text":"..."}'`
- `wacli bulk-send --items-file items.json`
- `wacli bulk-send --stdin-json`
- `wacli media download --chat <reference> --message-id <id>`
- `wacli webhooks list|add|remove|test|replay|deliveries`
- `wacli auto-replies list|add|remove`
- `wacli triggers list|add|enable|disable|remove|test`
- `wacli groups list|info|create|add|remove|promote|demote|rename|invite|join|leave`
- `wacli call [place] --to <reference> [--say <text> | --audio <file>] [--video]`
- `wacli call list|status|queue|answer|reject|end|capture|dump`
- `wacli check +15551234567,+15559876543`
- `wacli api <METHOD> </path> [json-body]`

Resolution rules:

- The harness may use human names, phone numbers, chat IDs, or raw JIDs.
- The harness should not assume it must remember JIDs.
- For ambiguous matches, the resolver returns multiple candidates and the harness should disambiguate before sending.

## Status API

### `GET /status`

Returns daemon and sync state.

Example response:

```json
{
  "connected": true,
  "user_jid": "1234567890@s.whatsapp.net",
  "dnd_mode": true,
  "initial_access_configured": true,
  "chat_count": 42,
  "message_count": 1823,
  "last_history_sync": "2026-04-12T13:14:15Z"
}
```

### `GET /config`

Returns the same snapshot as `GET /status`. It exists for callers that read it as configuration
rather than liveness; there is no separate config document.

### `GET /logs`

The daemon's own log records, newest first. Useful for explaining a failure that the API reported
only as an error string.

Query params:

- `limit=<int>` (default 200)
- `level=<level>`
- `category=<category>`
- `query=<search>`

Returns `{ "logs": [ ... ] }`.

## DND API

### `GET /dnd`

Returns:

```json
{ "enabled": true }
```

### `PUT /dnd`

Request:

```json
{ "enabled": true }
```

Important:

- Only when DND is on can the AI send messages through the API.
- Only when DND is on will configured webhooks be delivered.

## Sync API

### `POST /sync`

Requests an additional history sync and contact refresh.

Use this:

- after login
- after daemon restart
- before important planning if the mirror may be stale

Example response:

```json
{
  "ok": true,
  "history_seen": true,
  "history_requested": true,
  "requested_at": "2026-04-12T13:20:00Z",
  "last_history_sync": "2026-04-12T13:20:08Z"
}
```

Notes:

- `history_requested=false` means no local message anchor existed for an on-demand history request.
- That is expected on a brand-new account link before the bootstrap history arrives from the primary device.

## Resolve API

### `GET /resolve`

Query params:

- `ref=<name|phone|chat-id|jid>`
- `kind=any|chat|contact`
- `limit=<int>`
- `allow_direct=true|false`

This is the canonical lookup endpoint behind the CLI resolver.

## Chat APIs

### `GET /chats`

Query params:

- `filter=all|locked|unlocked|groups|dms`
- `limit=<int>`
- `query=<search>`

Example:

```bash
curl -s "http://127.0.0.1:8765/chats?filter=unlocked&limit=100"
```

Response shape:

```json
{
  "chats": [
    {
      "jid": "15551234567@s.whatsapp.net",
      "name": "Anjali",
      "is_group": false,
      "locked": false,
      "first_seen_at": "2026-04-10T09:00:00Z",
      "last_message_at": "2026-04-12T13:00:00Z",
      "last_message_preview": "Can you call me later?"
    }
  ]
}
```

### `PUT /chats/{jid}`

Sets lock state for a specific chat.

Request:

```json
{ "locked": true }
```

The harness may use this for safety workflows, but should not unlock sensitive chats on its own unless explicitly instructed.

### `PUT /chats/access`

Replaces the initial access selection set.

Request:

```json
{
  "unlocked_jids": [
    "15551234567@s.whatsapp.net",
    "120363000000000@g.us"
  ]
}
```

This locks all other currently known chats.

## Message APIs

### `GET /messages`

Query params:

- `chat_ref=<name|phone|chat-id|jid>` optional
- `chat_jid=<jid>` optional
- `sender_ref=<name|phone|jid>` optional
- `sender_jid=<jid>` optional
- `query=<search>` optional
- `limit=<int>`
- `media_only=true|false`
- `from_me=yes|no|true|false`
- `before=<RFC3339>` optional
- `after=<RFC3339>` optional

Examples:

```bash
curl -s "http://127.0.0.1:8765/messages?chat_ref=Anjali&limit=50"
curl -s "http://127.0.0.1:8765/messages?chat_ref=120363000000000@g.us&query=invoice&media_only=true"
curl -s "http://127.0.0.1:8765/messages?limit=100"
```

Returned messages include:

- message id
- chat JID
- sender JID
- text content
- timestamp
- media metadata when present
- resolved chat/sender metadata when references were provided
- direction
- message type
- media metadata when present

### Media download

`POST /media/download`

Request:

```json
{
  "message_id": "ABC123",
  "chat_ref": "Anjali"
}
```

Response:

```json
{
  "ok": true,
  "resolved_chat": {},
  "path": "/home/user/.wacli/media/15551234567_s.whatsapp.net/photo.jpg"
}
```

### `POST /messages/edit`

Edits a message already sent. Subject to the same gates as `/send`: refused while DND is off or the
chat is locked.

```json
{ "chat": "Anjali", "id": "ABC123", "text": "corrected text" }
```

Returns `{ "ok": true, "message": { ... } }`.

### `POST /messages/delete`

Revokes a message for everyone. `DELETE` is accepted too, in which case `chat` and `id` may be query
params instead of a body.

```json
{ "chat": "Anjali", "id": "ABC123" }
```

Returns `{ "ok": true, "deleted": "ABC123" }`.

> Note the field names. These two routes take `chat` and `id`, while `/media/download` above takes
> `chat_ref` and `message_id`. They are not interchangeable.

### `GET /messages/receipts`

Delivery and read receipts for one message.

Query params:

- `id=<message-id>` (required)

Returns `{ "message_id": "ABC123", "receipts": [ ... ] }`.

## Send APIs

### `POST /send`

Request:

```json
{
  "to": "15551234567@s.whatsapp.net",
  "text": "Hello from the harness",
  "media_path": "/absolute/path/to/file.png"
}
```

Rules:

- blocked if DND is off
- blocked if the target chat is locked
- `to` may be a human name, phone number, chat ID, or full JID
- text-only and media sends are both supported

### `POST /bulk_send`

Request:

```json
{
  "items": [
    {
      "to": "15551234567@s.whatsapp.net",
      "text": "First message"
    },
    {
      "to": "15557654321@s.whatsapp.net",
      "text": "Second message",
      "media_path": "/absolute/path/to/file.pdf"
    }
  ],
  "interval_ms": 1500
}
```

Use this carefully. The harness should:

- verify every target chat is unlocked first
- avoid using bulk send for uncertain or high-risk actions
- treat per-item failure independently

## Story API

### `POST /stories`

Request:

```json
{
  "text": "At work right now",
  "media_path": "/absolute/path/to/story.jpg"
}
```

Notes:

- DND must be on
- image and video stories are the intended use case

## Call APIs

Calls are placed and answered with audio rather than a live microphone: `say` is spoken via
text-to-speech, `audio` plays a file. Outbound calls are gated on DND like any other send.

### `GET /calls`

Lists calls. `?active=true` narrows to calls in progress.

Returns `{ "calls": [ ... ] }`.

### `POST /calls`

Places a call.

```json
{
  "to": "Anjali",
  "video": false,
  "ring_for_seconds": 30,
  "no_expire": false,
  "say": "Hello, this is an automated call.",
  "voice": "en-US",
  "audio": "/absolute/path/to/clip.wav",
  "repeat": false,
  "record": "/absolute/path/to/peer.wav"
}
```

Only `to` is required. `say` and `audio` are alternatives; `repeat` loops the audio while the call
is up, and `record` captures the peer's audio to that path. `no_expire` rings indefinitely and takes
precedence over `ring_for_seconds`.

Returns `{ "ok": true, "call": { ... } }`.

### `POST /calls/stream`

Places a call that carries a live two-way conversation instead of a fixed clip.

```json
{
  "to": "Anjali",
  "relay_url": "wss://relay.example.com/v1/call",
  "token": "<scoped session token>",
  "language": "hi-IN",
  "voice": "anushka",
  "ring_for_seconds": 45,
  "cached_lines": { "greeting": "<base64 s16le 16 kHz mono PCM>" }
}
```

`to`, `relay_url` and `token` are required. The relay is dialled *before* the call is offered, so
the speech and model streams are already warm when the peer answers.

Audio is bridged entirely inside the daemon: the peer's voice goes up the relay socket as 16 kHz
mono PCM and synthesised audio comes back down the same socket. Nothing about the audio is
reachable over this API — only the control events below.

`cached_lines` are pre-rendered clips keyed by id. `greeting` plays the moment the peer picks up,
and the relay can trigger any other id with a `play_cached` event. A cached line costs no network
round trip, which is the difference between a call that feels responsive and one that does not.
Hosts embedding the daemon should prefer the `AddCachedLine` binding over base64 here.

Unlike `POST /calls` this does not queue behind a call already in progress; it fails instead, on the
grounds that a scheduled conversation starting several minutes late is worse than one that reports
it could not start.

Returns `{ "ok": true, "call": { ... } }`.

Progress arrives as events (`GET /logs`, a webhook subscription, or the embedded event handler):

- `call_state` — `{ "call_id", "state" }` where state is `ringing`, `connected`, `listening`,
  `thinking`, `speaking` or `ended`
- `call_transcript` — `{ "call_id", "text", "final" }`, the peer's speech
- `call_ended` — `{ "call_id", "reason" }`, exactly once

### `GET /calls/status`

Query params:

- `ref=<call-id-or-chat-ref>` (`call_id` accepted as an alias)

Returns `{ "call": { ... }, "queue": { ... } }`. 404 when no such call is known.

### `GET /calls/queue`

Returns `{ "queue": { ... } }` — calls waiting to be placed. Calls are serialised, so this is what to
poll when a placement did not start immediately.

### `POST /calls/answer`

Answers a ringing inbound call with audio. Omit `call_id` to answer the current one.

```json
{ "call_id": "…", "say": "Leave a message after the tone", "voice": "en-US", "record": "/tmp/peer.wav" }
```

Returns `{ "ok": true, "call_id": "…" }`.

### `POST /calls/end`

```json
{ "call_id": "…", "reason": "done" }
```

Ends an active call. Returns `{ "ok": true, "call": { ... } }`.

### `POST /calls/reject`

```json
{ "call_id": "…" }
```

Rejects a ringing inbound call without answering it.

### `POST /calls/capture`

```json
{ "enabled": true }
```

Toggles recording of raw call signalling stanzas for debugging. Returns the file being written as
`path`. This is a diagnostic, not a call recorder — for peer audio use `record` on the call itself.

## Group APIs

### `GET /groups`

Lists joined groups. With `?ref=<group>` returns one group's details instead, as `{ "group": {...} }`.

### `POST /groups`

```json
{ "name": "Launch team", "participants": ["917569236628", "Anjali"] }
```

### `POST /groups/participants`

```json
{ "group": "Launch team", "action": "add", "participants": ["917569236628"] }
```

`action` is `add`, `remove`, `promote`, or `demote`. Returns `{ "ok": true, "participants": [ ... ] }`.

### `POST /groups/update`

```json
{ "group": "Launch team", "name": "New name" }
```

Provide exactly one of `name`, `topic`, or `leave: true` — they are checked in that order and the
first one present wins, so sending several silently ignores the rest.

### `GET /groups/invite`

Query params:

- `group=<ref>`
- `reset=true` to revoke the existing link and issue a new one

Returns `{ "invite_link": "https://chat.whatsapp.com/…" }`.

### `POST /groups/invite`

```json
{ "link": "https://chat.whatsapp.com/…", "preview": false }
```

Joins the group behind an invite link. With `preview: true` it only reads the group's details and
does not join — do that first if the link is untrusted.

## Presence API

### `POST /presence`

Two modes in one route. With `available` present it sets global online presence; otherwise it sets
per-chat typing state.

```json
{ "chat": "Anjali", "typing": true }
```

```json
{ "available": false }
```

`recording` is the voice-note equivalent of `typing`. Returns `{ "ok": true }`.

## Contact and Memory APIs

The contacts table is intended to hold both WhatsApp-derived identity info and AI memory.

### `GET /contacts`

Query params:

- `limit=<int>`
- `query=<search>`

### `GET /contacts/{jid}`

Returns a single contact record.

### `PUT /contacts/{jid}`

Updates AI memory fields for the contact.

Request body fields:

- `bio`
- `notes`
- `memory`
- `metadata_json`

Example:

```json
{
  "bio": "Founder at Lyzn",
  "notes": "Prefers direct answers. Usually replies at night.",
  "memory": "Interested in hiring flows and WhatsApp automation.",
  "metadata_json": "{\"timezone\":\"Asia/Kolkata\",\"priority\":\"high\"}"
}
```

Recommended usage:

- put concise stable facts in `bio`
- put relationship or style notes in `notes`
- put conversation memory or preferences in `memory`
- put structured machine data in `metadata_json`

### `GET /contacts/lookup`

Query params:

- `ref=<name|phone|jid>`

Returns both:

- `resolved`
- `contact`

### `PUT /contacts/update`

Request:

```json
{
  "ref": "Jio Phone",
  "memory": "Prefers short replies and quick follow-ups."
}
```

This is the preferred update route for harnesses because it resolves human references first.

### `POST /contacts/check`

Asks WhatsApp which of a list of phone numbers are registered. This is a network round trip, not a
local lookup, so batch the numbers rather than calling it per contact.

```json
{ "phones": ["917569236628", "15551234567"] }
```

Returns `{ "results": [ ... ] }`.

## Trigger APIs

Triggers are wacli's rule engine: match an event, run actions. They are policy that lives in the
daemon, as opposed to webhooks, which are transport out to something else.

### `GET /triggers`

Returns `{ "triggers": [ ... ], "events": [ ... ] }`, where `events` is the list of event names a
trigger may match — read it rather than guessing names.

### `POST /triggers`

Creates a trigger. The body is a trigger object; take its shape from `GET /triggers`.

Returns `{ "ok": true, "trigger": { ... } }`.

### `GET /triggers/{id}`

Returns `{ "trigger": { ... } }`.

### `PUT /triggers/{id}`

Replaces a trigger. The id in the path wins over any id in the body.

### `PATCH /triggers/{id}`

Enables or disables without rewriting the rule:

```json
{ "enabled": false }
```

### `DELETE /triggers/{id}`

Returns `{ "ok": true, "deleted": 3 }`.

### `POST /triggers/test`

Evaluates a trigger against a hypothetical message without sending anything.

```json
{ "id": 3, "chat": "Anjali", "text": "invoice attached" }
```

Returns `{ "ok": true, "result": { ... } }`. Use this before enabling a rule that sends.

## Webhook APIs

Webhooks are only active when DND is on.

### `GET /webhooks`

Lists configured webhooks.

### `POST /webhooks`

Request:

```json
{
  "url": "https://example.com/whatsapp-hook",
  "secret": "shared-secret",
  "events": [
    "incoming_message",
    "outgoing_message",
    "auto_reply_sent",
    "sync_complete",
    "connection_state"
  ],
  "scope": "selected_chats",
  "chat_refs": [
    "Jio Phone"
  ],
  "message_types": [
    "text",
    "image"
  ],
  "context_limit": 12,
  "enabled": true
}
```

Webhook scope rules:

- `scope=all_unlocked` means all currently unlocked chats and future unlocked chats.
- `scope=selected_chats` means only the named unlocked chats resolved from `chat_ref`, `chat_refs`, or `chat_jids`.
- Selected-chat webhook creation is rejected if any chosen chat is currently locked.
- For message events, locked chats never trigger delivery.

### `DELETE /webhooks/{id}`

Deletes a webhook.

### `GET /webhook_deliveries`

Query params:

- `limit=<int>`
- `status=pending|done|failed`
- `event=<event-name>`
- `query=<search>`

Returns persisted outbound HTTP webhook attempts, including request body, status, HTTP status, and response/error text.

### Webhook envelope

Delivered JSON:

```json
{
  "event": "incoming_message",
  "generated_at": "2026-04-12T13:30:00Z",
  "webhook": {
    "id": 7,
    "scope": "selected_chats",
    "chat_jids": [
      "15551234567@s.whatsapp.net"
    ],
    "message_types": [
      "text",
      "image"
    ],
    "context_limit": 12
  },
  "payload": {
    "chat": {},
    "message": {},
    "recent_messages": [],
    "chat_contact": {},
    "sender_contact": {},
    "message_kinds": [
      "message",
      "image",
      "media"
    ],
    "source": "whatsapp_event"
  }
}
```

Message-oriented webhook payloads include:

- resolved chat metadata
- the triggering message
- the previous few messages from the same chat
- contact memory for the chat when available
- contact memory for the sender when available
- normalized `message_kinds` to support downstream routing by content type

### Webhook signatures

If a webhook has a `secret`, the daemon signs every delivery:

```text
X-WACLI-Signature:    sha256=<hex>
X-WACLI-Signature-V1: v1=<hex>
X-WACLI-Timestamp:    <unix seconds>
```

`X-WACLI-Signature` is HMAC-SHA256 over the body alone and exists for consumers written against the
original scheme. `X-WACLI-Signature-V1` is HMAC-SHA256 over `<timestamp> + "." + <body>`; verify that
one and reject a timestamp too far from now, otherwise a captured request can be replayed forever.

### `POST /webhooks/test`

Delivers a synthetic event to one webhook so a consumer can be checked without waiting for real
traffic.

Request:

```json
{ "id": 3 }
```

Returns `{ "ok": true, "delivery": { ... } }`, where `ok` reflects whether the delivery reached the
consumer, not whether the request was accepted.

### `POST /webhook_deliveries/replay`

Re-sends a delivery that has already been recorded, by its delivery id. Same request and response
shape as `/webhooks/test`. Use it after fixing a consumer that rejected an event.

## Auto-Reply APIs

Auto-replies are only evaluated for unlocked chats while DND is on.

### `GET /auto_replies`

Lists rules.

### `POST /auto_replies`

Request:

```json
{
  "name": "default-away-reply",
  "match_type": "always",
  "reply_text": "Kartik is away right now. I am handling messages for him.",
  "enabled": true,
  "apply_to_dms": true,
  "apply_to_groups": false,
  "priority": 100
}
```

Supported `match_type` values:

- `always`
- `exact`
- `contains`
- `prefix`
- `suffix`
- `regex`

### `DELETE /auto_replies/{id}`

Deletes a rule.

## Failure Semantics

Common failure cases:

- `409` when DND is off and the harness tries to automate
- `403` when the target chat is locked
- `503` when WhatsApp is not connected
- `400` for bad request bodies or unsupported actions

The harness should treat these as policy or state errors, not as reasons to retry blindly.

## Safety Guidance For Harnesses

The harness should:

1. Always check lock state before sending.
2. Always check DND state before sending or expecting webhooks.
3. Prefer using contact memory fields for stable user facts instead of inventing state.
4. Avoid bulk sends unless explicitly requested.
5. Never assume a phone number or chat is safe to message unless it is unlocked and user-authorized.

The harness should not:

1. Unlock sensitive chats without explicit user intent.
2. Send to locked chats by trying alternate formats of the same JID.
3. Rely on webhooks when DND is off.
4. Assume sync is current after a daemon restart without checking `/status` or calling `/sync`.

## Minimal Example Session

```bash
curl -s http://127.0.0.1:8765/status
curl -s "http://127.0.0.1:8765/chats?filter=unlocked&limit=20"
curl -s "http://127.0.0.1:8765/messages?chat_jid=15551234567@s.whatsapp.net&limit=25"
curl -s -X PUT http://127.0.0.1:8765/dnd -H 'content-type: application/json' -d '{"enabled":true}'
curl -s -X POST http://127.0.0.1:8765/send -H 'content-type: application/json' -d '{"to":"15551234567@s.whatsapp.net","text":"hello"}'
```

## File Locations

Default local state:

```text
~/.wacli/state.db
~/.wacli/session.db
~/.wacli/media/
```

Reference service file:

```text
/home/mellob/code/wacli/wacli.service
```
