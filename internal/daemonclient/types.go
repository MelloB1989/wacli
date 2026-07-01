package daemonclient

import "time"

type StatusSnapshot struct {
	Connected               bool       `json:"connected"`
	UserJID                 string     `json:"user_jid,omitempty"`
	DNDMode                 bool       `json:"dnd_mode"`
	InitialAccessConfigured bool       `json:"initial_access_configured"`
	ChatCount               int        `json:"chat_count"`
	MessageCount            int        `json:"message_count"`
	LastHistorySync         *time.Time `json:"last_history_sync,omitempty"`
}

type ChatRecord struct {
	JID                string    `json:"jid"`
	Name               string    `json:"name"`
	IsGroup            bool      `json:"is_group"`
	Locked             bool      `json:"locked"`
	FirstSeenAt        time.Time `json:"first_seen_at"`
	LastMessageAt      time.Time `json:"last_message_at"`
	LastMessagePreview string    `json:"last_message_preview"`
}

type AssistantSettings struct {
	AssistantName    string `json:"assistant_name"`
	Personality      string `json:"personality"`
	Behavior         string `json:"behavior"`
	ReplyStyle       string `json:"reply_style"`
	ReplyInstruction string `json:"reply_instruction"`
	PreferredRuntime string `json:"preferred_runtime"`
	CodexModel       string `json:"codex_model"`
	ClaudeModel      string `json:"claude_model"`
	OpenClawCommand  string `json:"openclaw_command"`
	KarmaProvider    string `json:"karma_provider"`
	KarmaModel       string `json:"karma_model"`
	KarmaAPIKey      string `json:"karma_api_key,omitempty"`
}

type AppLogRecord struct {
	ID          int64     `json:"id"`
	Level       string    `json:"level"`
	Category    string    `json:"category"`
	Message     string    `json:"message"`
	DetailsJSON string    `json:"details_json,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type WebhookRecord struct {
	ID           int64     `json:"id"`
	URL          string    `json:"url"`
	Secret       string    `json:"secret,omitempty"`
	Events       []string  `json:"events"`
	Scope        string    `json:"scope"`
	ChatJIDs     []string  `json:"chat_jids,omitempty"`
	MessageTypes []string  `json:"message_types,omitempty"`
	ContextLimit int       `json:"context_limit"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type OpenClawBridgeRecord struct {
	ID           int64     `json:"id"`
	Command      string    `json:"command"`
	Scope        string    `json:"scope"`
	ChatJIDs     []string  `json:"chat_jids,omitempty"`
	MessageTypes []string  `json:"message_types,omitempty"`
	ContextLimit int       `json:"context_limit"`
	Instruction  string    `json:"instruction"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type OpenClawDeliveryRecord struct {
	BridgeID       int64     `json:"bridge_id"`
	ChatJID        string    `json:"chat_jid"`
	ChatName       string    `json:"chat_name,omitempty"`
	MessageID      string    `json:"message_id"`
	SessionID      string    `json:"session_id"`
	Command        string    `json:"command,omitempty"`
	Status         string    `json:"status"`
	LastError      string    `json:"last_error,omitempty"`
	RequestMessage string    `json:"request_message,omitempty"`
	ResponseOutput string    `json:"response_output,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ChatListOptions struct {
	Filter string
	Query  string
	Limit  int
}

type LogQuery struct {
	Level    string
	Category string
	Query    string
	Limit    int
}

type DeliveryQuery struct {
	Status string
	Query  string
	Limit  int
}

type SyncResponse struct {
	OK               bool       `json:"ok"`
	HistorySeen      bool       `json:"history_seen"`
	HistoryRequested bool       `json:"history_requested"`
	RequestedAt      string     `json:"requested_at"`
	LastHistorySync  *time.Time `json:"last_history_sync,omitempty"`
}
