import type { EventSubscription } from 'expo-modules-core';

import WacliModule from './WacliModule';
import type {
  ChatRecord,
  ListChatsOptions,
  ListMessagesOptions,
  MessageRecord,
  SendMessageOptions,
  StatusSnapshot,
  WacliModuleEvents,
} from './Wacli.types';

export * from './Wacli.types';

/**
 * Call a wacli API route directly and get the parsed JSON back.
 *
 * Every helper below is a thin wrapper over this. The full route list is in
 * `docs/ai-harness-reference.md` — reach for `request` when you need one the helpers skip
 * (triggers, calls, groups, webhooks).
 */
export async function request<T = unknown>(
  method: string,
  path: string,
  body?: unknown
): Promise<T> {
  const encoded = body === undefined ? null : JSON.stringify(body);
  const raw = await WacliModule.request(method, path, encoded);
  if (!raw) {
    return undefined as T;
  }
  return JSON.parse(raw) as T;
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

/** Whether a WhatsApp session exists. Call this before `start()` to decide login vs. resume. */
export function isPaired(): Promise<boolean> {
  return WacliModule.isPaired();
}

/** Whether the service is started. */
export function isRunning(): Promise<boolean> {
  return WacliModule.isRunning();
}

/** Whether the WhatsApp socket is up right now. Can be false while running, during a reconnect. */
export function isConnected(): Promise<boolean> {
  return WacliModule.isConnected();
}

/**
 * Connect to WhatsApp and begin serving the API.
 *
 * On Android this starts a foreground service, so the connection survives backgrounding; the user
 * sees a persistent notification, which is the price the platform charges for that. On iOS the
 * connection lives only as long as the app is foregrounded — see the README for what that means.
 */
export function start(): Promise<void> {
  return WacliModule.start();
}

/** Disconnect and close the databases cleanly. */
export function stop(): Promise<void> {
  return WacliModule.stop();
}

/** Erase the WhatsApp session. Local message history is kept. */
export function logout(): Promise<void> {
  return WacliModule.logout();
}

/** The wacli version the native bindings were built from. */
export function getVersion(): Promise<string> {
  return WacliModule.getVersion();
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

/**
 * Begin QR pairing. Codes arrive on `onLoginQRCode` and must be rendered for another device to
 * scan.
 *
 * If the app is running on the same phone as WhatsApp there is nothing to scan the code with —
 * use {@link loginWithPhone} instead.
 */
export function loginWithQR(): Promise<void> {
  return WacliModule.loginWithQR();
}

/**
 * Begin pairing-code login for a phone number in international format (`+15551234567`). The code
 * arrives on `onLoginPairingCode`; the user types it into WhatsApp under Linked Devices → Link with
 * phone number.
 */
export function loginWithPhone(phone: string): Promise<void> {
  return WacliModule.loginWithPhone(phone);
}

/** Abort a login attempt in progress. */
export function cancelLogin(): Promise<void> {
  return WacliModule.cancelLogin();
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

/**
 * Subscribe to a wacli event. Remember to `.remove()` the subscription on unmount.
 *
 * `onEvent` payloads cross the native bridge as JSON strings — the shapes are deep and vary by
 * event, which is far more than the bridge's own type system wants to model — so they are parsed
 * here before reaching the listener.
 */
export function addListener<E extends keyof WacliModuleEvents>(
  event: E,
  listener: WacliModuleEvents[E]
): EventSubscription {
  if (event !== 'onEvent') {
    return WacliModule.addListener(event, listener as never);
  }
  const wrapped = ({ event: name, payload }: { event: string; payload: string }) => {
    let parsed: unknown = {};
    try {
      parsed = payload ? JSON.parse(payload) : {};
    } catch {
      // A payload we cannot parse is still worth delivering — the event name alone tells a UI to
      // refresh — so fall through with an empty object rather than dropping it.
    }
    (listener as WacliModuleEvents['onEvent'])({
      event: name as Parameters<WacliModuleEvents['onEvent']>[0]['event'],
      payload: parsed as Parameters<WacliModuleEvents['onEvent']>[0]['payload'],
    });
  };
  return WacliModule.addListener('onEvent', wrapped as never);
}

// ---------------------------------------------------------------------------
// Typed helpers
// ---------------------------------------------------------------------------

export function getStatus(): Promise<StatusSnapshot> {
  return request<StatusSnapshot>('GET', '/status');
}

/**
 * Read the DND switch. It is wacli's hard automation gate: while it is off, outbound sends are
 * refused and webhooks do not fire.
 */
export async function getDND(): Promise<boolean> {
  const response = await request<{ enabled: boolean }>('GET', '/dnd');
  return response.enabled;
}

/** Arm or disarm outbound automation. Off by default — nothing can send until you turn it on. */
export async function setDND(enabled: boolean): Promise<boolean> {
  const response = await request<{ enabled: boolean }>('PUT', '/dnd', { enabled });
  return response.enabled;
}

/** Pull fresh chats, contacts and history from WhatsApp. Slow; safe to call on a pull-to-refresh. */
export function sync(): Promise<{ ok: boolean; history_requested: boolean }> {
  return request('POST', '/sync', {});
}

export async function listChats(options: ListChatsOptions = {}): Promise<ChatRecord[]> {
  const query = new URLSearchParams();
  if (options.filter) query.set('filter', options.filter);
  if (options.query) query.set('query', options.query);
  if (options.limit) query.set('limit', String(options.limit));
  const response = await request<{ chats: ChatRecord[] | null }>(
    'GET',
    withQuery('/chats', query)
  );
  return response.chats ?? [];
}

export async function listMessages(options: ListMessagesOptions = {}): Promise<MessageRecord[]> {
  const query = new URLSearchParams();
  if (options.chat) query.set('chat_ref', options.chat);
  if (options.sender) query.set('sender_ref', options.sender);
  if (options.query) query.set('query', options.query);
  if (options.limit) query.set('limit', String(options.limit));
  if (options.mediaOnly) query.set('media_only', 'true');
  if (options.fromMe !== undefined) query.set('from_me', String(options.fromMe));
  if (options.before) query.set('before', options.before.toISOString());
  if (options.after) query.set('after', options.after.toISOString());
  const response = await request<{ messages: MessageRecord[] | null }>(
    'GET',
    withQuery('/messages', query)
  );
  return response.messages ?? [];
}

/**
 * Send a message. `to` accepts a phone number, a JID, or a contact name.
 *
 * Rejects if DND is off or the chat is locked — both are deliberate safety gates, not bugs. Surface
 * the error message to the user rather than retrying.
 */
export function sendMessage(
  options: SendMessageOptions
): Promise<{ ok: boolean; message: MessageRecord }> {
  return request('POST', '/send', {
    to: options.to,
    text: options.text ?? '',
    media_path: options.mediaPath,
    reply_to: options.replyTo,
  });
}

export function editMessage(
  chat: string,
  messageID: string,
  text: string
): Promise<{ ok: boolean }> {
  return request('POST', '/messages/edit', { chat_ref: chat, message_id: messageID, text });
}

export function deleteMessage(chat: string, messageID: string): Promise<{ ok: boolean }> {
  return request('POST', '/messages/delete', { chat_ref: chat, message_id: messageID });
}

/**
 * Download a message's media into the app sandbox and return its absolute path. Read the file with
 * expo-file-system, or hand the path straight to an `<Image source={{ uri: 'file://' + path }} />`.
 */
export async function downloadMedia(chat: string, messageID: string): Promise<string> {
  const response = await request<{ path: string }>('POST', '/media/download', {
    chat_ref: chat,
    message_id: messageID,
  });
  return response.path;
}

/** Lock a chat to put it permanently off-limits to automation, or unlock it again. */
export function setChatLocked(jid: string, locked: boolean): Promise<void> {
  return request('PUT', `/chats/${encodeURIComponent(jid)}`, { locked });
}

function withQuery(path: string, query: URLSearchParams): string {
  const encoded = query.toString();
  return encoded ? `${path}?${encoded}` : path;
}
