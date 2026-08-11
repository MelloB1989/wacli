# WACLI Desktop UI Reference

`desktop/` is a Wails desktop control plane for the local `wacli` daemon.

It is intended for:

- first-time WhatsApp login with QR visibility inside the app
- status, sync, and DND control
- locked / unlocked chat management
- webhook management
- local app log inspection

## Requirements

On Linux, Wails needs `webkit2gtk`.

For Arch / Garuda:

```bash
sudo pacman -S webkit2gtk
```

Wails CLI install:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

## Project Layout

- Desktop app root: `desktop/`
- Frontend: `desktop/frontend/`
- Wails config: `desktop/wails.json`

## Run In Dev Mode

Start the daemon first:

```bash
cd /home/mellob/code/wacli
./wacli daemon
```

Then run the desktop app:

```bash
cd /home/mellob/code/wacli/desktop
~/go/bin/wails dev
```

## Build

Frontend build only:

```bash
cd /home/mellob/code/wacli/desktop/frontend
npm install
npm run build
```

Desktop Go build:

```bash
cd /home/mellob/code/wacli/desktop
go build ./...
```

Full desktop package:

```bash
cd /home/mellob/code/wacli/desktop
~/go/bin/wails build
```

## Login Flow

The desktop app starts `wacli login --skip-access-config` in a subprocess.

That means:

- QR output still uses the existing `wacli` login path
- the app reads `~/.wacli/qr.png` and shows it in the UI
- access configuration is handled in the chat management screen after login

## Current Desktop Screens

- `Dashboard`
- `Chats`
- `Logs`
- `Automation`

## Runtime Notes

- Automation is webhooks; the rule engine behind `wacli triggers` has no desktop screen yet.
- `DND` still remains the hard automation gate.
