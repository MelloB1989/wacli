/**
 * Types mirroring wacli's JSON contract. The authoritative definitions live in the Go tree —
 * `wa.MessageRecord`, `client.ChatRecord`, `client.StatusSnapshot` — and the full route list is in
 * `docs/ai-harness-reference.md`.
 */

export type StatusSnapshot = {
  connected: boolean;
  user_jid?: string;
  dnd_mode: boolean;
  initial_access_configured: boolean;
  chat_count: number;
  message_count: number;
  last_history_sync?: string;
};

export type ChatRecord = {
  jid: string;
  name: string;
  is_group: boolean;
  /** Locked chats are off-limits to automation; sends to them are refused. */
  locked: boolean;
  first_seen_at: string;
  last_message_at: string;
  last_message_preview: string;
};

export type MessageRecord = {
  id: string;
  chat_jid: string;
  sender_jid: string;
  content: string;
  timestamp: string;
  is_from_me: boolean;
  message_type: string;
  /** This message @-mentioned the logged-in account. */
  mentions_me?: boolean;
  /** This message replies to one the logged-in account sent. */
  quoted_is_from_me?: boolean;
  mention_count?: number;
  media_type?: string;
  mime_type?: string;
  file_name?: string;
  /**
   * Absolute path inside the app sandbox, populated once the media has been downloaded. Read it
   * with expo-file-system — the daemon and the app are the same process here, so the path is
   * directly readable rather than something to fetch over HTTP.
   */
  media_path?: string;
};

/** Events wacli emits while running. Same set that feeds webhooks. */
export type WacliEventName =
  | 'incoming_message'
  | 'outgoing_message'
  | 'connection_state'
  | 'sync_complete';

export type WacliEventPayload = {
  chat?: ChatRecord;
  message?: MessageRecord;
  [key: string]: unknown;
};

/** Progress of a login attempt. */
export type LoginStatus = 'connecting' | 'success' | 'timeout' | 'cancelled';

export type WacliModuleEvents = {
  /** A live event from WhatsApp. */
  onEvent: (params: { event: WacliEventName; payload: WacliEventPayload }) => void;
  /**
   * A QR payload to render, during `loginWithQR`. Fires repeatedly — WhatsApp rotates the code
   * roughly every 20 seconds and each rotation replaces the last.
   */
  onLoginQRCode: (params: { code: string }) => void;
  /** The 8-character code to type into WhatsApp, during `loginWithPhone`. */
  onLoginPairingCode: (params: { code: string }) => void;
  /** A login lifecycle transition. */
  onLoginStatus: (params: { status: LoginStatus }) => void;
  /** A login attempt failed. */
  onLoginError: (params: { message: string }) => void;
};

export type SendMessageOptions = {
  /** Phone number, JID, or contact name — wacli resolves all three. */
  to: string;
  text?: string;
  /** Absolute path to a file in the app sandbox. */
  mediaPath?: string;
  /** Message ID to reply to. */
  replyTo?: string;
};

export type ListChatsOptions = {
  filter?: string;
  query?: string;
  limit?: number;
};

export type ListMessagesOptions = {
  chat?: string;
  sender?: string;
  query?: string;
  limit?: number;
  mediaOnly?: boolean;
  fromMe?: boolean;
  before?: Date;
  after?: Date;
};

/** Options for a streaming voice call. */
export type VoiceCallOptions = {
  /** Phone number in international format, or any reference the API accepts. */
  to: string;
  /** wss:// endpoint that runs the conversation. */
  relayUrl: string;
  /** Session token authorising this one call. */
  token: string;
  /** Speech language code, e.g. "hi-IN". */
  language?: string;
  /** Speaker id for synthesis. */
  voice?: string;
};

/** Lifecycle of a streaming call. */
export type VoiceCallState =
  | 'dialling'
  | 'ringing'
  | 'connected'
  | 'listening'
  | 'thinking'
  | 'speaking'
  | 'ended';

/** Callbacks for a streaming call. Audio never crosses this boundary. */
export type VoiceCallHandlers = {
  onState?: (state: VoiceCallState) => void;
  /** The other party's speech. Partial results arrive with `final` false. */
  onTranscript?: (text: string, final: boolean) => void;
  /** Fires exactly once, whatever ended the call. */
  onEnded?: (reason: string) => void;
};
