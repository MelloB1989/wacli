package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

const (
	settingDNDMode                 = "dnd_mode"
	settingInitialAccessConfigured = "initial_access_configured"
	settingLastHistorySync         = "last_history_sync"
	settingAssistantSettings       = "assistant_settings"
)

type Store struct {
	db *sql.DB
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

type MessageRecord struct {
	ID          string    `json:"id"`
	ChatJID     string    `json:"chat_jid"`
	SenderJID   string    `json:"sender_jid"`
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
	IsFromMe    bool      `json:"is_from_me"`
	MessageType string    `json:"message_type"`
	// MentionsMe = this message @-mentions THIS account (the bot). QuotedIsFromMe
	// = it is a reply/quote to a message THIS account sent. Both let a consumer
	// (e.g. KARMAX) tell that the bot is being directly addressed — generically,
	// from the account's own identity, with no hardcoded numbers.
	MentionsMe     bool `json:"mentions_me,omitempty"`
	QuotedIsFromMe bool `json:"quoted_is_from_me,omitempty"`
	// MentionCount is how many JIDs this message @-mentioned in total, so a
	// consumer can tell a direct mention from an "@all"-style mass mention.
	// wacli deliberately doesn't judge that itself — that policy lives in the
	// consumer (e.g. the KARMAX wa-monitor loop).
	MentionCount  int    `json:"mention_count,omitempty"`
	MediaType     string `json:"media_type,omitempty"`
	MimeType      string `json:"mime_type,omitempty"`
	FileName      string `json:"file_name,omitempty"`
	MediaPath     string `json:"media_path,omitempty"`
	URL           string `json:"url,omitempty"`
	DirectPath    string `json:"direct_path,omitempty"`
	FileLength    uint64 `json:"file_length,omitempty"`
	MediaKey      []byte `json:"-"`
	FileSHA256    []byte `json:"-"`
	FileEncSHA256 []byte `json:"-"`
}

type ContactRecord struct {
	JID          string    `json:"jid"`
	Phone        string    `json:"phone"`
	FullName     string    `json:"full_name"`
	FirstName    string    `json:"first_name"`
	PushName     string    `json:"push_name"`
	BusinessName string    `json:"business_name"`
	Found        bool      `json:"found"`
	Bio          string    `json:"bio,omitempty"`
	Notes        string    `json:"notes,omitempty"`
	Memory       string    `json:"memory,omitempty"`
	MetadataJSON string    `json:"metadata_json,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ContactUpdate struct {
	Bio          *string `json:"bio,omitempty"`
	Notes        *string `json:"notes,omitempty"`
	Memory       *string `json:"memory,omitempty"`
	MetadataJSON *string `json:"metadata_json,omitempty"`
}

type WebhookRecord struct {
	ID           int64    `json:"id"`
	URL          string   `json:"url"`
	Secret       string   `json:"secret,omitempty"`
	Events       []string `json:"events"`
	Scope        string   `json:"scope"`
	ChatJIDs     []string `json:"chat_jids,omitempty"`
	MessageTypes []string `json:"message_types,omitempty"`
	ContextLimit int      `json:"context_limit"`
	Enabled      bool     `json:"enabled"`
	// IncludeMentions delivers messages from chats OUTSIDE this webhook's scope
	// when they @-mention this account, so a consumer can react to being
	// summoned anywhere. Pure transport: what to do with those events (e.g.
	// ignoring "@all" blasts) is the consumer's policy, not wacli's.
	IncludeMentions bool      `json:"include_mentions"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
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

type OpenClawSessionRecord struct {
	ChatJID   string    `json:"chat_jid"`
	ChatName  string    `json:"chat_name,omitempty"`
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type WebhookDeliveryRecord struct {
	ID           int64     `json:"id"`
	WebhookID    int64     `json:"webhook_id"`
	URL          string    `json:"url"`
	Event        string    `json:"event"`
	ChatJID      string    `json:"chat_jid,omitempty"`
	MessageID    string    `json:"message_id,omitempty"`
	Status       string    `json:"status"`
	HTTPStatus   int       `json:"http_status,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	RequestBody  string    `json:"request_body,omitempty"`
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

type AutoReplyRule struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	MatchType     string    `json:"match_type"`
	Pattern       string    `json:"pattern,omitempty"`
	ReplyText     string    `json:"reply_text,omitempty"`
	MediaPath     string    `json:"media_path,omitempty"`
	Enabled       bool      `json:"enabled"`
	ApplyToDMs    bool      `json:"apply_to_dms"`
	ApplyToGroups bool      `json:"apply_to_groups"`
	Priority      int       `json:"priority"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type AppLogRecord struct {
	ID          int64     `json:"id"`
	Level       string    `json:"level"`
	Category    string    `json:"category"`
	Message     string    `json:"message"`
	DetailsJSON string    `json:"details_json,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
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

type StatusSnapshot struct {
	Connected               bool       `json:"connected"`
	UserJID                 string     `json:"user_jid,omitempty"`
	DNDMode                 bool       `json:"dnd_mode"`
	InitialAccessConfigured bool       `json:"initial_access_configured"`
	ChatCount               int        `json:"chat_count"`
	MessageCount            int        `json:"message_count"`
	LastHistorySync         *time.Time `json:"last_history_sync,omitempty"`
}

type MessageSearchOptions struct {
	ChatJID   string
	SenderJID string
	Query     string
	Limit     int
	FromMe    *bool
	MediaOnly bool
	Before    *time.Time
	After     *time.Time
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open store db: %w", err)
	}
	store := &Store{db: db}
	if err := store.init(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) init() error {
	pragmas := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
	}
	for _, pragma := range pragmas {
		if _, err := s.db.Exec(pragma); err != nil {
			return fmt.Errorf("apply pragma %q: %w", pragma, err)
		}
	}

	schema := `
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS chats (
	jid TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	is_group INTEGER NOT NULL DEFAULT 0,
	locked INTEGER NOT NULL DEFAULT 1,
	first_seen_at INTEGER NOT NULL,
	last_message_at INTEGER NOT NULL,
	last_message_preview TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_chats_last_message_at ON chats(last_message_at DESC);

CREATE TABLE IF NOT EXISTS messages (
	id TEXT NOT NULL,
	chat_jid TEXT NOT NULL,
	sender_jid TEXT NOT NULL,
	content TEXT NOT NULL DEFAULT '',
	timestamp INTEGER NOT NULL,
	is_from_me INTEGER NOT NULL DEFAULT 0,
	message_type TEXT NOT NULL DEFAULT 'text',
	media_type TEXT NOT NULL DEFAULT '',
	mime_type TEXT NOT NULL DEFAULT '',
	file_name TEXT NOT NULL DEFAULT '',
	media_path TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	direct_path TEXT NOT NULL DEFAULT '',
	file_length INTEGER NOT NULL DEFAULT 0,
	media_key BLOB,
	file_sha256 BLOB,
	file_enc_sha256 BLOB,
	PRIMARY KEY (id, chat_jid),
	FOREIGN KEY(chat_jid) REFERENCES chats(jid) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_jid, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_messages_ts ON messages(timestamp DESC);

CREATE TABLE IF NOT EXISTS contacts (
	jid TEXT PRIMARY KEY,
	phone TEXT NOT NULL DEFAULT '',
	full_name TEXT NOT NULL DEFAULT '',
	first_name TEXT NOT NULL DEFAULT '',
	push_name TEXT NOT NULL DEFAULT '',
	business_name TEXT NOT NULL DEFAULT '',
	found INTEGER NOT NULL DEFAULT 0,
	bio TEXT NOT NULL DEFAULT '',
	notes TEXT NOT NULL DEFAULT '',
	memory TEXT NOT NULL DEFAULT '',
	metadata_json TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS webhooks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	url TEXT NOT NULL,
	secret TEXT NOT NULL DEFAULT '',
	events TEXT NOT NULL,
	scope TEXT NOT NULL DEFAULT 'all_unlocked',
	chat_jids TEXT NOT NULL DEFAULT '[]',
	message_types TEXT NOT NULL DEFAULT '["*"]',
	context_limit INTEGER NOT NULL DEFAULT 12,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS openclaw_bridges (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	command TEXT NOT NULL DEFAULT 'openclaw',
	scope TEXT NOT NULL DEFAULT 'all_unlocked',
	chat_jids TEXT NOT NULL DEFAULT '[]',
	message_types TEXT NOT NULL DEFAULT '["*"]',
	context_limit INTEGER NOT NULL DEFAULT 12,
	instruction TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS openclaw_sessions (
	chat_jid TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS openclaw_deliveries (
	bridge_id INTEGER NOT NULL,
	chat_jid TEXT NOT NULL,
	message_id TEXT NOT NULL,
	session_id TEXT NOT NULL DEFAULT '',
	command TEXT NOT NULL DEFAULT '',
	request_message TEXT NOT NULL DEFAULT '',
	response_output TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'pending',
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (bridge_id, chat_jid, message_id),
	FOREIGN KEY(bridge_id) REFERENCES openclaw_bridges(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_openclaw_deliveries_status ON openclaw_deliveries(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	webhook_id INTEGER NOT NULL,
	url TEXT NOT NULL DEFAULT '',
	event TEXT NOT NULL DEFAULT '',
	chat_jid TEXT NOT NULL DEFAULT '',
	message_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'pending',
	http_status INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	response_body TEXT NOT NULL DEFAULT '',
	request_body TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	FOREIGN KEY(webhook_id) REFERENCES webhooks(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_status ON webhook_deliveries(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS auto_replies (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	match_type TEXT NOT NULL,
	pattern TEXT NOT NULL DEFAULT '',
	reply_text TEXT NOT NULL DEFAULT '',
	media_path TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	apply_to_dms INTEGER NOT NULL DEFAULT 1,
	apply_to_groups INTEGER NOT NULL DEFAULT 0,
	priority INTEGER NOT NULL DEFAULT 100,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auto_replies_priority ON auto_replies(priority ASC, id ASC);

CREATE TABLE IF NOT EXISTS app_logs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	level TEXT NOT NULL DEFAULT 'info',
	category TEXT NOT NULL DEFAULT 'app',
	message TEXT NOT NULL DEFAULT '',
	details_json TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_app_logs_created_at ON app_logs(created_at DESC);

CREATE TABLE IF NOT EXISTS message_receipts (
	message_id TEXT NOT NULL,
	chat_jid TEXT NOT NULL,
	recipient_jid TEXT NOT NULL,
	receipt_type TEXT NOT NULL DEFAULT '',
	timestamp INTEGER NOT NULL,
	PRIMARY KEY (message_id, recipient_jid, receipt_type)
);
CREATE INDEX IF NOT EXISTS idx_message_receipts_msg ON message_receipts(message_id);
`

	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	if err := s.ensureWebhookSchema(); err != nil {
		return err
	}
	if err := s.ensureOpenClawDeliverySchema(); err != nil {
		return err
	}
	if err := s.ensureMessageSchema(); err != nil {
		return err
	}

	if err := s.ensureSettingDefault(settingDNDMode, "false"); err != nil {
		return err
	}
	if err := s.ensureSettingDefault(settingInitialAccessConfigured, "false"); err != nil {
		return err
	}
	if err := s.ensureSettingDefault(settingAssistantSettings, "{}"); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureWebhookSchema() error {
	columns := []struct {
		name string
		ddl  string
	}{
		{name: "scope", ddl: "TEXT NOT NULL DEFAULT 'all_unlocked'"},
		{name: "chat_jids", ddl: "TEXT NOT NULL DEFAULT '[]'"},
		{name: "message_types", ddl: `TEXT NOT NULL DEFAULT '["*"]'`},
		{name: "context_limit", ddl: "INTEGER NOT NULL DEFAULT 12"},
		{name: "include_mentions", ddl: "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		if err := s.ensureTableColumn("webhooks", column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureOpenClawDeliverySchema() error {
	columns := []struct {
		name string
		ddl  string
	}{
		{name: "command", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "request_message", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "response_output", ddl: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := s.ensureTableColumn("openclaw_deliveries", column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureMessageSchema() error {
	columns := []struct {
		name string
		ddl  string
	}{
		{name: "edited", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "deleted", ddl: "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		if err := s.ensureTableColumn("messages", column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

// ReceiptRecord is a delivery/read receipt for a message from one recipient.
type ReceiptRecord struct {
	MessageID    string    `json:"message_id"`
	ChatJID      string    `json:"chat_jid"`
	RecipientJID string    `json:"recipient_jid"`
	Type         string    `json:"type"`
	Timestamp    time.Time `json:"timestamp"`
}

// StoreReceipt upserts a receipt (message + recipient + type is unique).
func (s *Store) StoreReceipt(rec ReceiptRecord) error {
	_, err := s.db.Exec(`INSERT INTO message_receipts (message_id, chat_jid, recipient_jid, receipt_type, timestamp)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(message_id, recipient_jid, receipt_type) DO UPDATE SET timestamp=excluded.timestamp`,
		rec.MessageID, rec.ChatJID, rec.RecipientJID, rec.Type, rec.Timestamp.Unix())
	return err
}

// ListReceipts returns all receipts for a message, oldest first.
func (s *Store) ListReceipts(messageID string) ([]ReceiptRecord, error) {
	rows, err := s.db.Query(`SELECT message_id, chat_jid, recipient_jid, receipt_type, timestamp
		FROM message_receipts WHERE message_id=? ORDER BY timestamp ASC`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReceiptRecord
	for rows.Next() {
		var r ReceiptRecord
		var ts int64
		if err := rows.Scan(&r.MessageID, &r.ChatJID, &r.RecipientJID, &r.Type, &ts); err != nil {
			return nil, err
		}
		r.Timestamp = time.Unix(ts, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateMessageContent rewrites a message's content and flags it edited.
func (s *Store) UpdateMessageContent(messageID, chatJID, content string) error {
	_, err := s.db.Exec(`UPDATE messages SET content=?, edited=1 WHERE id=? AND chat_jid=?`, content, messageID, chatJID)
	return err
}

// MarkMessageDeleted flags a message as revoked/deleted.
func (s *Store) MarkMessageDeleted(messageID, chatJID string) error {
	_, err := s.db.Exec(`UPDATE messages SET deleted=1 WHERE id=? AND chat_jid=?`, messageID, chatJID)
	return err
}

func (s *Store) ensureTableColumn(table, column, ddl string) error {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, column) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddl))
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) setSetting(key, value string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
`, key, value, now)
	return err
}

func (s *Store) ensureSettingDefault(key, defaultValue string) error {
	current, err := s.getSetting(key)
	if err != nil {
		return err
	}
	if current != "" {
		return nil
	}
	return s.setSetting(key, defaultValue)
}

func (s *Store) getSetting(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *Store) SetBoolSetting(key string, value bool) error {
	current, err := s.getSetting(key)
	if err != nil {
		return err
	}
	next := strconv.FormatBool(value)
	if current == next {
		return nil
	}
	return s.setSetting(key, next)
}

func (s *Store) GetBoolSetting(key string, defaultValue bool) (bool, error) {
	value, err := s.getSetting(key)
	if err != nil {
		return defaultValue, err
	}
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return defaultValue, nil
	}
	return parsed, nil
}

func (s *Store) SetDNDMode(enabled bool) error {
	if err := s.SetBoolSetting(settingDNDMode, enabled); err != nil {
		return err
	}
	return s.AddAppLog("info", "dnd", fmt.Sprintf("DND mode set to %t", enabled), map[string]any{
		"enabled": enabled,
	})
}

func (s *Store) GetDNDMode() (bool, error) {
	return s.GetBoolSetting(settingDNDMode, false)
}

func (s *Store) SetInitialAccessConfigured(enabled bool) error {
	return s.SetBoolSetting(settingInitialAccessConfigured, enabled)
}

func (s *Store) InitialAccessConfigured() (bool, error) {
	return s.GetBoolSetting(settingInitialAccessConfigured, false)
}

func (s *Store) SetLastHistorySync(ts time.Time) error {
	return s.setSetting(settingLastHistorySync, strconv.FormatInt(ts.Unix(), 10))
}

func (s *Store) LastHistorySync() (*time.Time, error) {
	value, err := s.getSetting(settingLastHistorySync)
	if err != nil || value == "" {
		return nil, err
	}
	unixTS, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, nil
	}
	t := time.Unix(unixTS, 0)
	return &t, nil
}

func (s *Store) EnsureChat(jid, name string, isGroup bool, messageTime time.Time, preview string, duringInitialSync bool) (ChatRecord, error) {
	if strings.TrimSpace(jid) == "" {
		return ChatRecord{}, errors.New("jid required")
	}
	if messageTime.IsZero() {
		messageTime = time.Now()
	}
	if strings.TrimSpace(name) == "" {
		name = jid
	}
	preview = strings.TrimSpace(preview)

	existing, err := s.GetChat(jid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ChatRecord{}, err
	}
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		initialConfigured, _ := s.InitialAccessConfigured()
		locked := true
		if !duringInitialSync && initialConfigured {
			locked = false
		}
		record := ChatRecord{
			JID:                jid,
			Name:               name,
			IsGroup:            isGroup,
			Locked:             locked,
			FirstSeenAt:        messageTime,
			LastMessageAt:      messageTime,
			LastMessagePreview: preview,
		}
		_, err = s.db.Exec(`
INSERT INTO chats (jid, name, is_group, locked, first_seen_at, last_message_at, last_message_preview)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, record.JID, record.Name, boolToInt(record.IsGroup), boolToInt(record.Locked), record.FirstSeenAt.Unix(), record.LastMessageAt.Unix(), record.LastMessagePreview)
		return record, err
	}

	record := existing
	if record.Name == "" || record.Name == record.JID {
		record.Name = name
	} else if name != "" && record.Name != name && !isNumericName(record.Name) {
		// Keep the existing human name; a new numeric/alt name doesn't override it.
	} else if name != "" {
		record.Name = name
	}
	record.IsGroup = isGroup
	if record.FirstSeenAt.IsZero() {
		record.FirstSeenAt = messageTime
	}
	if messageTime.After(record.LastMessageAt) {
		record.LastMessageAt = messageTime
		if preview != "" {
			record.LastMessagePreview = preview
		}
	} else if record.LastMessagePreview == "" && preview != "" {
		record.LastMessagePreview = preview
	}

	_, err = s.db.Exec(`
UPDATE chats
SET name = ?, is_group = ?, first_seen_at = ?, last_message_at = ?, last_message_preview = ?
WHERE jid = ?
`, record.Name, boolToInt(record.IsGroup), record.FirstSeenAt.Unix(), record.LastMessageAt.Unix(), record.LastMessagePreview, record.JID)
	return record, err
}

func isNumericName(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func (s *Store) GetChat(jid string) (ChatRecord, error) {
	var record ChatRecord
	var isGroup, locked int
	var firstSeenAt, lastMessageAt int64
	err := s.db.QueryRow(`
SELECT jid, name, is_group, locked, first_seen_at, last_message_at, last_message_preview
FROM chats
WHERE jid = ?
`, jid).Scan(&record.JID, &record.Name, &isGroup, &locked, &firstSeenAt, &lastMessageAt, &record.LastMessagePreview)
	if err != nil {
		return ChatRecord{}, err
	}
	record.IsGroup = intToBool(isGroup)
	record.Locked = intToBool(locked)
	record.FirstSeenAt = time.Unix(firstSeenAt, 0)
	record.LastMessageAt = time.Unix(lastMessageAt, 0)
	return record, nil
}

func (s *Store) ListChats(filter string, limit int, query string) ([]ChatRecord, error) {
	base := `
SELECT jid, name, is_group, locked, first_seen_at, last_message_at, last_message_preview
FROM chats
`
	var clauses []string
	var args []any
	switch filter {
	case "locked":
		clauses = append(clauses, "locked = 1")
	case "unlocked":
		clauses = append(clauses, "locked = 0")
	case "groups":
		clauses = append(clauses, "is_group = 1")
	case "dms":
		clauses = append(clauses, "is_group = 0")
	case "", "all":
	default:
		return nil, fmt.Errorf("unsupported chat filter %q", filter)
	}
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		clauses = append(clauses, "(LOWER(name) LIKE ? OR LOWER(jid) LIKE ?)")
		needle := "%" + strings.ToLower(trimmed) + "%"
		args = append(args, needle, needle)
	}
	if len(clauses) > 0 {
		base += " WHERE " + strings.Join(clauses, " AND ")
	}
	base += " ORDER BY last_message_at DESC"
	if limit > 0 {
		base += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []ChatRecord
	for rows.Next() {
		var record ChatRecord
		var isGroup, locked int
		var firstSeenAt, lastMessageAt int64
		if err := rows.Scan(&record.JID, &record.Name, &isGroup, &locked, &firstSeenAt, &lastMessageAt, &record.LastMessagePreview); err != nil {
			return nil, err
		}
		record.IsGroup = intToBool(isGroup)
		record.Locked = intToBool(locked)
		record.FirstSeenAt = time.Unix(firstSeenAt, 0)
		record.LastMessageAt = time.Unix(lastMessageAt, 0)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) SetChatLocked(jid string, locked bool) error {
	result, err := s.db.Exec(`UPDATE chats SET locked = ? WHERE jid = ?`, boolToInt(locked), jid)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	action := "unlocked"
	if locked {
		action = "locked"
	}
	return s.AddAppLog("info", "access", fmt.Sprintf("Chat %s %s", jid, action), map[string]any{
		"jid":    jid,
		"locked": locked,
	})
}

func (s *Store) ConfigureInitialAccess(unlockedJIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE chats SET locked = 1`); err != nil {
		return err
	}
	if len(unlockedJIDs) > 0 {
		placeholders := make([]string, 0, len(unlockedJIDs))
		args := make([]any, 0, len(unlockedJIDs))
		for _, jid := range unlockedJIDs {
			placeholders = append(placeholders, "?")
			args = append(args, jid)
		}
		query := fmt.Sprintf(`UPDATE chats SET locked = 0 WHERE jid IN (%s)`, strings.Join(placeholders, ","))
		if _, err := tx.Exec(query, args...); err != nil {
			return err
		}
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
`, settingInitialAccessConfigured, strconv.FormatBool(true), now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.AddAppLog("info", "access", fmt.Sprintf("Initial access configured with %d unlocked chats", len(unlockedJIDs)), map[string]any{
		"unlocked_jids": unlockedJIDs,
	})
}

func defaultAssistantSettings() AssistantSettings {
	return AssistantSettings{
		AssistantName:    "WACLI",
		PreferredRuntime: "openclaw",
		CodexModel:       "openai-codex/gpt-5.4",
		ClaudeModel:      "claude-sonnet-4-6",
		OpenClawCommand:  "openclaw",
		KarmaProvider:    "openai",
		KarmaModel:       "gpt-4.1-mini",
	}
}

func (s *Store) GetAssistantSettings() (AssistantSettings, error) {
	settings := defaultAssistantSettings()
	raw, err := s.getSetting(settingAssistantSettings)
	if err != nil {
		return settings, err
	}
	if strings.TrimSpace(raw) == "" {
		return settings, nil
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return defaultAssistantSettings(), nil
	}
	if strings.TrimSpace(settings.AssistantName) == "" {
		settings.AssistantName = "WACLI"
	}
	if strings.TrimSpace(settings.PreferredRuntime) == "" {
		settings.PreferredRuntime = "openclaw"
	}
	if strings.TrimSpace(settings.CodexModel) == "" {
		settings.CodexModel = "openai-codex/gpt-5.4"
	}
	if strings.TrimSpace(settings.ClaudeModel) == "" {
		settings.ClaudeModel = "claude-sonnet-4-6"
	}
	if strings.TrimSpace(settings.OpenClawCommand) == "" {
		settings.OpenClawCommand = "openclaw"
	}
	if strings.TrimSpace(settings.KarmaProvider) == "" {
		settings.KarmaProvider = "openai"
	}
	if strings.TrimSpace(settings.KarmaModel) == "" {
		settings.KarmaModel = "gpt-4.1-mini"
	}
	return settings, nil
}

func (s *Store) PutAssistantSettings(settings AssistantSettings) error {
	if strings.TrimSpace(settings.AssistantName) == "" {
		settings.AssistantName = "WACLI"
	}
	if strings.TrimSpace(settings.PreferredRuntime) == "" {
		settings.PreferredRuntime = "openclaw"
	}
	payload, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := s.setSetting(settingAssistantSettings, string(payload)); err != nil {
		return err
	}
	return s.AddAppLog("info", "assistant", "Assistant settings updated", map[string]any{
		"preferred_runtime": settings.PreferredRuntime,
		"assistant_name":    settings.AssistantName,
	})
}

func (s *Store) AddAppLog(level, category, message string, details any) error {
	level = strings.TrimSpace(strings.ToLower(level))
	if level == "" {
		level = "info"
	}
	category = strings.TrimSpace(strings.ToLower(category))
	if category == "" {
		category = "app"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	detailsJSON := ""
	if details != nil {
		if data, err := json.Marshal(details); err == nil {
			detailsJSON = truncateString(string(data), 32000)
		}
	}
	_, err := s.db.Exec(`
INSERT INTO app_logs (level, category, message, details_json, created_at)
VALUES (?, ?, ?, ?, ?)
`, level, category, message, detailsJSON, time.Now().Unix())
	return err
}

func (s *Store) ListAppLogs(limit int, level, category, query string) ([]AppLogRecord, error) {
	base := `
SELECT id, level, category, message, details_json, created_at
FROM app_logs
`
	var clauses []string
	var args []any
	if trimmed := strings.TrimSpace(level); trimmed != "" {
		clauses = append(clauses, "level = ?")
		args = append(args, strings.ToLower(trimmed))
	}
	if trimmed := strings.TrimSpace(category); trimmed != "" {
		clauses = append(clauses, "category = ?")
		args = append(args, strings.ToLower(trimmed))
	}
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		needle := "%" + strings.ToLower(trimmed) + "%"
		clauses = append(clauses, "(LOWER(message) LIKE ? OR LOWER(details_json) LIKE ?)")
		args = append(args, needle, needle)
	}
	if len(clauses) > 0 {
		base += " WHERE " + strings.Join(clauses, " AND ")
	}
	base += " ORDER BY created_at DESC"
	if limit > 0 {
		base += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []AppLogRecord
	for rows.Next() {
		var record AppLogRecord
		var createdAt int64
		if err := rows.Scan(&record.ID, &record.Level, &record.Category, &record.Message, &record.DetailsJSON, &createdAt); err != nil {
			return nil, err
		}
		record.CreatedAt = time.Unix(createdAt, 0)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) StoreMessage(record MessageRecord) error {
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	_, err := s.db.Exec(`
INSERT INTO messages (
	id, chat_jid, sender_jid, content, timestamp, is_from_me, message_type,
	media_type, mime_type, file_name, media_path, url, direct_path, file_length,
	media_key, file_sha256, file_enc_sha256
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id, chat_jid) DO UPDATE SET
	sender_jid = excluded.sender_jid,
	content = excluded.content,
	timestamp = excluded.timestamp,
	is_from_me = excluded.is_from_me,
	message_type = excluded.message_type,
	media_type = excluded.media_type,
	mime_type = excluded.mime_type,
	file_name = excluded.file_name,
	media_path = CASE WHEN excluded.media_path != '' THEN excluded.media_path ELSE messages.media_path END,
	url = excluded.url,
	direct_path = excluded.direct_path,
	file_length = excluded.file_length,
	media_key = excluded.media_key,
	file_sha256 = excluded.file_sha256,
	file_enc_sha256 = excluded.file_enc_sha256
`, record.ID, record.ChatJID, record.SenderJID, record.Content, record.Timestamp.Unix(), boolToInt(record.IsFromMe), record.MessageType,
		record.MediaType, record.MimeType, record.FileName, record.MediaPath, record.URL, record.DirectPath, int64(record.FileLength),
		record.MediaKey, record.FileSHA256, record.FileEncSHA256)
	return err
}

func (s *Store) ListMessages(chatJID string, limit int) ([]MessageRecord, error) {
	opts := MessageSearchOptions{
		ChatJID: chatJID,
		Limit:   limit,
	}
	return s.SearchMessages(opts)
}

func (s *Store) SearchMessages(opts MessageSearchOptions) ([]MessageRecord, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	query := `
SELECT id, chat_jid, sender_jid, content, timestamp, is_from_me, message_type,
       media_type, mime_type, file_name, media_path, url, direct_path, file_length,
       media_key, file_sha256, file_enc_sha256
FROM messages
`
	args := []any{}
	var clauses []string
	if opts.ChatJID != "" {
		clauses = append(clauses, "chat_jid = ?")
		args = append(args, opts.ChatJID)
	}
	if opts.SenderJID != "" {
		clauses = append(clauses, "sender_jid = ?")
		args = append(args, opts.SenderJID)
	}
	if trimmed := strings.TrimSpace(opts.Query); trimmed != "" {
		clauses = append(clauses, "(LOWER(content) LIKE ? OR LOWER(file_name) LIKE ? OR LOWER(media_type) LIKE ? OR LOWER(message_type) LIKE ?)")
		needle := "%" + strings.ToLower(trimmed) + "%"
		args = append(args, needle, needle, needle, needle)
	}
	if opts.FromMe != nil {
		clauses = append(clauses, "is_from_me = ?")
		args = append(args, boolToInt(*opts.FromMe))
	}
	if opts.MediaOnly {
		clauses = append(clauses, "media_type != ''")
	}
	if opts.Before != nil {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, opts.Before.Unix())
	}
	if opts.After != nil {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, opts.After.Unix())
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, opts.Limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []MessageRecord
	for rows.Next() {
		record, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	return records, nil
}

func scanMessage(scanner interface {
	Scan(dest ...any) error
}) (MessageRecord, error) {
	var record MessageRecord
	var timestamp int64
	var isFromMe int
	var fileLength int64
	if err := scanner.Scan(
		&record.ID,
		&record.ChatJID,
		&record.SenderJID,
		&record.Content,
		&timestamp,
		&isFromMe,
		&record.MessageType,
		&record.MediaType,
		&record.MimeType,
		&record.FileName,
		&record.MediaPath,
		&record.URL,
		&record.DirectPath,
		&fileLength,
		&record.MediaKey,
		&record.FileSHA256,
		&record.FileEncSHA256,
	); err != nil {
		return MessageRecord{}, err
	}
	record.Timestamp = time.Unix(timestamp, 0)
	record.IsFromMe = intToBool(isFromMe)
	record.FileLength = uint64(fileLength)
	return record, nil
}

func (s *Store) GetMessage(messageID, chatJID string) (MessageRecord, error) {
	row := s.db.QueryRow(`
SELECT id, chat_jid, sender_jid, content, timestamp, is_from_me, message_type,
       media_type, mime_type, file_name, media_path, url, direct_path, file_length,
       media_key, file_sha256, file_enc_sha256
FROM messages
WHERE id = ? AND chat_jid = ?
`, messageID, chatJID)
	return scanMessage(row)
}

func (s *Store) LatestMessage() (MessageRecord, error) {
	row := s.db.QueryRow(`
SELECT id, chat_jid, sender_jid, content, timestamp, is_from_me, message_type,
       media_type, mime_type, file_name, media_path, url, direct_path, file_length,
       media_key, file_sha256, file_enc_sha256
FROM messages
ORDER BY timestamp DESC
LIMIT 1
`)
	return scanMessage(row)
}

func (s *Store) UpdateMessageMediaPath(messageID, chatJID, mediaPath string) error {
	_, err := s.db.Exec(`UPDATE messages SET media_path = ? WHERE id = ? AND chat_jid = ?`, mediaPath, messageID, chatJID)
	return err
}

func (s *Store) UpsertContact(record ContactRecord) error {
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now()
	}
	_, err := s.db.Exec(`
INSERT INTO contacts (
	jid, phone, full_name, first_name, push_name, business_name, found,
	bio, notes, memory, metadata_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET
	phone = excluded.phone,
	full_name = excluded.full_name,
	first_name = excluded.first_name,
	push_name = excluded.push_name,
	business_name = excluded.business_name,
	found = excluded.found,
	updated_at = excluded.updated_at
`, record.JID, record.Phone, record.FullName, record.FirstName, record.PushName, record.BusinessName, boolToInt(record.Found),
		record.Bio, record.Notes, record.Memory, record.MetadataJSON, record.UpdatedAt.Unix())
	return err
}

func (s *Store) GetContact(jid string) (ContactRecord, error) {
	var record ContactRecord
	var found int
	var updatedAt int64
	err := s.db.QueryRow(`
SELECT jid, phone, full_name, first_name, push_name, business_name, found, bio, notes, memory, metadata_json, updated_at
FROM contacts
WHERE jid = ?
`, jid).Scan(
		&record.JID,
		&record.Phone,
		&record.FullName,
		&record.FirstName,
		&record.PushName,
		&record.BusinessName,
		&found,
		&record.Bio,
		&record.Notes,
		&record.Memory,
		&record.MetadataJSON,
		&updatedAt,
	)
	if err != nil {
		return ContactRecord{}, err
	}
	record.Found = intToBool(found)
	record.UpdatedAt = time.Unix(updatedAt, 0)
	return record, nil
}

func (s *Store) UpsertContactMemory(jid string, update ContactUpdate) (ContactRecord, error) {
	record, err := s.GetContact(jid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ContactRecord{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		record = ContactRecord{
			JID:       jid,
			Phone:     normalizePhone(strings.TrimSuffix(jid, "@s.whatsapp.net")),
			Found:     true,
			UpdatedAt: time.Now(),
		}
	}
	if update.Bio != nil {
		record.Bio = *update.Bio
	}
	if update.Notes != nil {
		record.Notes = *update.Notes
	}
	if update.Memory != nil {
		record.Memory = *update.Memory
	}
	if update.MetadataJSON != nil {
		record.MetadataJSON = *update.MetadataJSON
	}
	record.UpdatedAt = time.Now()
	if _, err := s.db.Exec(`
INSERT INTO contacts (
	jid, phone, full_name, first_name, push_name, business_name, found,
	bio, notes, memory, metadata_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET
	bio = excluded.bio,
	notes = excluded.notes,
	memory = excluded.memory,
	metadata_json = excluded.metadata_json,
	updated_at = excluded.updated_at
`, record.JID, record.Phone, record.FullName, record.FirstName, record.PushName, record.BusinessName, boolToInt(record.Found),
		record.Bio, record.Notes, record.Memory, record.MetadataJSON, record.UpdatedAt.Unix()); err != nil {
		return ContactRecord{}, err
	}
	return s.GetContact(jid)
}

func (s *Store) ListContacts(limit int, query string) ([]ContactRecord, error) {
	base := `
SELECT jid, phone, full_name, first_name, push_name, business_name, found, bio, notes, memory, metadata_json, updated_at
FROM contacts
`
	var args []any
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		base += ` WHERE LOWER(full_name) LIKE ? OR LOWER(first_name) LIKE ? OR LOWER(push_name) LIKE ? OR LOWER(phone) LIKE ? OR LOWER(jid) LIKE ?`
		needle := "%" + strings.ToLower(trimmed) + "%"
		args = append(args, needle, needle, needle, needle, needle)
	}
	base += ` ORDER BY CASE WHEN full_name != '' THEN full_name WHEN push_name != '' THEN push_name ELSE jid END ASC`
	if limit > 0 {
		base += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.Query(base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []ContactRecord
	for rows.Next() {
		var record ContactRecord
		var found int
		var updatedAt int64
		if err := rows.Scan(
			&record.JID,
			&record.Phone,
			&record.FullName,
			&record.FirstName,
			&record.PushName,
			&record.BusinessName,
			&found,
			&record.Bio,
			&record.Notes,
			&record.Memory,
			&record.MetadataJSON,
			&updatedAt,
		); err != nil {
			return nil, err
		}
		record.Found = intToBool(found)
		record.UpdatedAt = time.Unix(updatedAt, 0)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) AddWebhook(record WebhookRecord) (WebhookRecord, error) {
	if strings.TrimSpace(record.URL) == "" {
		return WebhookRecord{}, errors.New("url required")
	}
	record, err := normalizeWebhookRecord(record)
	if err != nil {
		return WebhookRecord{}, err
	}
	eventJSON, err := json.Marshal(record.Events)
	if err != nil {
		return WebhookRecord{}, err
	}
	chatJIDsJSON, err := json.Marshal(record.ChatJIDs)
	if err != nil {
		return WebhookRecord{}, err
	}
	messageTypesJSON, err := json.Marshal(record.MessageTypes)
	if err != nil {
		return WebhookRecord{}, err
	}
	now := time.Now()
	result, err := s.db.Exec(`
INSERT INTO webhooks (url, secret, events, scope, chat_jids, message_types, context_limit, enabled, include_mentions, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, strings.TrimSpace(record.URL), strings.TrimSpace(record.Secret), string(eventJSON), record.Scope, string(chatJIDsJSON), string(messageTypesJSON), record.ContextLimit, boolToInt(record.Enabled), boolToInt(record.IncludeMentions), now.Unix(), now.Unix())
	if err != nil {
		return WebhookRecord{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return WebhookRecord{}, err
	}
	record.ID = id
	record.CreatedAt = now
	record.UpdatedAt = now
	return record, nil
}

func (s *Store) ListWebhooks() ([]WebhookRecord, error) {
	rows, err := s.db.Query(`SELECT id, url, secret, events, scope, chat_jids, message_types, context_limit, enabled, include_mentions, created_at, updated_at FROM webhooks ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []WebhookRecord
	for rows.Next() {
		var record WebhookRecord
		var eventsJSON, chatJIDsJSON, messageTypesJSON string
		var enabled, includeMentions int
		var createdAt, updatedAt int64
		if err := rows.Scan(&record.ID, &record.URL, &record.Secret, &eventsJSON, &record.Scope, &chatJIDsJSON, &messageTypesJSON, &record.ContextLimit, &enabled, &includeMentions, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		record.Enabled = intToBool(enabled)
		record.IncludeMentions = intToBool(includeMentions)
		record.CreatedAt = time.Unix(createdAt, 0)
		record.UpdatedAt = time.Unix(updatedAt, 0)
		if err := json.Unmarshal([]byte(eventsJSON), &record.Events); err != nil {
			record.Events = []string{}
		}
		if err := json.Unmarshal([]byte(chatJIDsJSON), &record.ChatJIDs); err != nil {
			record.ChatJIDs = []string{}
		}
		if err := json.Unmarshal([]byte(messageTypesJSON), &record.MessageTypes); err != nil {
			record.MessageTypes = []string{"*"}
		}
		record, err = normalizeWebhookRecord(record)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) DeleteWebhook(id int64) error {
	_, err := s.db.Exec(`DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

func (s *Store) StartWebhookDelivery(webhookID int64, url, event, chatJID, messageID, requestBody string) (int64, error) {
	now := time.Now()
	result, err := s.db.Exec(`
INSERT INTO webhook_deliveries (webhook_id, url, event, chat_jid, message_id, status, http_status, last_error, response_body, request_body, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 'pending', 0, '', '', ?, ?, ?)
`, webhookID, strings.TrimSpace(url), strings.TrimSpace(event), strings.TrimSpace(chatJID), strings.TrimSpace(messageID), truncateString(requestBody, 32000), now.Unix(), now.Unix())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) FinishWebhookDelivery(id int64, status string, httpStatus int, lastError, responseBody string) error {
	if strings.TrimSpace(status) == "" {
		status = "done"
	}
	_, err := s.db.Exec(`
UPDATE webhook_deliveries
SET status = ?, http_status = ?, last_error = ?, response_body = ?, updated_at = ?
WHERE id = ?
`, status, httpStatus, truncateString(lastError, 4000), truncateString(responseBody, 16000), time.Now().Unix(), id)
	return err
}

func (s *Store) ListWebhookDeliveries(limit int, status, event, query string) ([]WebhookDeliveryRecord, error) {
	base := `
SELECT id, webhook_id, url, event, chat_jid, message_id, status, http_status, last_error, response_body, request_body, created_at, updated_at
FROM webhook_deliveries
`
	var args []any
	var clauses []string
	if trimmed := strings.TrimSpace(status); trimmed != "" {
		clauses = append(clauses, "LOWER(status) = ?")
		args = append(args, strings.ToLower(trimmed))
	}
	if trimmed := strings.TrimSpace(event); trimmed != "" {
		clauses = append(clauses, "LOWER(event) = ?")
		args = append(args, strings.ToLower(trimmed))
	}
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		needle := "%" + strings.ToLower(trimmed) + "%"
		clauses = append(clauses, "(LOWER(url) LIKE ? OR LOWER(chat_jid) LIKE ? OR LOWER(message_id) LIKE ? OR LOWER(last_error) LIKE ? OR LOWER(response_body) LIKE ?)")
		args = append(args, needle, needle, needle, needle, needle)
	}
	if len(clauses) > 0 {
		base += " WHERE " + strings.Join(clauses, " AND ")
	}
	base += " ORDER BY updated_at DESC, id DESC"
	if limit > 0 {
		base += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.Query(base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []WebhookDeliveryRecord
	for rows.Next() {
		var record WebhookDeliveryRecord
		var createdAt, updatedAt int64
		if err := rows.Scan(&record.ID, &record.WebhookID, &record.URL, &record.Event, &record.ChatJID, &record.MessageID, &record.Status, &record.HTTPStatus, &record.LastError, &record.ResponseBody, &record.RequestBody, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		record.CreatedAt = time.Unix(createdAt, 0)
		record.UpdatedAt = time.Unix(updatedAt, 0)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) AddOpenClawBridge(record OpenClawBridgeRecord) (OpenClawBridgeRecord, error) {
	record, err := normalizeOpenClawBridgeRecord(record)
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	chatJIDsJSON, err := json.Marshal(record.ChatJIDs)
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	messageTypesJSON, err := json.Marshal(record.MessageTypes)
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	now := time.Now()
	result, err := s.db.Exec(`
INSERT INTO openclaw_bridges (command, scope, chat_jids, message_types, context_limit, instruction, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, record.Command, record.Scope, string(chatJIDsJSON), string(messageTypesJSON), record.ContextLimit, record.Instruction, boolToInt(record.Enabled), now.Unix(), now.Unix())
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	record.ID = id
	record.CreatedAt = now
	record.UpdatedAt = now
	return record, nil
}

func (s *Store) GetOpenClawBridge(id int64) (OpenClawBridgeRecord, error) {
	row := s.db.QueryRow(`
SELECT id, command, scope, chat_jids, message_types, context_limit, instruction, enabled, created_at, updated_at
FROM openclaw_bridges
WHERE id = ?
`, id)
	return scanOpenClawBridge(row)
}

func (s *Store) UpdateOpenClawBridge(record OpenClawBridgeRecord) (OpenClawBridgeRecord, error) {
	if record.ID <= 0 {
		return OpenClawBridgeRecord{}, errors.New("openclaw bridge id required")
	}
	record, err := normalizeOpenClawBridgeRecord(record)
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	chatJIDsJSON, err := json.Marshal(record.ChatJIDs)
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	messageTypesJSON, err := json.Marshal(record.MessageTypes)
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	now := time.Now()
	result, err := s.db.Exec(`
UPDATE openclaw_bridges
SET command = ?, scope = ?, chat_jids = ?, message_types = ?, context_limit = ?, instruction = ?, enabled = ?, updated_at = ?
WHERE id = ?
`, record.Command, record.Scope, string(chatJIDsJSON), string(messageTypesJSON), record.ContextLimit, record.Instruction, boolToInt(record.Enabled), now.Unix(), record.ID)
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	if rows == 0 {
		return OpenClawBridgeRecord{}, sql.ErrNoRows
	}
	return s.GetOpenClawBridge(record.ID)
}

func (s *Store) ListOpenClawBridges() ([]OpenClawBridgeRecord, error) {
	rows, err := s.db.Query(`
SELECT id, command, scope, chat_jids, message_types, context_limit, instruction, enabled, created_at, updated_at
FROM openclaw_bridges
ORDER BY id ASC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []OpenClawBridgeRecord
	for rows.Next() {
		record, err := scanOpenClawBridge(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func scanOpenClawBridge(scanner interface {
	Scan(dest ...any) error
}) (OpenClawBridgeRecord, error) {
	var record OpenClawBridgeRecord
	var chatJIDsJSON, messageTypesJSON string
	var enabled int
	var createdAt, updatedAt int64
	if err := scanner.Scan(&record.ID, &record.Command, &record.Scope, &chatJIDsJSON, &messageTypesJSON, &record.ContextLimit, &record.Instruction, &enabled, &createdAt, &updatedAt); err != nil {
		return OpenClawBridgeRecord{}, err
	}
	record.Enabled = intToBool(enabled)
	record.CreatedAt = time.Unix(createdAt, 0)
	record.UpdatedAt = time.Unix(updatedAt, 0)
	if err := json.Unmarshal([]byte(chatJIDsJSON), &record.ChatJIDs); err != nil {
		record.ChatJIDs = []string{}
	}
	if err := json.Unmarshal([]byte(messageTypesJSON), &record.MessageTypes); err != nil {
		record.MessageTypes = []string{"*"}
	}
	return normalizeOpenClawBridgeRecord(record)
}

func (s *Store) DeleteOpenClawBridge(id int64) error {
	_, err := s.db.Exec(`DELETE FROM openclaw_bridges WHERE id = ?`, id)
	return err
}

func (s *Store) GetOrCreateOpenClawSession(chatJID string) (OpenClawSessionRecord, error) {
	chatJID = strings.TrimSpace(chatJID)
	if chatJID == "" {
		return OpenClawSessionRecord{}, errors.New("chat_jid required")
	}
	if session, err := s.GetOpenClawSession(chatJID); err == nil {
		_, _ = s.db.Exec(`UPDATE openclaw_sessions SET updated_at = ? WHERE chat_jid = ?`, time.Now().Unix(), chatJID)
		session.UpdatedAt = time.Now()
		return session, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return OpenClawSessionRecord{}, err
	}
	now := time.Now()
	sessionID := uuid.NewString()
	_, err := s.db.Exec(`
INSERT INTO openclaw_sessions (chat_jid, session_id, created_at, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(chat_jid) DO NOTHING
`, chatJID, sessionID, now.Unix(), now.Unix())
	if err != nil {
		return OpenClawSessionRecord{}, err
	}
	return s.GetOpenClawSession(chatJID)
}

func (s *Store) GetOpenClawSession(chatJID string) (OpenClawSessionRecord, error) {
	var record OpenClawSessionRecord
	var createdAt, updatedAt int64
	err := s.db.QueryRow(`
SELECT s.chat_jid, COALESCE(c.name, ''), s.session_id, s.created_at, s.updated_at
FROM openclaw_sessions s
LEFT JOIN chats c ON c.jid = s.chat_jid
WHERE s.chat_jid = ?
`, chatJID).Scan(&record.ChatJID, &record.ChatName, &record.SessionID, &createdAt, &updatedAt)
	if err != nil {
		return OpenClawSessionRecord{}, err
	}
	record.CreatedAt = time.Unix(createdAt, 0)
	record.UpdatedAt = time.Unix(updatedAt, 0)
	return record, nil
}

func (s *Store) ListOpenClawSessions(limit int, query string) ([]OpenClawSessionRecord, error) {
	base := `
SELECT s.chat_jid, COALESCE(c.name, ''), s.session_id, s.created_at, s.updated_at
FROM openclaw_sessions s
LEFT JOIN chats c ON c.jid = s.chat_jid
`
	var args []any
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		base += ` WHERE LOWER(s.chat_jid) LIKE ? OR LOWER(COALESCE(c.name, '')) LIKE ? OR LOWER(s.session_id) LIKE ?`
		needle := "%" + strings.ToLower(trimmed) + "%"
		args = append(args, needle, needle, needle)
	}
	base += ` ORDER BY s.updated_at DESC`
	if limit > 0 {
		base += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []OpenClawSessionRecord
	for rows.Next() {
		var record OpenClawSessionRecord
		var createdAt, updatedAt int64
		if err := rows.Scan(&record.ChatJID, &record.ChatName, &record.SessionID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		record.CreatedAt = time.Unix(createdAt, 0)
		record.UpdatedAt = time.Unix(updatedAt, 0)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) TryStartOpenClawDelivery(bridgeID int64, chatJID, messageID, sessionID, command, requestMessage string) (bool, error) {
	now := time.Now()
	result, err := s.db.Exec(`
INSERT OR IGNORE INTO openclaw_deliveries (bridge_id, chat_jid, message_id, session_id, command, request_message, response_output, status, last_error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, '', 'pending', '', ?, ?)
`, bridgeID, chatJID, messageID, sessionID, strings.TrimSpace(command), truncateString(requestMessage, 32000), now.Unix(), now.Unix())
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *Store) FinishOpenClawDelivery(bridgeID int64, chatJID, messageID, status, lastError, responseOutput string) error {
	if strings.TrimSpace(status) == "" {
		status = "done"
	}
	_, err := s.db.Exec(`
UPDATE openclaw_deliveries
SET status = ?, last_error = ?, response_output = ?, updated_at = ?
WHERE bridge_id = ? AND chat_jid = ? AND message_id = ?
`, status, truncateString(lastError, 4000), truncateString(responseOutput, 16000), time.Now().Unix(), bridgeID, chatJID, messageID)
	return err
}

func (s *Store) ListOpenClawDeliveries(limit int, status, query string) ([]OpenClawDeliveryRecord, error) {
	base := `
SELECT d.bridge_id, d.chat_jid, COALESCE(c.name, ''), d.message_id, d.session_id, d.command, d.status, d.last_error, d.request_message, d.response_output, d.created_at, d.updated_at
FROM openclaw_deliveries d
LEFT JOIN chats c ON c.jid = d.chat_jid
`
	var args []any
	var clauses []string
	if trimmed := strings.TrimSpace(status); trimmed != "" {
		clauses = append(clauses, "LOWER(d.status) = ?")
		args = append(args, strings.ToLower(trimmed))
	}
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		needle := "%" + strings.ToLower(trimmed) + "%"
		clauses = append(clauses, "(LOWER(d.chat_jid) LIKE ? OR LOWER(COALESCE(c.name, '')) LIKE ? OR LOWER(d.message_id) LIKE ? OR LOWER(d.session_id) LIKE ? OR LOWER(d.last_error) LIKE ? OR LOWER(d.response_output) LIKE ?)")
		args = append(args, needle, needle, needle, needle, needle, needle)
	}
	if len(clauses) > 0 {
		base += " WHERE " + strings.Join(clauses, " AND ")
	}
	base += " ORDER BY d.updated_at DESC, d.created_at DESC"
	if limit > 0 {
		base += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []OpenClawDeliveryRecord
	for rows.Next() {
		var record OpenClawDeliveryRecord
		var createdAt, updatedAt int64
		if err := rows.Scan(&record.BridgeID, &record.ChatJID, &record.ChatName, &record.MessageID, &record.SessionID, &record.Command, &record.Status, &record.LastError, &record.RequestMessage, &record.ResponseOutput, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		record.CreatedAt = time.Unix(createdAt, 0)
		record.UpdatedAt = time.Unix(updatedAt, 0)
		records = append(records, record)
	}
	return records, rows.Err()
}

func normalizeWebhookRecord(record WebhookRecord) (WebhookRecord, error) {
	if len(record.Events) == 0 {
		record.Events = []string{"incoming_message"}
	}
	record.Events = normalizeStringList(record.Events, true)
	if len(record.Events) == 0 {
		record.Events = []string{"incoming_message"}
	}
	if strings.TrimSpace(record.Scope) == "" {
		record.Scope = "all_unlocked"
	}
	record.Scope = strings.ToLower(strings.TrimSpace(record.Scope))
	switch record.Scope {
	case "all", "all_unlocked", "all-unlocked":
		record.Scope = "all_unlocked"
	case "chat", "single_chat", "selected_chats", "selected-chats":
		record.Scope = "selected_chats"
	default:
		return WebhookRecord{}, fmt.Errorf("unsupported webhook scope %q", record.Scope)
	}
	record.ChatJIDs = normalizeStringList(record.ChatJIDs, false)
	if record.Scope == "selected_chats" && len(record.ChatJIDs) == 0 {
		return WebhookRecord{}, errors.New("chat_jids required when webhook scope is selected_chats")
	}
	record.MessageTypes = normalizeStringList(record.MessageTypes, true)
	if len(record.MessageTypes) == 0 {
		record.MessageTypes = []string{"*"}
	}
	for idx, value := range record.MessageTypes {
		record.MessageTypes[idx] = normalizeWebhookMessageType(value)
	}
	record.MessageTypes = normalizeStringList(record.MessageTypes, true)
	if record.ContextLimit <= 0 {
		record.ContextLimit = 12
	}
	if record.ContextLimit > 100 {
		record.ContextLimit = 100
	}
	return record, nil
}

func normalizeOpenClawBridgeRecord(record OpenClawBridgeRecord) (OpenClawBridgeRecord, error) {
	if strings.TrimSpace(record.Command) == "" {
		record.Command = "openclaw"
	}
	if strings.TrimSpace(record.Scope) == "" {
		record.Scope = "all_unlocked"
	}
	record.Scope = strings.ToLower(strings.TrimSpace(record.Scope))
	switch record.Scope {
	case "all", "all_unlocked", "all-unlocked":
		record.Scope = "all_unlocked"
	case "chat", "single_chat", "selected_chats", "selected-chats":
		record.Scope = "selected_chats"
	default:
		return OpenClawBridgeRecord{}, fmt.Errorf("unsupported openclaw bridge scope %q", record.Scope)
	}
	record.ChatJIDs = normalizeStringList(record.ChatJIDs, false)
	if record.Scope == "selected_chats" && len(record.ChatJIDs) == 0 {
		return OpenClawBridgeRecord{}, errors.New("chat_jids required when openclaw bridge scope is selected_chats")
	}
	record.MessageTypes = normalizeStringList(record.MessageTypes, true)
	if len(record.MessageTypes) == 0 {
		record.MessageTypes = []string{"*"}
	}
	for idx, value := range record.MessageTypes {
		record.MessageTypes[idx] = normalizeWebhookMessageType(value)
	}
	record.MessageTypes = normalizeStringList(record.MessageTypes, true)
	if record.ContextLimit <= 0 {
		record.ContextLimit = 12
	}
	if record.ContextLimit > 100 {
		record.ContextLimit = 100
	}
	record.Instruction = strings.TrimSpace(record.Instruction)
	if record.Instruction == "" {
		record.Instruction = defaultOpenClawInstruction(defaultAssistantSettings())
	}
	return record, nil
}

func normalizeStringList(values []string, lower bool) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if lower {
			value = strings.ToLower(value)
		}
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeWebhookMessageType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "msg", "msgs", "message", "messages":
		return "message"
	case "text", "texts":
		return "text"
	case "img", "image", "images", "photo", "photos":
		return "image"
	case "vid", "video", "videos":
		return "video"
	case "doc", "docs", "document", "documents", "file", "files":
		return "document"
	case "audio", "audios", "voice", "voices", "voice_note", "voice_notes":
		return "audio"
	case "sticker", "stickers":
		return "sticker"
	case "media", "attachment", "attachments":
		return "media"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func (s *Store) AddAutoReply(rule AutoReplyRule) (AutoReplyRule, error) {
	if strings.TrimSpace(rule.Name) == "" {
		return AutoReplyRule{}, errors.New("name required")
	}
	if strings.TrimSpace(rule.MatchType) == "" {
		rule.MatchType = "always"
	}
	if !rule.ApplyToDMs && !rule.ApplyToGroups {
		rule.ApplyToDMs = true
	}
	now := time.Now()
	result, err := s.db.Exec(`
INSERT INTO auto_replies (
	name, match_type, pattern, reply_text, media_path, enabled,
	apply_to_dms, apply_to_groups, priority, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, rule.Name, strings.ToLower(strings.TrimSpace(rule.MatchType)), rule.Pattern, rule.ReplyText, rule.MediaPath,
		boolToInt(rule.Enabled), boolToInt(rule.ApplyToDMs), boolToInt(rule.ApplyToGroups), rule.Priority, now.Unix(), now.Unix())
	if err != nil {
		return AutoReplyRule{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AutoReplyRule{}, err
	}
	rule.ID = id
	rule.CreatedAt = now
	rule.UpdatedAt = now
	return rule, nil
}

func (s *Store) ListAutoReplies() ([]AutoReplyRule, error) {
	rows, err := s.db.Query(`
SELECT id, name, match_type, pattern, reply_text, media_path, enabled, apply_to_dms, apply_to_groups, priority, created_at, updated_at
FROM auto_replies
ORDER BY priority ASC, id ASC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []AutoReplyRule
	for rows.Next() {
		var rule AutoReplyRule
		var enabled, applyToDMs, applyToGroups int
		var createdAt, updatedAt int64
		if err := rows.Scan(&rule.ID, &rule.Name, &rule.MatchType, &rule.Pattern, &rule.ReplyText, &rule.MediaPath, &enabled, &applyToDMs, &applyToGroups, &rule.Priority, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		rule.Enabled = intToBool(enabled)
		rule.ApplyToDMs = intToBool(applyToDMs)
		rule.ApplyToGroups = intToBool(applyToGroups)
		rule.CreatedAt = time.Unix(createdAt, 0)
		rule.UpdatedAt = time.Unix(updatedAt, 0)
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) DeleteAutoReply(id int64) error {
	_, err := s.db.Exec(`DELETE FROM auto_replies WHERE id = ?`, id)
	return err
}

func (s *Store) BuildStatus(connected bool, userJID string) (StatusSnapshot, error) {
	dndMode, err := s.GetDNDMode()
	if err != nil {
		return StatusSnapshot{}, err
	}
	initialConfigured, err := s.InitialAccessConfigured()
	if err != nil {
		return StatusSnapshot{}, err
	}
	lastHistorySync, err := s.LastHistorySync()
	if err != nil {
		return StatusSnapshot{}, err
	}
	var chatCount, messageCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM chats`).Scan(&chatCount); err != nil {
		return StatusSnapshot{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messageCount); err != nil {
		return StatusSnapshot{}, err
	}
	return StatusSnapshot{
		Connected:               connected,
		UserJID:                 userJID,
		DNDMode:                 dndMode,
		InitialAccessConfigured: initialConfigured,
		ChatCount:               chatCount,
		MessageCount:            messageCount,
		LastHistorySync:         lastHistorySync,
	}, nil
}
