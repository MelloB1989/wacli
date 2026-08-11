export type StatusSnapshot = {
  connected: boolean
  user_jid?: string
  dnd_mode: boolean
  initial_access_configured: boolean
  chat_count: number
  message_count: number
  last_history_sync?: string | null
}

export type ChatRecord = {
  jid: string
  name: string
  is_group: boolean
  locked: boolean
  first_seen_at: string
  last_message_at: string
  last_message_preview: string
}

export type AppLogRecord = {
  id: number
  level: string
  category: string
  message: string
  details_json?: string
  created_at: string
}

export type WebhookRecord = {
  id: number
  url: string
  secret?: string
  events: string[]
  scope: string
  chat_jids?: string[]
  message_types?: string[]
  context_limit: number
  enabled: boolean
  created_at: string
  updated_at: string
}

export type SyncResponse = {
  ok: boolean
  history_seen: boolean
  history_requested: boolean
  requested_at: string
  last_history_sync?: string | null
}

export type LoginSessionState = {
  running: boolean
  pair_code: boolean
  started_at: string
  qr_path: string
  qr_available: boolean
  output_lines: string[]
  last_error?: string
}

declare global {
  interface Window {
    go?: {
      main?: {
        App?: Record<string, (...args: any[]) => Promise<any>>
      }
    }
  }
}

function getBackendMethod<T extends (...args: any[]) => Promise<any>>(name: string): T {
  const fn = window.go?.main?.App?.[name]
  if (!fn) {
    throw new Error(`Wails backend method ${name} is not available`)
  }
  return fn as T
}

export const wacliDesktop = {
  GetStatus: () => getBackendMethod<() => Promise<StatusSnapshot>>('GetStatus')(),
  SetDND: (enabled: boolean) => getBackendMethod<(enabled: boolean) => Promise<void>>('SetDND')(enabled),
  Sync: () => getBackendMethod<() => Promise<SyncResponse>>('Sync')(),
  ListChats: (filter: string, query: string, limit: number) =>
    getBackendMethod<(filter: string, query: string, limit: number) => Promise<ChatRecord[]>>('ListChats')(filter, query, limit),
  SetChatLocked: (jid: string, locked: boolean) =>
    getBackendMethod<(jid: string, locked: boolean) => Promise<void>>('SetChatLocked')(jid, locked),
  ConfigureAccess: (unlocked: string[]) =>
    getBackendMethod<(unlocked: string[]) => Promise<void>>('ConfigureAccess')(unlocked),
  ListLogs: (limit: number) =>
    getBackendMethod<(limit: number) => Promise<AppLogRecord[]>>('ListLogs')(limit),
  ListWebhooks: () => getBackendMethod<() => Promise<WebhookRecord[]>>('ListWebhooks')(),
  CreateWebhook: (record: WebhookRecord) =>
    getBackendMethod<(record: WebhookRecord) => Promise<WebhookRecord>>('CreateWebhook')(record),
  DeleteWebhook: (id: number) => getBackendMethod<(id: number) => Promise<void>>('DeleteWebhook')(id),
  StartLogin: (pairCode: boolean, phone: string) =>
    getBackendMethod<(pairCode: boolean, phone: string) => Promise<void>>('StartLogin')(pairCode, phone),
  GetLoginSession: () => getBackendMethod<() => Promise<LoginSessionState>>('GetLoginSession')(),
  GetQRCodeData: () => getBackendMethod<() => Promise<string>>('GetQRCodeData')(),
}
