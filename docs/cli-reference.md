# wacli CLI Reference

`wacli` is both:

- the WhatsApp daemon
- the daemon client

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
wacli version
wacli status
wacli sync
wacli dnd
wacli dnd on
wacli dnd off
```

`wacli status` reports the daemon's view when one is running and falls back to reading the local
database directly when none answers, so it is also the way to check state with the daemon down.

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

## Editing And Deleting Messages

Edit a message already sent:

```bash
wacli edit --chat "Jio Phone" --id ABC123 --text "corrected text"
```

Delete (revoke for everyone):

```bash
wacli delete --chat "Jio Phone" --id ABC123
```

Inspect delivery and read receipts:

```bash
wacli receipts --id ABC123
```

Edit and delete are sends as far as the safety gates are concerned: both are refused while `DND` is
off or the chat is locked.

## Calls

Calls carry audio rather than a live microphone: `--say` is spoken with text-to-speech, `--audio`
plays a file. Placing a call is gated on `DND` like any other send.

Place:

```bash
wacli call --to "Jio Phone" --say "Your order has shipped."
wacli call --to 917569236628 --audio /absolute/path/notice.wav --repeat
wacli call --to "Jio Phone" --video --ring-for 60
```

Place options:

- `--say <text>` / `--voice <voice>` — speak on answer (`say -v '?'` lists voices)
- `--audio <file>` — play a `.wav`/`.mp3`/`.opus` instead of `--say`
- `--repeat` — loop the audio instead of hanging up when it ends
- `--record <file.wav>` — write the other party's voice to a file
- `--ring-for <seconds>` (default 45) or `--no-expire` to ring until ended
- `--video`

Inspect:

```bash
wacli call list
wacli call list --active
wacli call status
wacli call queue
```

Calls are placed one at a time, so `queue` is what to check when a placement did not start
immediately.

Answer, reject, end:

```bash
wacli call answer --say "Please leave a message." --record /tmp/peer.wav
wacli call reject --id <call-id>
wacli call end --id <call-id> --reason "done"
```

`--id` defaults to the only ringing call, so it can be omitted when just one is in flight.

Signalling capture, for debugging call setup:

```bash
wacli call capture
wacli call capture --off
wacli call dump --last 50
```

This records raw signalling stanzas, not audio — use `--record` on the call itself for that.

## Groups

List and inspect:

```bash
wacli groups list
wacli groups info "Lyzn AI | Early access"
```

Create:

```bash
wacli groups create --name "Launch team" --members "917569236628,Anjali"
```

Membership. `--members` accepts phones, JIDs, or contact names:

```bash
wacli groups add --group "Launch team" --members 917569236628
wacli groups remove --group "Launch team" --members 917569236628
wacli groups promote --group "Launch team" --members Anjali
wacli groups demote --group "Launch team" --members Anjali
```

Rename or set the topic:

```bash
wacli groups rename --group "Launch team" --name "Launch crew"
wacli groups rename --group "Launch team" --topic "Ship by Friday"
```

Invite links:

```bash
wacli groups invite --group "Launch team"
wacli groups invite --group "Launch team" --reset
wacli groups join --link https://chat.whatsapp.com/… --preview
wacli groups join --link https://chat.whatsapp.com/…
```

`--preview` reads the group behind a link without joining it. Do that first for an untrusted link.
`--reset` revokes the previous link.

Leave:

```bash
wacli groups leave --group "Launch team"
```

## Number Check

Ask WhatsApp which numbers are registered. This is a network round trip, so pass them together
rather than one at a time:

```bash
wacli check +15551234567,+15559876543
```

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

## Triggers

Triggers are the rule engine: match an inbound event, run actions. They live in the daemon and are
evaluated only for unlocked chats while `DND` is on.

List:

```bash
wacli triggers list
```

Add:

```bash
wacli triggers add --name "invoice" --match contains --pattern invoice --reply "Got it, forwarding to accounts."
wacli triggers add --name "standup" --match regex --pattern '^standup' --scope groups --react 👍
wacli triggers add --name "urgent" --match contains --pattern urgent --webhook https://example.com/hook
```

Matching:

- `--match` is `always|contains|exact|prefix|suffix|regex` (default `contains`)
- `--scope` is `all|dms|groups|list` (default `all`); with `list`, pass `--chats a,b,c`
- `--events` is a comma-separated list of event kinds, defaulting to `incoming_message`

Actions, any combination:

- `--reply <text>`
- `--media <file>`
- `--react <emoji>`
- `--forward <chat>`
- `--webhook <url>`
- `--mark-read`

Ordering:

- `--priority <int>` evaluates lowest first (default 100)
- `--cooldown <seconds>` throttles re-firing per chat
- `--continue` lets lower-priority rules run as well; otherwise the first match wins

Enable, disable, remove:

```bash
wacli triggers enable 3
wacli triggers disable 3
wacli triggers remove 3
```

Test a rule against a hypothetical message without sending anything:

```bash
wacli triggers test --id 3 --chat "Jio Phone" --text "invoice attached"
```

Do this before enabling any rule whose action sends.


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
