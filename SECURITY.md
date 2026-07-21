# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue for security vulnerabilities. Instead,
report privately via GitHub's
[Report a vulnerability](https://github.com/MelloB1989/wacli/security/advisories/new)
(Security → Advisories) so it can be triaged before disclosure.

Please include reproduction steps and the affected version/commit. You'll get an
acknowledgement as soon as possible.

## What wacli stores, and where

wacli keeps its state in `~/.wacli` (override with `WACLI_HOME`):

- the WhatsApp **session keys** (device credentials),
- the **full message history** and contacts, in a local SQLite database,
- registered **webhook secrets**.

**This data is stored unencrypted at rest.** Treat `~/.wacli` as a secret store:
restrict its permissions, don't back it up to untrusted locations, and never
commit it. The repository `.gitignore` already excludes `.env`, `*.db`,
`*.session`, and the built binary.

## Operational safety controls

- **DND switch** — outbound sends are disabled unless `wacli dnd on` is set; it
  does not persist across daemon restarts, so automation starts "muted."
- **Chat locking** — `wacli access lock <chat>` makes a chat permanently
  off-limits to automation.
- **Webhook signing** — deliveries carry `X-WACLI-Signature: sha256=<hmac>`
  (HMAC-SHA256 of the raw body with your secret). Always verify it on your
  endpoint and reject unsigned/mismatched requests.

## Scope note

Automating a personal WhatsApp account may violate WhatsApp's Terms of Service
and can result in the number being banned. This is a usage risk, not a software
vulnerability — please don't report it as one.
