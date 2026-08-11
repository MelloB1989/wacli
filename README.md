# wacli

**A local-first WhatsApp automation daemon and CLI.** `wacli` logs into WhatsApp
once (via QR or pairing code), keeps a persistent connection, and exposes your
account to scripts, AI agents, and desktop tooling through a clean CLI, a local
HTTP API, and signed webhooks — all running on your own machine, with your data
in a local SQLite database.

Built on [whatsmeow](https://github.com/tulir/whatsmeow). No cloud, no third
party sees your messages.

> ⚠️ **Automating a personal WhatsApp account is against WhatsApp's Terms of
> Service and can get the number banned.** `wacli` is intended for personal
> automation, research, and building AI assistants on numbers you control and
> accept that risk for. Use responsibly. See [Security & safety](#security--safety).

---

## Why wacli

- **One binary, three roles** — the same `wacli` is the daemon, the daemon
  client (every CLI command talks to it over `127.0.0.1:8765`), and the host for
  inbound bridges.
- **Local & private** — WhatsApp session and message history live in
  `~/.wacli` (SQLite). Nothing leaves your machine unless you wire up a webhook.
- **Scriptable** — send, edit, delete, search messages, manage contacts,
  download media, post stories, all from the shell or JSON.
- **Event-driven** — register webhooks (with HMAC signatures) that fire on
  incoming/outgoing messages, scoped to the exact chats you choose.
- **AI-agent ready** — a stable JSON contract designed for harnesses like Claude
  Code or Codex to read/act on WhatsApp. It powers
  [KARMAX](https://github.com/MelloB1989/KARMAX), a personal AI assistant.
- **Access control** — lock chats so automation can never touch them; a DND
  switch gates all outbound sends.
- **Interfaces for humans** — a Bubble Tea terminal UI and an optional Wails
  desktop app for QR login and configuration.

## Install

Requires **Go 1.25+**. The tree is **CGO-free** (SQLite via the pure-Go
`modernc.org/sqlite`), so it cross-compiles to any target without a C toolchain
— including Android and iOS, which is what makes the
[Expo module](expo-wacli/) possible.

```bash
go install github.com/MelloB1989/wacli@latest
```

Or build from source:

```bash
git clone https://github.com/MelloB1989/wacli
cd wacli
go build -o wacli .
```

## Quickstart

```bash
# 1. Log in (shows a QR to scan from WhatsApp → Linked Devices)
wacli login

# 2. Run the daemon (keeps the connection alive; serves the local API on :8765)
wacli daemon

# 3. In another shell — arm outbound sends, then send a message
wacli dnd on
wacli send --to "+15551234567" --text "hello from wacli"

# 4. Read recent messages from a chat
wacli messages --chat "+15551234567" --limit 15
```

Run `wacli` with no arguments for the full command list, or see the
[CLI reference](docs/cli-reference.md).

## Command surface

| Area | Commands |
|------|----------|
| Session | `login`, `daemon`, `sync`, `status`, `dnd` |
| Messaging | `send`, `bulk-send`, `edit`, `delete`, `receipts`, `story` |
| Reading | `chats`, `messages`, `resolve`, `media download` |
| Contacts | `contacts` (list / lookup / update) |
| Access control | `access` (lock / unlock / list / configure) |
| Automation | `webhooks`, `openclaw`, `auto-replies` |
| Interfaces | `tui` (terminal UI), `api` (generic local API client) |

## The local HTTP API

The daemon serves an authenticated JSON API on `http://127.0.0.1:8765` — the
same surface the CLI uses. Highlights: `GET /status`, `GET/PUT /dnd`,
`POST /sync`, `GET /chats`, `GET /resolve`, `PUT /chats/access`, and the
assistant settings + webhook/bridge CRUD endpoints. See the
[AI harness reference](docs/ai-harness-reference.md) for the full contract that
agents consume.

## Webhooks

Register a webhook scoped to specific chats; `wacli` POSTs a JSON envelope to
your URL on each qualifying event, signed with `X-WACLI-Signature: sha256=<hmac>`
(HMAC-SHA256 of the body with your secret):

```bash
wacli webhooks add \
  --url https://example.com/hook \
  --events incoming_message,outgoing_message \
  --scope selected_chats \
  --chat "+15551234567" \
  --secret "$(openssl rand -hex 24)"
```

Each delivery tells the consumer whether the bot was **directly addressed** —
`mentions_me` (the account was @-mentioned) and `quoted_is_from_me` (a reply to a
message the account sent) — computed from the account's own identity, so
consumers need no configured numbers. See the
[CLI reference](docs/cli-reference.md#webhooks) for the payload shape.

## Desktop app

`desktop/` is an optional [Wails](https://wails.io) control plane for QR login,
status/sync/DND, chat access management, and webhook/bridge configuration. See
the [desktop UI reference](docs/desktop-ui-reference.md).

## Architecture

```
wacli (single Go binary, package main)
├── daemon        long-running whatsmeow client + local HTTP API (:8765)
├── client        every CLI subcommand → HTTP call to the daemon
├── store         SQLite persistence (session, chats, messages, contacts,
│                 webhooks, bridges) in ~/.wacli
├── webhooks      HMAC-signed, chat-scoped event delivery
├── openclaw      inbound bridge host (route chats into external harnesses)
└── tui           Bubble Tea operations console

internal/daemonclient   typed Go client for the daemon API (reusable)
desktop/                Wails desktop app (separate Go module)
```

State directory: `~/.wacli` (override with `WACLI_HOME`).

## Security & safety

- The WhatsApp session, keys, and full message history are stored **unencrypted
  at rest** in `~/.wacli`. Protect that directory like any credential store.
- Outbound sends are gated by a **DND switch** (`wacli dnd on|off`) — off by
  default so automation can't message anyone until you explicitly arm it.
- **Lock** any chat (`wacli access lock`) to make it permanently off-limits to
  automation.
- Webhooks are HMAC-signed; always verify `X-WACLI-Signature` on your endpoint.
- Never commit `~/.wacli`, `.env`, or any `*.db`/`*.session` file. See
  [`.gitignore`](.gitignore) and [SECURITY.md](SECURITY.md).

## Documentation

- [CLI reference](docs/cli-reference.md) — every command and flag
- [AI harness reference](docs/ai-harness-reference.md) — the JSON contract for agents
- [Desktop UI reference](docs/desktop-ui-reference.md) — the Wails app

## Contributing

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). Please run
`go build ./... && go vet ./... && gofmt -l .` before opening a PR.

## License

[MIT](LICENSE) © MelloB1989
