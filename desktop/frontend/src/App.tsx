import {FormEvent, useDeferredValue, useEffect, useMemo, useState} from 'react'
import {useMutation, useQuery, useQueryClient} from '@tanstack/react-query'
import './App.css'
import {AssistantSettings, ChatRecord, OpenClawBridgeRecord, WebhookRecord, wacliDesktop} from './lib/wails'
import {AppTab, useUIStore} from './store/ui'

const chatFilters = ['all', 'locked', 'unlocked', 'groups', 'dms']

function App() {
  const queryClient = useQueryClient()
  const {
    activeTab,
    chatFilter,
    chatQuery,
    loginPairCode,
    loginPhone,
    setActiveTab,
    setChatFilter,
    setChatQuery,
    setLoginPairCode,
    setLoginPhone,
  } = useUIStore()

  const deferredChatQuery = useDeferredValue(chatQuery)
  const [assistantDraft, setAssistantDraft] = useState<AssistantSettings | null>(null)
  const [webhookForm, setWebhookForm] = useState({
    url: '',
    secret: '',
    events: 'incoming_message',
    scope: 'all_unlocked',
    context_limit: 12,
    enabled: true,
    chat_jids: [] as string[],
  })
  const [bridgeForm, setBridgeForm] = useState({
    command: 'openclaw',
    scope: 'all_unlocked',
    context_limit: 12,
    instruction: '',
    enabled: true,
    chat_jids: [] as string[],
  })

  const statusQuery = useQuery({
    queryKey: ['status'],
    queryFn: () => wacliDesktop.GetStatus(),
    refetchInterval: 5000,
  })

  const assistantQuery = useQuery({
    queryKey: ['assistant-settings'],
    queryFn: () => wacliDesktop.GetAssistantSettings(),
    refetchInterval: 10000,
  })

  const chatsQuery = useQuery({
    queryKey: ['chats', chatFilter, deferredChatQuery],
    queryFn: () => wacliDesktop.ListChats(chatFilter, deferredChatQuery, 300),
    refetchInterval: 7000,
  })

  const logsQuery = useQuery({
    queryKey: ['logs'],
    queryFn: () => wacliDesktop.ListLogs(200),
    refetchInterval: 4000,
  })

  const webhooksQuery = useQuery({
    queryKey: ['webhooks'],
    queryFn: () => wacliDesktop.ListWebhooks(),
    refetchInterval: 6000,
  })

  const bridgesQuery = useQuery({
    queryKey: ['openclaw-bridges'],
    queryFn: () => wacliDesktop.ListOpenClawBridges(),
    refetchInterval: 6000,
  })

  const deliveriesQuery = useQuery({
    queryKey: ['openclaw-deliveries'],
    queryFn: () => wacliDesktop.ListOpenClawDeliveries(30),
    refetchInterval: 4000,
  })

  const loginQuery = useQuery({
    queryKey: ['login-session'],
    queryFn: () => wacliDesktop.GetLoginSession(),
    refetchInterval: 2000,
  })

  const qrQuery = useQuery({
    queryKey: ['login-qr', loginQuery.data?.qr_available],
    queryFn: () => wacliDesktop.GetQRCodeData(),
    enabled: Boolean(loginQuery.data?.qr_available),
    refetchInterval: loginQuery.data?.running ? 2000 : false,
  })

  useEffect(() => {
    if (assistantQuery.data) {
      setAssistantDraft(assistantQuery.data)
    }
  }, [assistantQuery.data])

  const dndMutation = useMutation({
    mutationFn: (enabled: boolean) => wacliDesktop.SetDND(enabled),
    onSuccess: () => queryClient.invalidateQueries({queryKey: ['status']}),
  })

  const syncMutation = useMutation({
    mutationFn: () => wacliDesktop.Sync(),
    onSuccess: () => {
      queryClient.invalidateQueries({queryKey: ['status']})
      queryClient.invalidateQueries({queryKey: ['chats']})
      queryClient.invalidateQueries({queryKey: ['logs']})
    },
  })

  const lockMutation = useMutation({
    mutationFn: ({jid, locked}: {jid: string; locked: boolean}) => wacliDesktop.SetChatLocked(jid, locked),
    onSuccess: () => {
      queryClient.invalidateQueries({queryKey: ['chats']})
      queryClient.invalidateQueries({queryKey: ['logs']})
    },
  })

  const accessMutation = useMutation({
    mutationFn: (unlockedJIDs: string[]) => wacliDesktop.ConfigureAccess(unlockedJIDs),
    onSuccess: () => {
      queryClient.invalidateQueries({queryKey: ['chats']})
      queryClient.invalidateQueries({queryKey: ['status']})
    },
  })

  const assistantMutation = useMutation({
    mutationFn: (settings: AssistantSettings) => wacliDesktop.SaveAssistantSettings(settings),
    onSuccess: () => {
      queryClient.invalidateQueries({queryKey: ['assistant-settings']})
      queryClient.invalidateQueries({queryKey: ['logs']})
    },
  })

  const loginMutation = useMutation({
    mutationFn: ({pairCode, phone}: {pairCode: boolean; phone: string}) => wacliDesktop.StartLogin(pairCode, phone),
    onSuccess: () => queryClient.invalidateQueries({queryKey: ['login-session']}),
  })

  const createWebhookMutation = useMutation({
    mutationFn: (record: WebhookRecord) => wacliDesktop.CreateWebhook(record),
    onSuccess: () => {
      queryClient.invalidateQueries({queryKey: ['webhooks']})
      queryClient.invalidateQueries({queryKey: ['logs']})
      setWebhookForm({
        url: '',
        secret: '',
        events: 'incoming_message',
        scope: 'all_unlocked',
        context_limit: 12,
        enabled: true,
        chat_jids: [],
      })
    },
  })

  const deleteWebhookMutation = useMutation({
    mutationFn: (id: number) => wacliDesktop.DeleteWebhook(id),
    onSuccess: () => {
      queryClient.invalidateQueries({queryKey: ['webhooks']})
      queryClient.invalidateQueries({queryKey: ['logs']})
    },
  })

  const createBridgeMutation = useMutation({
    mutationFn: (record: OpenClawBridgeRecord) => wacliDesktop.CreateOpenClawBridge(record),
    onSuccess: () => {
      queryClient.invalidateQueries({queryKey: ['openclaw-bridges']})
      queryClient.invalidateQueries({queryKey: ['logs']})
      setBridgeForm({
        command: 'openclaw',
        scope: 'all_unlocked',
        context_limit: 12,
        instruction: '',
        enabled: true,
        chat_jids: [],
      })
    },
  })

  const deleteBridgeMutation = useMutation({
    mutationFn: (id: number) => wacliDesktop.DeleteOpenClawBridge(id),
    onSuccess: () => {
      queryClient.invalidateQueries({queryKey: ['openclaw-bridges']})
      queryClient.invalidateQueries({queryKey: ['logs']})
    },
  })

  const chats = chatsQuery.data ?? []
  const unlockedJIDs = useMemo(() => chats.filter((chat) => !chat.locked).map((chat) => chat.jid), [chats])
  const selectedChatOptions = useMemo(() => chats.map((chat) => ({value: chat.jid, label: chat.name || chat.jid})), [chats])
  const latestLogs = logsQuery.data ?? []
  const deliveries = deliveriesQuery.data ?? []

  function submitAssistant(event: FormEvent) {
    event.preventDefault()
    if (!assistantDraft) return
    assistantMutation.mutate(assistantDraft)
  }

  function submitWebhook(event: FormEvent) {
    event.preventDefault()
    createWebhookMutation.mutate({
      id: 0,
      url: webhookForm.url,
      secret: webhookForm.secret,
      events: webhookForm.events.split(',').map((item) => item.trim()).filter(Boolean),
      scope: webhookForm.scope,
      chat_jids: webhookForm.scope === 'selected_chats' ? webhookForm.chat_jids : [],
      message_types: ['*'],
      context_limit: Number(webhookForm.context_limit),
      enabled: webhookForm.enabled,
      created_at: '',
      updated_at: '',
    })
  }

  function submitBridge(event: FormEvent) {
    event.preventDefault()
    createBridgeMutation.mutate({
      id: 0,
      command: bridgeForm.command,
      scope: bridgeForm.scope,
      chat_jids: bridgeForm.scope === 'selected_chats' ? bridgeForm.chat_jids : [],
      message_types: ['*'],
      context_limit: Number(bridgeForm.context_limit),
      instruction: bridgeForm.instruction,
      enabled: bridgeForm.enabled,
      created_at: '',
      updated_at: '',
    })
  }

  return (
    <div className="shell">
      <aside className="rail">
        <div className="brand">
          <div className="brand-mark">WA</div>
          <div>
            <div className="brand-title">WACLI</div>
            <div className="brand-subtitle">WhatsApp automation agent</div>
          </div>
        </div>
        <nav className="nav">
          {(['dashboard', 'chats', 'logs', 'automation', 'assistant'] as AppTab[]).map((tab) => (
            <button
              key={tab}
              className={`nav-button ${activeTab === tab ? 'active' : ''}`}
              onClick={() => setActiveTab(tab)}
            >
              {tab}
            </button>
          ))}
        </nav>
      </aside>

      <main className="main">
        <header className="hero">
          <div>
            <div className="eyebrow">Local-first control plane</div>
            <h1>Own the assistant before it owns your inbox.</h1>
            <p>
              DND gates automation, locked chats enforce hard boundaries, and the daemon stays the
              source of truth for every message, webhook, and agent action.
            </p>
          </div>
          <div className="hero-actions">
            <button className="primary" onClick={() => syncMutation.mutate()} disabled={syncMutation.isPending}>
              {syncMutation.isPending ? 'Syncing…' : 'Sync now'}
            </button>
            <button
              className="secondary"
              onClick={() => dndMutation.mutate(!(statusQuery.data?.dnd_mode ?? false))}
              disabled={dndMutation.isPending}
            >
              {statusQuery.data?.dnd_mode ? 'Disable DND' : 'Enable DND'}
            </button>
          </div>
        </header>

        <section className="stats-grid">
          <StatCard label="Connection" value={statusQuery.data?.connected ? 'Connected' : 'Offline'} tone={statusQuery.data?.connected ? 'good' : 'warn'} />
          <StatCard label="DND" value={statusQuery.data?.dnd_mode ? 'On' : 'Off'} tone={statusQuery.data?.dnd_mode ? 'good' : 'warn'} />
          <StatCard label="Chats" value={String(statusQuery.data?.chat_count ?? 0)} />
          <StatCard label="Messages" value={String(statusQuery.data?.message_count ?? 0)} />
        </section>

        <section className="login-strip panel">
          <div className="panel-header">
            <div>
              <h2>WhatsApp Connection</h2>
              <p>Run QR or phone-pair login from the desktop app and keep access setup in-app.</p>
            </div>
          </div>
          <div className="login-controls">
            <label className="toggle">
              <input type="checkbox" checked={loginPairCode} onChange={(event) => setLoginPairCode(event.target.checked)} />
              Use phone-number pairing
            </label>
            <input
              className="field"
              placeholder="+91..."
              value={loginPhone}
              onChange={(event) => setLoginPhone(event.target.value)}
              disabled={!loginPairCode}
            />
            <button
              className="primary"
              onClick={() => loginMutation.mutate({pairCode: loginPairCode, phone: loginPhone})}
              disabled={loginMutation.isPending || loginQuery.data?.running}
            >
              {loginQuery.data?.running ? 'Login running…' : 'Start login'}
            </button>
          </div>
          <div className="login-grid">
            <div className="login-qr">
              {qrQuery.data ? <img src={qrQuery.data} alt="WhatsApp QR" /> : <div className="empty-state">QR will appear here when login starts.</div>}
            </div>
            <div className="login-console">
              <div className="console-title">Login output</div>
              <pre>{(loginQuery.data?.output_lines ?? []).join('\n') || 'No login output yet.'}</pre>
            </div>
          </div>
        </section>

        {activeTab === 'dashboard' && (
          <div className="tab-grid">
            <section className="panel">
              <div className="panel-header">
                <div>
                  <h2>System Snapshot</h2>
                  <p>Connection, bootstrap status, and guardrail state.</p>
                </div>
              </div>
              <div className="detail-list">
                <DetailRow label="User JID" value={statusQuery.data?.user_jid || 'Not connected'} />
                <DetailRow
                  label="Last history sync"
                  value={statusQuery.data?.last_history_sync ? new Date(statusQuery.data.last_history_sync).toLocaleString() : 'Never'}
                />
                <DetailRow
                  label="Initial access configured"
                  value={statusQuery.data?.initial_access_configured ? 'Yes' : 'No'}
                />
                <DetailRow label="Preferred runtime" value={assistantQuery.data?.preferred_runtime || 'openclaw'} />
              </div>
            </section>
            <section className="panel">
              <div className="panel-header">
                <div>
                  <h2>Recent Assistant Activity</h2>
                  <p>OpenClaw delivery history from the daemon.</p>
                </div>
              </div>
              <div className="list">
                {deliveries.slice(0, 8).map((delivery) => (
                  <article key={`${delivery.bridge_id}-${delivery.message_id}`} className="list-item">
                    <div>
                      <strong>{delivery.chat_name || delivery.chat_jid}</strong>
                      <div className="meta">{new Date(delivery.updated_at).toLocaleString()}</div>
                    </div>
                    <span className={`pill ${delivery.status}`}>{delivery.status}</span>
                  </article>
                ))}
                {deliveries.length === 0 && <div className="empty-state">No assistant deliveries yet.</div>}
              </div>
            </section>
          </div>
        )}

        {activeTab === 'chats' && (
          <section className="panel">
            <div className="panel-header">
              <div>
                <h2>Locked / Unlocked Chats</h2>
                <p>Use locks as hard boundaries. Save the current unlocked set as the assistant allow-list.</p>
              </div>
              <div className="panel-actions">
                <button className="secondary" onClick={() => accessMutation.mutate(unlockedJIDs)} disabled={accessMutation.isPending}>
                  Save allow-list
                </button>
              </div>
            </div>
            <div className="toolbar">
              <input className="field grow" placeholder="Search chats…" value={chatQuery} onChange={(event) => setChatQuery(event.target.value)} />
              <div className="chip-row">
                {chatFilters.map((filter) => (
                  <button
                    key={filter}
                    className={`chip ${chatFilter === filter ? 'active' : ''}`}
                    onClick={() => setChatFilter(filter)}
                  >
                    {filter}
                  </button>
                ))}
              </div>
            </div>
            <div className="chat-list">
              {chats.map((chat) => (
                <article key={chat.jid} className="chat-card">
                  <div>
                    <div className="chat-title">{chat.name || chat.jid}</div>
                    <div className="meta">{chat.jid}</div>
                    <div className="meta">{chat.last_message_preview || 'No preview available'}</div>
                  </div>
                  <div className="chat-actions">
                    <span className={`pill ${chat.locked ? 'warn' : 'good'}`}>{chat.locked ? 'Locked' : 'Unlocked'}</span>
                    <button
                      className="secondary"
                      onClick={() => lockMutation.mutate({jid: chat.jid, locked: !chat.locked})}
                      disabled={lockMutation.isPending}
                    >
                      {chat.locked ? 'Unlock' : 'Lock'}
                    </button>
                  </div>
                </article>
              ))}
              {chats.length === 0 && <div className="empty-state">No chats match the current filter.</div>}
            </div>
          </section>
        )}

        {activeTab === 'logs' && (
          <section className="panel">
            <div className="panel-header">
              <div>
                <h2>Local App Logs</h2>
                <p>Persisted connection, message, webhook, and assistant events.</p>
              </div>
            </div>
            <div className="log-list">
              {latestLogs.map((record) => (
                <article key={record.id} className="log-row">
                  <div className="log-meta">
                    <span className={`pill ${record.level}`}>{record.level}</span>
                    <span>{new Date(record.created_at).toLocaleString()}</span>
                    <span>{record.category}</span>
                  </div>
                  <div className="log-message">{record.message}</div>
                  {record.details_json && <pre className="log-details">{record.details_json}</pre>}
                </article>
              ))}
              {latestLogs.length === 0 && <div className="empty-state">No logs yet.</div>}
            </div>
          </section>
        )}

        {activeTab === 'automation' && (
          <div className="tab-grid automation-grid">
            <section className="panel">
              <div className="panel-header">
                <div>
                  <h2>HTTP Webhooks</h2>
                  <p>Deliver incoming and outgoing message context to external systems.</p>
                </div>
              </div>
              <form className="form-grid" onSubmit={submitWebhook}>
                <input className="field" placeholder="Webhook URL" value={webhookForm.url} onChange={(event) => setWebhookForm((prev) => ({...prev, url: event.target.value}))} />
                <input className="field" placeholder="Secret (optional)" value={webhookForm.secret} onChange={(event) => setWebhookForm((prev) => ({...prev, secret: event.target.value}))} />
                <input className="field" placeholder="Events (comma-separated)" value={webhookForm.events} onChange={(event) => setWebhookForm((prev) => ({...prev, events: event.target.value}))} />
                <select className="field" value={webhookForm.scope} onChange={(event) => setWebhookForm((prev) => ({...prev, scope: event.target.value}))}>
                  <option value="all_unlocked">all_unlocked</option>
                  <option value="selected_chats">selected_chats</option>
                </select>
                <input
                  className="field"
                  type="number"
                  min={1}
                  max={100}
                  value={webhookForm.context_limit}
                  onChange={(event) => setWebhookForm((prev) => ({...prev, context_limit: Number(event.target.value)}))}
                />
                {webhookForm.scope === 'selected_chats' && (
                  <select
                    multiple
                    className="field multi"
                    value={webhookForm.chat_jids}
                    onChange={(event) =>
                      setWebhookForm((prev) => ({
                        ...prev,
                        chat_jids: Array.from(event.target.selectedOptions).map((item) => item.value),
                      }))
                    }
                  >
                    {selectedChatOptions.map((option) => (
                      <option key={option.value} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                )}
                <button className="primary" type="submit" disabled={createWebhookMutation.isPending}>
                  Add webhook
                </button>
              </form>
              <div className="list">
                {(webhooksQuery.data ?? []).map((webhook) => (
                  <article key={webhook.id} className="list-item stacked">
                    <div>
                      <strong>{webhook.url}</strong>
                      <div className="meta">{webhook.events.join(', ')} · scope={webhook.scope}</div>
                    </div>
                    <button className="ghost" onClick={() => deleteWebhookMutation.mutate(webhook.id)}>
                      Delete
                    </button>
                  </article>
                ))}
                {webhooksQuery.data?.length === 0 && <div className="empty-state">No webhooks configured.</div>}
              </div>
            </section>

            <section className="panel">
              <div className="panel-header">
                <div>
                  <h2>OpenClaw Bridges</h2>
                  <p>Existing command-driven assistant automation path.</p>
                </div>
              </div>
              <form className="form-grid" onSubmit={submitBridge}>
                <input className="field" placeholder="Command" value={bridgeForm.command} onChange={(event) => setBridgeForm((prev) => ({...prev, command: event.target.value}))} />
                <select className="field" value={bridgeForm.scope} onChange={(event) => setBridgeForm((prev) => ({...prev, scope: event.target.value}))}>
                  <option value="all_unlocked">all_unlocked</option>
                  <option value="selected_chats">selected_chats</option>
                </select>
                <input
                  className="field"
                  type="number"
                  min={1}
                  max={100}
                  value={bridgeForm.context_limit}
                  onChange={(event) => setBridgeForm((prev) => ({...prev, context_limit: Number(event.target.value)}))}
                />
                <textarea className="field textarea" placeholder="Instruction override" value={bridgeForm.instruction} onChange={(event) => setBridgeForm((prev) => ({...prev, instruction: event.target.value}))} />
                {bridgeForm.scope === 'selected_chats' && (
                  <select
                    multiple
                    className="field multi"
                    value={bridgeForm.chat_jids}
                    onChange={(event) =>
                      setBridgeForm((prev) => ({
                        ...prev,
                        chat_jids: Array.from(event.target.selectedOptions).map((item) => item.value),
                      }))
                    }
                  >
                    {selectedChatOptions.map((option) => (
                      <option key={option.value} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                )}
                <button className="primary" type="submit" disabled={createBridgeMutation.isPending}>
                  Add bridge
                </button>
              </form>
              <div className="list">
                {(bridgesQuery.data ?? []).map((bridge) => (
                  <article key={bridge.id} className="list-item stacked">
                    <div>
                      <strong>{bridge.command}</strong>
                      <div className="meta">scope={bridge.scope} · context={bridge.context_limit}</div>
                    </div>
                    <button className="ghost" onClick={() => deleteBridgeMutation.mutate(bridge.id)}>
                      Delete
                    </button>
                  </article>
                ))}
              </div>
              <div className="delivery-stream">
                <h3>Recent deliveries</h3>
                {deliveries.map((delivery) => (
                  <article key={`${delivery.bridge_id}-${delivery.message_id}`} className="delivery-row">
                    <div>
                      <strong>{delivery.chat_name || delivery.chat_jid}</strong>
                      <div className="meta">{delivery.status}</div>
                    </div>
                    <div className="meta">{new Date(delivery.updated_at).toLocaleString()}</div>
                  </article>
                ))}
              </div>
            </section>
          </div>
        )}

        {activeTab === 'assistant' && assistantDraft && (
          <section className="panel">
            <div className="panel-header">
              <div>
                <h2>Assistant Behavior</h2>
                <p>
                  Define personality, reply style, and runtime preferences for OpenClaw, Codex,
                  Claude, or Karma-backed custom replies.
                </p>
              </div>
            </div>
            <form className="assistant-form" onSubmit={submitAssistant}>
              <div className="form-grid two">
                <label>
                  <span>Assistant name</span>
                  <input
                    className="field"
                    value={assistantDraft.assistant_name}
                    onChange={(event) => setAssistantDraft({...assistantDraft, assistant_name: event.target.value})}
                  />
                </label>
                <label>
                  <span>Preferred runtime</span>
                  <select
                    className="field"
                    value={assistantDraft.preferred_runtime}
                    onChange={(event) => setAssistantDraft({...assistantDraft, preferred_runtime: event.target.value})}
                  >
                    <option value="openclaw">OpenClaw</option>
                    <option value="codex">Codex</option>
                    <option value="claude">Claude Code</option>
                    <option value="karma">Karma AI</option>
                  </select>
                </label>
              </div>

              <label>
                <span>Personality</span>
                <textarea className="field textarea" value={assistantDraft.personality} onChange={(event) => setAssistantDraft({...assistantDraft, personality: event.target.value})} />
              </label>
              <label>
                <span>Behavior</span>
                <textarea className="field textarea" value={assistantDraft.behavior} onChange={(event) => setAssistantDraft({...assistantDraft, behavior: event.target.value})} />
              </label>
              <label>
                <span>Reply style</span>
                <textarea className="field textarea" value={assistantDraft.reply_style} onChange={(event) => setAssistantDraft({...assistantDraft, reply_style: event.target.value})} />
              </label>
              <label>
                <span>Reply instruction</span>
                <textarea
                  className="field textarea tall"
                  value={assistantDraft.reply_instruction}
                  onChange={(event) => setAssistantDraft({...assistantDraft, reply_instruction: event.target.value})}
                />
              </label>

              <div className="form-grid three">
                <label>
                  <span>Codex model</span>
                  <input className="field" value={assistantDraft.codex_model} onChange={(event) => setAssistantDraft({...assistantDraft, codex_model: event.target.value})} />
                </label>
                <label>
                  <span>Claude model</span>
                  <input className="field" value={assistantDraft.claude_model} onChange={(event) => setAssistantDraft({...assistantDraft, claude_model: event.target.value})} />
                </label>
                <label>
                  <span>OpenClaw command</span>
                  <input className="field" value={assistantDraft.openclaw_command} onChange={(event) => setAssistantDraft({...assistantDraft, openclaw_command: event.target.value})} />
                </label>
                <label>
                  <span>Karma provider</span>
                  <input className="field" value={assistantDraft.karma_provider} onChange={(event) => setAssistantDraft({...assistantDraft, karma_provider: event.target.value})} />
                </label>
                <label>
                  <span>Karma model</span>
                  <input className="field" value={assistantDraft.karma_model} onChange={(event) => setAssistantDraft({...assistantDraft, karma_model: event.target.value})} />
                </label>
                <label>
                  <span>Karma API key</span>
                  <input className="field" type="password" value={assistantDraft.karma_api_key || ''} onChange={(event) => setAssistantDraft({...assistantDraft, karma_api_key: event.target.value})} />
                </label>
              </div>

              <div className="panel-actions">
                <button className="primary" type="submit" disabled={assistantMutation.isPending}>
                  Save assistant settings
                </button>
              </div>
            </form>
          </section>
        )}
      </main>
    </div>
  )
}

function StatCard({label, value, tone = 'neutral'}: {label: string; value: string; tone?: 'neutral' | 'good' | 'warn'}) {
  return (
    <article className={`stat-card ${tone}`}>
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
    </article>
  )
}

function DetailRow({label, value}: {label: string; value: string}) {
  return (
    <div className="detail-row">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  )
}

export default App
