package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const desktopDefaultHTTPAddr = "127.0.0.1:8765"

type daemonClient struct {
	baseURL    string
	httpClient *http.Client
}

func newDaemonClient() *daemonClient {
	return &daemonClient{
		baseURL:    desktopBaseURL(),
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func desktopBaseURL() string {
	addr := strings.TrimSpace(os.Getenv("WACLI_HTTP_ADDR"))
	if addr == "" {
		addr = desktopDefaultHTTPAddr
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	return "http://" + strings.TrimRight(addr, "/")
}

type StatusSnapshot struct {
	Connected               bool    `json:"connected"`
	UserJID                 string  `json:"user_jid,omitempty"`
	DNDMode                 bool    `json:"dnd_mode"`
	InitialAccessConfigured bool    `json:"initial_access_configured"`
	ChatCount               int     `json:"chat_count"`
	MessageCount            int     `json:"message_count"`
	LastHistorySync         *string `json:"last_history_sync,omitempty"`
}

type ChatRecord struct {
	JID                string `json:"jid"`
	Name               string `json:"name"`
	IsGroup            bool   `json:"is_group"`
	Locked             bool   `json:"locked"`
	FirstSeenAt        string `json:"first_seen_at"`
	LastMessageAt      string `json:"last_message_at"`
	LastMessagePreview string `json:"last_message_preview"`
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
	ID          int64  `json:"id"`
	Level       string `json:"level"`
	Category    string `json:"category"`
	Message     string `json:"message"`
	DetailsJSON string `json:"details_json,omitempty"`
	CreatedAt   string `json:"created_at"`
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
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

type OpenClawBridgeRecord struct {
	ID           int64    `json:"id"`
	Command      string   `json:"command"`
	Scope        string   `json:"scope"`
	ChatJIDs     []string `json:"chat_jids,omitempty"`
	MessageTypes []string `json:"message_types,omitempty"`
	ContextLimit int      `json:"context_limit"`
	Instruction  string   `json:"instruction"`
	Enabled      bool     `json:"enabled"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

type OpenClawDeliveryRecord struct {
	BridgeID       int64  `json:"bridge_id"`
	ChatJID        string `json:"chat_jid"`
	ChatName       string `json:"chat_name,omitempty"`
	MessageID      string `json:"message_id"`
	SessionID      string `json:"session_id"`
	Command        string `json:"command,omitempty"`
	Status         string `json:"status"`
	LastError      string `json:"last_error,omitempty"`
	RequestMessage string `json:"request_message,omitempty"`
	ResponseOutput string `json:"response_output,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type SyncResponse struct {
	OK               bool    `json:"ok"`
	HistorySeen      bool    `json:"history_seen"`
	HistoryRequested bool    `json:"history_requested"`
	RequestedAt      string  `json:"requested_at"`
	LastHistorySync  *string `json:"last_history_sync,omitempty"`
}

type LoginSessionState struct {
	Running     bool     `json:"running"`
	PairCode    bool     `json:"pair_code"`
	StartedAt   string   `json:"started_at"`
	QRPath      string   `json:"qr_path"`
	QRAvailable bool     `json:"qr_available"`
	OutputLines []string `json:"output_lines"`
	LastError   string   `json:"last_error,omitempty"`
}

func (c *daemonClient) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf(strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *daemonClient) status(ctx context.Context) (StatusSnapshot, error) {
	var response StatusSnapshot
	err := c.do(ctx, http.MethodGet, "/status", nil, nil, &response)
	return response, err
}

func (c *daemonClient) setDND(ctx context.Context, enabled bool) error {
	return c.do(ctx, http.MethodPut, "/dnd", nil, map[string]any{"enabled": enabled}, nil)
}

func (c *daemonClient) sync(ctx context.Context) (SyncResponse, error) {
	var response SyncResponse
	err := c.do(ctx, http.MethodPost, "/sync", nil, map[string]any{}, &response)
	return response, err
}

func (c *daemonClient) assistantSettings(ctx context.Context) (AssistantSettings, error) {
	var response AssistantSettings
	err := c.do(ctx, http.MethodGet, "/assistant/settings", nil, nil, &response)
	return response, err
}

func (c *daemonClient) saveAssistantSettings(ctx context.Context, settings AssistantSettings) (AssistantSettings, error) {
	var response struct {
		AssistantSettings AssistantSettings `json:"assistant_settings"`
	}
	err := c.do(ctx, http.MethodPut, "/assistant/settings", nil, settings, &response)
	return response.AssistantSettings, err
}

func (c *daemonClient) chats(ctx context.Context, filter, queryText string, limit int) ([]ChatRecord, error) {
	query := url.Values{}
	if strings.TrimSpace(filter) != "" {
		query.Set("filter", strings.TrimSpace(filter))
	}
	if strings.TrimSpace(queryText) != "" {
		query.Set("query", strings.TrimSpace(queryText))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var response struct {
		Chats []ChatRecord `json:"chats"`
	}
	err := c.do(ctx, http.MethodGet, "/chats", query, nil, &response)
	return response.Chats, err
}

func (c *daemonClient) setChatLocked(ctx context.Context, jid string, locked bool) error {
	return c.do(ctx, http.MethodPut, "/chats/"+url.PathEscape(strings.TrimSpace(jid)), nil, map[string]any{"locked": locked}, nil)
}

func (c *daemonClient) configureAccess(ctx context.Context, unlockedJIDs []string) error {
	return c.do(ctx, http.MethodPut, "/chats/access", nil, map[string]any{"unlocked_jids": unlockedJIDs}, nil)
}

func (c *daemonClient) logs(ctx context.Context, limit int) ([]AppLogRecord, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var response struct {
		Logs []AppLogRecord `json:"logs"`
	}
	err := c.do(ctx, http.MethodGet, "/logs", query, nil, &response)
	return response.Logs, err
}

func (c *daemonClient) webhooks(ctx context.Context) ([]WebhookRecord, error) {
	var response struct {
		Webhooks []WebhookRecord `json:"webhooks"`
	}
	err := c.do(ctx, http.MethodGet, "/webhooks", nil, nil, &response)
	return response.Webhooks, err
}

func (c *daemonClient) createWebhook(ctx context.Context, webhook WebhookRecord) (WebhookRecord, error) {
	var response struct {
		Webhook WebhookRecord `json:"webhook"`
	}
	err := c.do(ctx, http.MethodPost, "/webhooks", nil, webhook, &response)
	return response.Webhook, err
}

func (c *daemonClient) deleteWebhook(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, "/webhooks/"+url.PathEscape(strconv.FormatInt(id, 10)), nil, nil, nil)
}

func (c *daemonClient) openClawBridges(ctx context.Context) ([]OpenClawBridgeRecord, error) {
	var response struct {
		OpenClawBridges []OpenClawBridgeRecord `json:"openclaw_bridges"`
	}
	err := c.do(ctx, http.MethodGet, "/openclaw_bridges", nil, nil, &response)
	return response.OpenClawBridges, err
}

func (c *daemonClient) createOpenClawBridge(ctx context.Context, bridge OpenClawBridgeRecord) (OpenClawBridgeRecord, error) {
	var response struct {
		OpenClawBridge OpenClawBridgeRecord `json:"openclaw_bridge"`
	}
	err := c.do(ctx, http.MethodPost, "/openclaw_bridges", nil, bridge, &response)
	return response.OpenClawBridge, err
}

func (c *daemonClient) deleteOpenClawBridge(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, "/openclaw_bridges/"+url.PathEscape(strconv.FormatInt(id, 10)), nil, nil, nil)
}

func (c *daemonClient) openClawDeliveries(ctx context.Context, limit int) ([]OpenClawDeliveryRecord, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var response struct {
		OpenClawDeliveries []OpenClawDeliveryRecord `json:"openclaw_deliveries"`
	}
	err := c.do(ctx, http.MethodGet, "/openclaw_deliveries", query, nil, &response)
	return response.OpenClawDeliveries, err
}

func qrFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wacli", "qr.png")
}

func readQRDataURI() (string, error) {
	path := qrFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), nil
}
