# expo-wacli

Run [`wacli`](../) — a local-first WhatsApp automation daemon — **inside** a React Native Expo app,
on iOS and Android. The Go daemon is compiled into the app as a native library and called directly;
there is no server, no socket, and nothing leaves the device.

> ⚠️ Automating a personal WhatsApp account is against WhatsApp's Terms of Service and can get the
> number banned. See the [main README](../README.md#security--safety).

---

## How it works

`wacli` is a Go program that keeps a WebSocket open to WhatsApp and stores everything in local
SQLite. Three things make it embeddable:

1. **The tree is CGO-free.** SQLite is `modernc.org/sqlite`, a pure-Go translation, so the whole
   dependency graph cross-compiles to `android/arm64` and `ios/arm64` without a C toolchain.
2. **[`mobile/`](../mobile) is a gomobile binding surface** — a narrow, string-in/string-out API
   that `gomobile bind` turns into an Android AAR and an iOS XCFramework.
3. **The API is dispatched in-process.** The daemon normally serves JSON on `127.0.0.1:8765`;
   embedded, `Request()` calls the same `http.Handler` directly. No port is bound, so there is no
   local-network permission prompt on iOS, no port collision, and nothing outside the app process
   can reach the API.

```
JS  ──▶ expo-wacli (TS)
          ├── Android: WacliModule.kt ──▶ wacli.aar        ──▶ Go: mobile ──▶ wa (whatsmeow + SQLite)
          └── iOS:     WacliModule.swift ─▶ Mobile.xcframework ─▶ Go: mobile ──▶ wa
```

## What works where

|                            | Android | iOS |
|----------------------------|---------|-----|
| Runs in-app                | ✅       | ✅   |
| Live messages, foreground  | ✅       | ✅   |
| Live messages, background  | ✅ (foreground service) | ❌ |
| Survives app being killed  | ⚠️ OEM-dependent | ❌ |

**Read this before choosing iOS.** iOS gives a backgrounded app roughly thirty seconds and then
freezes it. There is no entitlement that keeps a socket open, and no declarable background mode
covers "maintain a WhatsApp connection" — so the module shuts the service down cleanly when the app
backgrounds and restarts it on return. Nothing is lost: messages that arrived meanwhile are fetched
on the next sync. But they do not arrive live and no notification fires for them. If your product
needs live delivery on iOS, the daemon has to run off-device and push to the app.

On Android a foreground service keeps it alive, at the cost of a permanent notification. Doze and
the OEM task-killers (Xiaomi, Oppo, Samsung) will still kill the process on their own schedule; the
connection watchdog reconnects, and Android 14's `dataSync` service type carries a daily runtime
budget you should measure against your usage.

## Install

The module lives in this repo. Point your app at it:

```json
{
  "dependencies": {
    "expo-wacli": "file:../wacli/expo-wacli"
  }
}
```

Then build the native bindings — they are gitignored build artifacts, not source:

```bash
cd wacli
./expo-wacli/scripts/build-bindings.sh          # both, if this machine can
./expo-wacli/scripts/build-bindings.sh android  # needs ANDROID_NDK_HOME
./expo-wacli/scripts/build-bindings.sh ios      # needs macOS + Xcode
```

Add the plugin to `app.json`:

```json
{
  "expo": {
    "plugins": [["expo-wacli", { "abiFilters": ["arm64-v8a"] }]]
  }
}
```

Then `npx expo prebuild` and run on a **dev client** — this is native code, so it cannot run in
Expo Go.

### Size

The Go runtime plus whatsmeow is ~20 MB per architecture. `abiFilters` defaults to
`['arm64-v8a', 'x86_64']`; narrow it to `['arm64-v8a']` for release builds, or use ABI splits, so
users download one slice rather than four.

## Demo app

[`example/`](example/) is a working app that links an account, lists chats, opens a conversation,
sends, and updates live — the fastest way to see the whole thing working:

```bash
./scripts/build-bindings.sh
cd example && npm install && npx expo prebuild --clean && npx expo run:android
```

## Usage

```tsx
import { useEffect, useState } from 'react';
import * as Wacli from 'expo-wacli';

export default function App() {
  const [qr, setQr] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      if (await Wacli.isPaired()) {
        await Wacli.start();
      } else {
        // On the phone that also runs WhatsApp there is nothing to scan a QR with, so prefer
        // pairing by code.
        await Wacli.loginWithPhone('+15551234567');
      }
    })();

    const qrSub = Wacli.addListener('onLoginQRCode', ({ code }) => setQr(code));
    const codeSub = Wacli.addListener('onLoginPairingCode', ({ code }) =>
      alert(`Enter this in WhatsApp → Linked Devices: ${code}`)
    );
    const eventSub = Wacli.addListener('onEvent', ({ event, payload }) => {
      if (event === 'incoming_message') {
        console.log('message from', payload.message?.sender_jid, payload.message?.content);
      }
    });

    return () => {
      qrSub.remove();
      codeSub.remove();
      eventSub.remove();
    };
  }, []);

  return null;
}
```

Sending requires arming the DND switch first — it is wacli's hard automation gate and is off by
default, so nothing can message anyone until you turn it on:

```ts
await Wacli.setDND(true);
await Wacli.sendMessage({ to: '+15551234567', text: 'hello from Expo' });
```

Reading:

```ts
const chats = await Wacli.listChats({ limit: 50 });
const messages = await Wacli.listMessages({ chat: '+15551234567', limit: 30 });

// Media downloads into the app sandbox and comes back as a path you can render directly.
const path = await Wacli.downloadMedia(chat.jid, message.id);
<Image source={{ uri: 'file://' + path }} />
```

Anything the typed helpers do not cover is one `request` away — the full route list is in
[`docs/ai-harness-reference.md`](../docs/ai-harness-reference.md):

```ts
const groups = await Wacli.request('GET', '/groups');
await Wacli.request('POST', '/triggers', { name: 'auto-ack', /* ... */ });
```

## API

| Function | Notes |
|----------|-------|
| `isPaired()` | Whether a session exists. Decides login vs. resume. Safe before `start()`. |
| `start()` / `stop()` | Connect and disconnect. `start` also starts the Android foreground service. |
| `isRunning()` / `isConnected()` | Started, versus socket actually up. |
| `loginWithPhone(phone)` | Pairing-code login. Preferred on mobile. |
| `loginWithQR()` | QR login, for scanning from another device. |
| `cancelLogin()` / `logout()` | Abort an attempt; erase the session (history is kept). |
| `getStatus()` / `sync()` | Snapshot; pull fresh state from WhatsApp. |
| `getDND()` / `setDND(on)` | The automation gate. Off means sends are refused. |
| `listChats()` / `listMessages()` | Local mirror, no network. |
| `sendMessage()` / `editMessage()` / `deleteMessage()` | |
| `downloadMedia()` | Returns a sandbox path. |
| `setChatLocked(jid, locked)` | Locked chats are permanently off-limits to automation. |
| `request(method, path, body)` | Escape hatch to any route. |
| `addListener(event, cb)` | `onEvent`, `onLoginQRCode`, `onLoginPairingCode`, `onLoginStatus`, `onLoginError`. |

## Security

The session keys and full message history sit unencrypted in the app's private directory
(`filesDir` on Android, Application Support on iOS). The app sandbox is what protects them, so:

- Never move the state directory to external storage or a shared container.
- On a rooted or jailbroken device, treat them as readable.
- Each install consumes one of WhatsApp's ~4 linked-device slots.

The in-process API has no authentication, exactly as the daemon's loopback API does not. That is
safe *because* nothing binds a port — do not add one.
