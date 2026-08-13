import { NativeModule, requireNativeModule } from 'expo';

import type { WacliModuleEvents } from './Wacli.types';

declare class WacliNativeModule extends NativeModule<WacliModuleEvents> {
  /**
   * Open the databases and connect to WhatsApp. Requires an existing session — check `isPaired()`
   * first. On Android this also starts the foreground service that keeps the connection alive.
   */
  start(): Promise<void>;
  /** Disconnect and close the databases. Stops the Android foreground service. */
  stop(): Promise<void>;
  /** Whether a WhatsApp session exists. Safe to call before `start()`. */
  isPaired(): Promise<boolean>;
  /** Whether the service is started. */
  isRunning(): Promise<boolean>;
  /** Whether the WhatsApp socket is currently up. False during a reconnect. */
  isConnected(): Promise<boolean>;
  /**
   * Call the wacli API in-process. Returns the parsed JSON body, or rejects with wacli's own error
   * message. Prefer the typed helpers in `index.ts`; reach for this for routes they don't cover.
   */
  request(method: string, path: string, body: string | null): Promise<string>;
  /**
   * Run a wacli command line and return everything it printed. This is the same command layer the
   * `wacli` binary runs, so the syntax is exactly the documented CLI syntax.
   */
  exec(line: string): Promise<string>;
  /** The client command names, newline-separated. For completion, so no list has to be hardcoded. */
  execCommands(): Promise<string>;
  /** Begin QR pairing. Progress arrives on the `onLoginQRCode` / `onLoginStatus` events. */
  loginWithQR(): Promise<void>;
  /** Begin pairing-code login for a phone number in international format. */
  loginWithPhone(phone: string): Promise<void>;
  /** Abort a login attempt in progress. */
  cancelLogin(): Promise<void>;
  /** Erase the WhatsApp session. Message history is kept. */
  logout(): Promise<void>;
  /** The wacli version the native bindings were built from. */
  getVersion(): Promise<string>;

  /** Store a pre-rendered line as base64 s16le 16 kHz mono PCM. */
  addCachedLine(id: string, pcm: string): Promise<void>;
  /** Drop every cached line. */
  clearCachedLines(): void;
  /**
   * Ring a contact and bridge the call to the relay. Progress arrives on the `onVoice*` events.
   * Audio stays inside the native module and never crosses this boundary.
   */
  startVoiceCall(
    to: string,
    relayUrl: string,
    token: string,
    language: string,
    voice: string,
  ): Promise<void>;
  /** Hang up the call in progress. */
  endVoiceCall(reason: string): Promise<void>;

  /** Stop the daemon and return the session as base64. */
  exportSession(): Promise<string>;
  /** Replace this device's session with one handed back. */
  importSession(base64: string): Promise<void>;
  /** Whether a paired session exists on disk, without opening the service. */
  hasSession(): Promise<boolean>;
  /** Stop the daemon and erase the local session after handing it over. */
  releaseSession(): Promise<void>;
}

export default requireNativeModule<WacliNativeModule>('Wacli');
