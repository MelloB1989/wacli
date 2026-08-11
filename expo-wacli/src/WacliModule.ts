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
}

export default requireNativeModule<WacliNativeModule>('Wacli');
