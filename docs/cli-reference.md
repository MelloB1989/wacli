# wacli CLI Reference

`wacli` is both:

- the WhatsApp daemon
- the daemon client
- the OpenClaw inbound bridge host

Most commands below talk to the local daemon at `http://127.0.0.1:8765`.

## Core Rules

- `DND` must be `on` for automation sends, stories, webhooks, and auto-replies.
- Locked chats cannot be automated.
- Existing chats start locked after first login.
- New chats discovered later are unlocked automatically.

## Startup

Login and first-time setup:

```bash
wacli login
wacli access configure
```

Run the daemon:

```bash
wacli daemon
```

Open the terminal UI:

```bash
wacli tui
```

Common status commands:

```bash
wacli status
wacli sync
wacli dnd
wacli dnd on
wacli dnd off
```

## Chat Listing And Access Control

List chats:

```bash
wacli chats
wacli chats --filter unlocked
wacli chats --filter groups --query Lyzn
```

Interactive access manager:

```bash
wacli access configure
```

Paged access inspection:

```bash
wacli access list --filter locked --page 1 --page-size 25
wacli access list --filter groups --query startup
```

Lock or unlock by name, phone, chat ID, or JID:

```bash
wacli access lock "Jio Phone"
wacli access unlock "Lyzn AI | Early access"
wacli access unlock 917569236628
wacli access lock 120363423215207538@g.us
```

## Resolver

Resolve a human reference into chats or contacts:

```bash
wacli resolve "Jio Phone"
wacli resolve --kind chat "Lyzn AI | Early access"
wacli resolve --kind contact --limit 5 "917569236628"
```

Flags:

- `--kind any|chat|contact`
- `--limit N`
- `--allow-direct=true|false`

## Message Query

Query messages in bulk or within one chat:

```bash
wacli messages --limit 50
wacli messages --chat "Jio Phone" --limit 20
wacli messages --chat "Lyzn AI | Early access" --query invoice
wacli messages --chat "Lyzn AI | Early access" --media-only
wacli messages --sender "Jio Phone" --from-me no
wacli messages --chat "Jio Phone" --after 2026-04-12T00:00:00Z
```

Flags:

- `--chat <reference>`
- `--sender <reference>`
- `--query <text>`
- `--limit N`
- `--media-only`
- `--from-me yes|no|true|false`
- `--before <RFC3339>`
- `--after <RFC3339>`

## Contacts And Memory

List contacts:

```bash
wacli contacts list
wacli contacts list --query kartik
wacli contacts list --limit 500
```

Lookup a contact by human reference:

```bash
wacli contacts lookup "Jio Phone"
wacli contacts lookup "917569236628"
```

Update AI memory fields:

```bash
wacli contacts update --ref "Jio Phone" --bio "Founder"
wacli contacts update --ref "Jio Phone" --notes "Prefers direct replies"
wacli contacts update --ref "Jio Phone" --memory "Interested in hiring and AI ops"
wacli contacts update --ref "Jio Phone" --metadata-json '{"priority":"high"}'
```

Flags:

- `--ref <reference>`
- `--bio <text>`
- `--notes <text>`
- `--memory <text>`
- `--metadata-json <json-string>`

## Sending Messages

Send text:

```bash
wacli send --to "Jio Phone" --text "hello"
```

Send text with media:

```bash
wacli send --to "Jio Phone" --text "see attached" --media /absolute/path/file.png
```

Notes:

- `--to` accepts name, phone, chat ID, or JID.
- Sends fail when `DND` is off.
- Sends fail when the chat is locked.

## Bulk Send

One inline item:

```bash
wacli bulk-send --item '{"to":"Jio Phone","text":"hello"}'
```

Multiple inline items:

```bash
wacli bulk-send \
  --item '{"to":"Jio Phone","text":"hello"}' \
  --item '{"to":"Lyzn AI | Early access","text":"update","media_path":"/tmp/report.pdf"}'
```

From a file:

```bash
wacli bulk-send --items-file items.json --interval-ms 1500
```

From stdin:

```bash
cat items.json | wacli bulk-send --stdin-json
```

Accepted item fields:

- `to`
- `text`
- `message`
- `media_path`

## Stories

Text story:

```bash
wacli story --text "At work"
```

Media story:

```bash
wacli story --text "Shipping today" --media /absolute/path/story.jpg
```

## Media Download

Download media from a stored message:

```bash
wacli media download --chat "Jio Phone" --message-id ABC123
```

The returned file path will point into `~/.wacli/media/`.

## Webhooks

List:

```bash
wacli webhooks list
```

Add:

```bash
wacli webhooks add --url https://example.com/hook
wacli webhooks add --url https://example.com/hook --secret secret123 --events incoming_message,outgoing_message,sync_complete
wacli webhooks add --url https://example.com/hook --chat "Jio Phone" --events incoming_message --message-types text,image
wacli webhooks add --url https://example.com/hook --scope all_unlocked --events incoming_message --message-types image,video --context-limit 20
```

Remove:

```bash
wacli webhooks remove 1
```

Inspect delivery logs:

```bash
wacli webhooks logs
wacli webhooks logs --status failed
wacli webhooks logs --event incoming_message --limit 50
wacli webhooks logs --query hooks/wacli
```

Notes:

- Webhooks only fire while `DND` is on.
- For message events, webhooks only fire for unlocked chats.
- `--chat` can be repeated to subscribe specific unlocked chats by name, phone, chat ID, or JID.
- `--scope all_unlocked` subscribes every currently unlocked chat and any future unlocked chats.
- `--message-types` filters by content kind. Supported values include `message`, `text`, `image`, `video`, `document`, `audio`, `sticker`, and `media`.
- Message webhooks include chat data, contact data, message kind tags, and recent message context.
- `--context-limit` controls how many recent messages are attached in `recent_messages`.
- Delivery attempts are persisted with request payload, status, HTTP status, and response/error text.
- Successful and failed attempts are also written to the daemon journal.

## OpenClaw Bridge

The OpenClaw bridge is separate from HTTP webhooks.

When an inbound message arrives in an unlocked chat while `DND` is on, `wacli` can:

- look up or create a stable OpenClaw session UUID for that WhatsApp chat
- dedupe by message id
- invoke:

```bash
openclaw agent --session-id <uuid> --message "<instruction + inbound event json>" --json
```

List bridges:

```bash
wacli openclaw list
```

Add a bridge for all unlocked chats:

```bash
wacli openclaw add --scope all_unlocked --message-types text,image,video
```

Add a bridge for one specific unlocked chat:

```bash
wacli openclaw add --chat "Jio Phone" --message-types text,image --instruction "Handle this WhatsApp conversation carefully."
```

Use a custom executable path:

```bash
wacli openclaw add --command /absolute/path/openclaw --scope all_unlocked
```

Update the instruction later:

```bash
wacli openclaw update 3 --instruction "New bridge instruction"
wacli openclaw update 3 --instruction-file /absolute/path/openclaw_instruction.txt
```

List stable session mappings:

```bash
wacli openclaw sessions
wacli openclaw sessions --query Jio
```

Inspect bridge delivery logs:

```bash
wacli openclaw deliveries
wacli openclaw deliveries --status failed
wacli openclaw deliveries --query Jio
```

Remove a bridge:

```bash
wacli openclaw remove 3
```

Notes:

- Selected-chat bridges only accept currently unlocked chats.
- `--chat` can be repeated.
- `--message-types` uses the same content vocabulary as message webhooks.
- The OpenClaw prompt includes the same inbound payload used for message webhooks, plus the configured instruction text.
- The actual session mapping is stable per WhatsApp chat, so DMs and groups keep separate continuity in OpenClaw.
- Bridge delivery logs persist command, session id, status, request message, captured output, and error text.

## Auto-Replies

List:

```bash
wacli auto-replies list
```

Add:

```bash
wacli auto-replies add --name away --match-type always --reply-text "Away right now"
wacli auto-replies add --name pricing --match-type contains --pattern pricing --reply-text "I will share pricing shortly"
```

Remove:

```bash
wacli auto-replies remove 1
```

Flags:

- `--name <rule-name>`
- `--match-type always|exact|contains|prefix|suffix|regex`
- `--pattern <text>`
- `--reply-text <text>`
- `--media <path>`
- `--disabled`
- `--dms`
- `--groups`
- `--priority N`

## Generic API Client

For daemon routes not covered by a higher-level command:

```bash
wacli api GET /status
wacli api POST /sync
wacli api GET '/messages?chat_ref=Jio%20Phone&limit=10'
wacli api PUT /dnd '{"enabled":true}'
wacli api POST /send '{"to":"Jio Phone","text":"hello"}'
```

Optional body sources:

```bash
wacli api POST /bulk_send --body-file items.json
cat body.json | wacli api POST /webhooks --stdin-json
```

## Systemd

If installed as a user service:

```bash
systemctl --user restart wacli
systemctl --user status wacli
journalctl --user -u wacli -f
```

## Files

Default local storage:

```text
~/.wacli/state.db
~/.wacli/session.db
~/.wacli/media/
```
