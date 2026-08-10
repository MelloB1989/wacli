package client

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
