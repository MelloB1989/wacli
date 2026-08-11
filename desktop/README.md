# WACLI Desktop

WACLI Desktop is the Wails-based control plane for the local `wacli` daemon.

It provides:

- WhatsApp login orchestration with QR rendering
- daemon status and sync controls
- DND controls
- chat lock / unlock management
- webhook management

## Development

From the desktop app root:

```bash
~/go/bin/wails dev
```

`wails.json` includes `build:tags=webkit2_41` so current Arch / Garuda systems can use the installed `webkit2gtk-4.1` package.

The local daemon should already be running:

```bash
cd /home/mellob/code/wacli
./wacli daemon
```

## Linux Dependency

Wails requires WebKitGTK on Linux.

On current Arch / Garuda, install the 4.1 package if it is missing:

```bash
sudo pacman -S webkit2gtk-4.1
```

If you remove the `webkit2_41` build tag, Wails falls back to the older 4.0 package:

```bash
sudo pacman -S webkit2gtk
```
