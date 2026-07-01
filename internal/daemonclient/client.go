package daemonclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultHTTPAddr = "127.0.0.1:8765"

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func New(baseURL string) *Client {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL()
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func DefaultBaseURL() string {
	addr := strings.TrimSpace(os.Getenv("WACLI_HTTP_ADDR"))
	if addr == "" {
		addr = defaultHTTPAddr
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	return "http://" + strings.TrimRight(addr, "/")
}

func (c *Client) Status(ctx context.Context) (StatusSnapshot, error) {
	var response StatusSnapshot
	err := c.do(ctx, http.MethodGet, "/status", nil, nil, &response)
	return response, err
}

func (c *Client) GetDND(ctx context.Context) (bool, error) {
	var response struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.do(ctx, http.MethodGet, "/dnd", nil, nil, &response); err != nil {
		return false, err
	}
	return response.Enabled, nil
}

func (c *Client) SetDND(ctx context.Context, enabled bool) (bool, error) {
	var response struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.do(ctx, http.MethodPut, "/dnd", nil, map[string]any{"enabled": enabled}, &response); err != nil {
		return false, err
	}
	return response.Enabled, nil
}

func (c *Client) Sync(ctx context.Context) (SyncResponse, error) {
	var response SyncResponse
	err := c.do(ctx, http.MethodPost, "/sync", nil, map[string]any{}, &response)
	return response, err
}

func (c *Client) GetAssistantSettings(ctx context.Context) (AssistantSettings, error) {
	var response AssistantSettings
	err := c.do(ctx, http.MethodGet, "/assistant/settings", nil, nil, &response)
	return response, err
}

func (c *Client) PutAssistantSettings(ctx context.Context, settings AssistantSettings) (AssistantSettings, error) {
	var response struct {
		OK                bool              `json:"ok"`
		AssistantSettings AssistantSettings `json:"assistant_settings"`
	}
	if err := c.do(ctx, http.MethodPut, "/assistant/settings", nil, settings, &response); err != nil {
		return AssistantSettings{}, err
	}
	return response.AssistantSettings, nil
}

func (c *Client) ListChats(ctx context.Context, opts ChatListOptions) ([]ChatRecord, error) {
	query := url.Values{}
	if strings.TrimSpace(opts.Filter) != "" {
		query.Set("filter", strings.TrimSpace(opts.Filter))
	}
	if strings.TrimSpace(opts.Query) != "" {
		query.Set("query", strings.TrimSpace(opts.Query))
	}
	if opts.Limit > 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
	}
	var response struct {
		Chats []ChatRecord `json:"chats"`
	}
	if err := c.do(ctx, http.MethodGet, "/chats", query, nil, &response); err != nil {
		return nil, err
	}
	return response.Chats, nil
}

func (c *Client) SetChatLocked(ctx context.Context, jid string, locked bool) error {
	return c.do(ctx, http.MethodPut, "/chats/"+url.PathEscape(strings.TrimSpace(jid)), nil, map[string]any{
		"locked": locked,
	}, nil)
}

func (c *Client) ListLogs(ctx context.Context, queryOpts LogQuery) ([]AppLogRecord, error) {
	query := url.Values{}
	if strings.TrimSpace(queryOpts.Level) != "" {
		query.Set("level", strings.TrimSpace(queryOpts.Level))
	}
	if strings.TrimSpace(queryOpts.Category) != "" {
		query.Set("category", strings.TrimSpace(queryOpts.Category))
	}
	if strings.TrimSpace(queryOpts.Query) != "" {
		query.Set("query", strings.TrimSpace(queryOpts.Query))
	}
	if queryOpts.Limit > 0 {
		query.Set("limit", strconv.Itoa(queryOpts.Limit))
	}
	var response struct {
		Logs []AppLogRecord `json:"logs"`
	}
	if err := c.do(ctx, http.MethodGet, "/logs", query, nil, &response); err != nil {
		return nil, err
	}
	return response.Logs, nil
}

func (c *Client) ListWebhooks(ctx context.Context) ([]WebhookRecord, error) {
	var response struct {
		Webhooks []WebhookRecord `json:"webhooks"`
	}
	if err := c.do(ctx, http.MethodGet, "/webhooks", nil, nil, &response); err != nil {
		return nil, err
	}
	return response.Webhooks, nil
}

func (c *Client) ListOpenClawBridges(ctx context.Context) ([]OpenClawBridgeRecord, error) {
	var response struct {
		OpenClawBridges []OpenClawBridgeRecord `json:"openclaw_bridges"`
	}
	if err := c.do(ctx, http.MethodGet, "/openclaw_bridges", nil, nil, &response); err != nil {
		return nil, err
	}
	return response.OpenClawBridges, nil
}

func (c *Client) ListOpenClawDeliveries(ctx context.Context, opts DeliveryQuery) ([]OpenClawDeliveryRecord, error) {
	query := url.Values{}
	if strings.TrimSpace(opts.Status) != "" {
		query.Set("status", strings.TrimSpace(opts.Status))
	}
	if strings.TrimSpace(opts.Query) != "" {
		query.Set("query", strings.TrimSpace(opts.Query))
	}
	if opts.Limit > 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
	}
	var response struct {
		OpenClawDeliveries []OpenClawDeliveryRecord `json:"openclaw_deliveries"`
	}
	if err := c.do(ctx, http.MethodGet, "/openclaw_deliveries", query, nil, &response); err != nil {
		return nil, err
	}
	return response.OpenClawDeliveries, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
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
		if len(data) == 0 {
			return fmt.Errorf("%s %s failed with HTTP %d", method, path, resp.StatusCode)
		}
		return fmt.Errorf("%s", strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
