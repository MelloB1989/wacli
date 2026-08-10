package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func newHTTPHandler(service *Service) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		status, err := service.store.BuildStatus(service.IsConnected(), service.CurrentUserJID())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		status.UserLID = service.CurrentUserLID()
		writeJSON(w, http.StatusOK, status)
	})

	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		status, err := service.store.BuildStatus(service.IsConnected(), service.CurrentUserJID())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})

	mux.HandleFunc("/assistant/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			settings, err := service.store.GetAssistantSettings()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, settings)
		case http.MethodPut:
			var body AssistantSettings
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if err := service.store.PutAssistantSettings(body); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "assistant_settings": body})
		default:
			writeMethodNotAllowed(w)
		}
	})

	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		limit := queryInt(r, "limit", 200)
		level := strings.TrimSpace(r.URL.Query().Get("level"))
		category := strings.TrimSpace(r.URL.Query().Get("category"))
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		records, err := service.store.ListAppLogs(limit, level, category, query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"logs": records})
	})

	mux.HandleFunc("/dnd", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			enabled, err := service.store.GetDNDMode()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
		case http.MethodPut:
			var body struct {
				Enabled bool `json:"enabled"`
			}
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if err := service.store.SetDNDMode(body.Enabled); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
		default:
			writeMethodNotAllowed(w)
		}
	})

	mux.HandleFunc("/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		marker := service.historyMarker()
		requested := true
		if err := service.RequestHistorySync(ctx, 100); err != nil {
			if !errors.Is(err, ErrHistoryAnchorUnavailable) {
				writeError(w, http.StatusBadGateway, err)
				return
			}
			requested = false
		}
		seen := service.waitForHistoryQuiet(marker, 30*time.Second, 4*time.Second)
		_ = service.SyncContacts(ctx)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                true,
			"history_seen":      seen,
			"history_requested": requested,
			"requested_at":      time.Now().UTC().Format(time.RFC3339),
			"last_history_sync": func() *time.Time {
				ts, _ := service.store.LastHistorySync()
				return ts
			}(),
		})
	})

	mux.HandleFunc("/resolve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		ref := strings.TrimSpace(r.URL.Query().Get("ref"))
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		limit := queryInt(r, "limit", 10)
		allowDirect := queryBool(r, "allow_direct", false)
		results, err := service.ResolveTargets(ref, kind, limit, allowDirect)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"matches": results})
	})

	mux.HandleFunc("/chats", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chats" {
			handleChatDetail(service, w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		filter := r.URL.Query().Get("filter")
		limit := queryInt(r, "limit", 200)
		query := r.URL.Query().Get("query")
		chats, err := service.store.ListChats(filter, limit, query)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
	})
	mux.HandleFunc("/chats/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chats/access" {
			if r.Method != http.MethodPut {
				writeMethodNotAllowed(w)
				return
			}
			var body struct {
				UnlockedJIDs []string `json:"unlocked_jids"`
			}
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if err := service.store.ConfigureInitialAccess(body.UnlockedJIDs); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":            true,
				"unlocked_jids": body.UnlockedJIDs,
			})
			return
		}
		handleChatDetail(service, w, r)
	})

	mux.HandleFunc("/chats/access", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			UnlockedJIDs []string `json:"unlocked_jids"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := service.store.ConfigureInitialAccess(body.UnlockedJIDs); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"unlocked_jids": body.UnlockedJIDs,
		})
	})

	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		chatRef := strings.TrimSpace(r.URL.Query().Get("chat_ref"))
		if chatRef == "" {
			chatRef = strings.TrimSpace(r.URL.Query().Get("chat_jid"))
		}
		senderRef := strings.TrimSpace(r.URL.Query().Get("sender_ref"))
		if senderRef == "" {
			senderRef = strings.TrimSpace(r.URL.Query().Get("sender_jid"))
		}
		query := r.URL.Query().Get("query")
		limit := queryInt(r, "limit", 100)
		mediaOnly := queryBool(r, "media_only", false)
		fromMe := r.URL.Query().Get("from_me")
		before := queryTime(r, "before")
		after := queryTime(r, "after")
		messages, resolvedChat, resolvedSender, err := service.SearchMessagesAdvanced(chatRef, senderRef, query, limit, mediaOnly, fromMe, before, after)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"messages":        messages,
			"resolved_chat":   resolvedChat,
			"resolved_sender": resolvedSender,
		})
	})

	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			To        string `json:"to"`
			Text      string `json:"text,omitempty"`
			Message   string `json:"message,omitempty"`
			MediaPath string `json:"media_path,omitempty"`
			ReplyTo   string `json:"reply_to,omitempty"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		text := body.Text
		if text == "" {
			text = body.Message
		}
		resolved, err := service.ResolveBestTarget(body.To, "chat", true)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		record, err := service.SendMessageReplying(ctx, resolved.JID, text, body.MediaPath, body.ReplyTo)
		if err != nil {
			writeError(w, automationStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resolved_to": resolved, "message": record})
	})

	mux.HandleFunc("/messages/edit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			Chat string `json:"chat"`
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		record, err := service.EditMessage(ctx, body.Chat, body.ID, body.Text)
		if err != nil {
			writeError(w, automationStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": record})
	})

	mux.HandleFunc("/messages/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			Chat string `json:"chat"`
			ID   string `json:"id"`
		}
		if r.Method == http.MethodDelete && r.Body == http.NoBody {
			body.Chat = strings.TrimSpace(r.URL.Query().Get("chat"))
			body.ID = strings.TrimSpace(r.URL.Query().Get("id"))
		} else if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := service.RevokeMessage(ctx, body.Chat, body.ID); err != nil {
			writeError(w, automationStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": body.ID})
	})

	mux.HandleFunc("/messages/receipts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		receipts, err := service.MessageReceipts(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message_id": id, "receipts": receipts})
	})

	mux.HandleFunc("/bulk_send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			Items      []BulkSendItem `json:"items"`
			IntervalMS int            `json:"interval_ms,omitempty"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		results, err := service.BulkSend(ctx, body.Items, time.Duration(body.IntervalMS)*time.Millisecond)
		if err != nil {
			writeError(w, automationStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results})
	})

	mux.HandleFunc("/stories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			Text      string `json:"text,omitempty"`
			MediaPath string `json:"media_path,omitempty"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := service.SendStory(ctx, body.Text, body.MediaPath); err != nil {
			writeError(w, automationStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("/calls", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{
				"calls": service.ListCalls(r.URL.Query().Get("active") == "true"),
			})
		case http.MethodPost:
			var body struct {
				To       string `json:"to"`
				Video    bool   `json:"video,omitempty"`
				RingFor  int    `json:"ring_for_seconds,omitempty"`
				NoExpire bool   `json:"no_expire,omitempty"`
				Say      string `json:"say,omitempty"`
				Voice    string `json:"voice,omitempty"`
				Audio    string `json:"audio,omitempty"`
				Repeat   bool   `json:"repeat,omitempty"`
				Record   string `json:"record,omitempty"`
			}
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if strings.TrimSpace(body.To) == "" {
				writeError(w, http.StatusBadRequest, errors.New("to is required"))
				return
			}
			opts := PlaceCallOptions{Video: body.Video, Audio: AudioRequest{
				Say: body.Say, Voice: body.Voice, File: body.Audio, Repeat: body.Repeat,
				Record: body.Record,
			}}
			switch {
			case body.NoExpire:
				opts.RingFor = -1
			case body.RingFor > 0:
				opts.RingFor = time.Duration(body.RingFor) * time.Second
			}
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			info, err := service.PlaceCall(ctx, body.To, opts)
			if err != nil {
				writeError(w, automationStatus(err), err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call": info})
		default:
			writeMethodNotAllowed(w)
		}
	})

	mux.HandleFunc("/calls/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		ref := strings.TrimSpace(r.URL.Query().Get("ref"))
		if ref == "" {
			ref = strings.TrimSpace(r.URL.Query().Get("call_id"))
		}
		status, err := service.CallStatus(ref)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"call": status, "queue": service.CallQueueStatus()})
	})

	mux.HandleFunc("/calls/queue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"queue": service.CallQueueStatus()})
	})

	mux.HandleFunc("/calls/answer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			CallID string `json:"call_id,omitempty"`
			Say    string `json:"say,omitempty"`
			Voice  string `json:"voice,omitempty"`
			Audio  string `json:"audio,omitempty"`
			Repeat bool   `json:"repeat,omitempty"`
			Record string `json:"record,omitempty"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		id, err := service.AnswerWithAudio(body.CallID, AudioRequest{
			Say: body.Say, Voice: body.Voice, File: body.Audio, Repeat: body.Repeat,
			Record: body.Record,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call_id": id})
	})

	mux.HandleFunc("/calls/capture", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var err error
		if body.Enabled {
			err = service.StartCallCapture()
		} else {
			err = service.StopCallCapture()
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled, "path": capturePath})
	})

	mux.HandleFunc("/calls/end", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			CallID string `json:"call_id"`
			Reason string `json:"reason,omitempty"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		info, err := service.EndCall(ctx, body.CallID, body.Reason)
		if err != nil {
			writeError(w, callErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call": info})
	})

	mux.HandleFunc("/calls/accept", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			CallID string `json:"call_id"`
			// Candidates are the addresses the caller should send media to. A real client always
			// sends at least one; without them the peer keeps ringing because it has nowhere to go.
			Candidates []struct {
				IP       string `json:"ip"`
				Port     uint16 `json:"port"`
				Priority int    `json:"priority"`
			} `json:"candidates"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// With no call_id, accept whatever is ringing — that is what a caller normally wants.
		if body.CallID == "" {
			latest, ok := service.LatestRingingCall()
			if !ok {
				writeError(w, http.StatusNotFound, errors.New("no incoming call is ringing"))
				return
			}
			body.CallID = latest.CallID
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		cands := make([]types.CallCandidate, 0, len(body.Candidates))
		for _, c := range body.Candidates {
			ip := net.ParseIP(c.IP)
			if ip == nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("bad candidate IP %q", c.IP))
				return
			}
			cands = append(cands, types.CallCandidate{IP: ip, Port: c.Port, Priority: c.Priority})
		}
		info, err := service.AcceptIncomingCall(ctx, body.CallID, cands...)
		if err != nil {
			writeError(w, callErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call": info})
	})

	mux.HandleFunc("/calls/reject", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			CallID string `json:"call_id"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		info, err := service.RejectIncomingCall(ctx, body.CallID)
		if err != nil {
			writeError(w, callErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call": info})
	})

	mux.HandleFunc("/media/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			MessageID string `json:"message_id"`
			ChatJID   string `json:"chat_jid,omitempty"`
			ChatRef   string `json:"chat_ref,omitempty"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		chatRef := body.ChatJID
		if chatRef == "" {
			chatRef = body.ChatRef
		}
		resolved, err := service.ResolveBestTarget(chatRef, "chat", false)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		path, err := service.DownloadMedia(ctx, body.MessageID, resolved.JID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resolved_chat": resolved, "path": path})
	})

	mux.HandleFunc("/contacts", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/contacts/lookup" {
			handleContactLookup(service, w, r)
			return
		}
		if r.URL.Path == "/contacts/update" {
			handleContactUpdateByRef(service, w, r)
			return
		}
		if r.URL.Path != "/contacts" {
			handleContactDetail(service, w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		limit := queryInt(r, "limit", 200)
		query := r.URL.Query().Get("query")
		contacts, err := service.store.ListContacts(limit, query)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"contacts": contacts})
	})
	mux.HandleFunc("/contacts/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/contacts/lookup":
			handleContactLookup(service, w, r)
		case "/contacts/update":
			handleContactUpdateByRef(service, w, r)
		default:
			handleContactDetail(service, w, r)
		}
	})

	mux.HandleFunc("/webhooks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/webhooks" {
			handleWebhookDetail(service, w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			webhooks, err := service.store.ListWebhooks()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"webhooks": webhooks})
		case http.MethodPost:
			var body struct {
				URL             string   `json:"url"`
				Secret          string   `json:"secret,omitempty"`
				Events          []string `json:"events,omitempty"`
				Scope           string   `json:"scope,omitempty"`
				ChatRef         string   `json:"chat_ref,omitempty"`
				ChatRefs        []string `json:"chat_refs,omitempty"`
				ChatJIDs        []string `json:"chat_jids,omitempty"`
				MessageTypes    []string `json:"message_types,omitempty"`
				ContextLimit    int      `json:"context_limit,omitempty"`
				Enabled         *bool    `json:"enabled,omitempty"`
				IncludeMentions bool     `json:"include_mentions,omitempty"`
			}
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			enabled := true
			if body.Enabled != nil {
				enabled = *body.Enabled
			}
			chatRefs := append([]string{}, body.ChatRefs...)
			if strings.TrimSpace(body.ChatRef) != "" {
				chatRefs = append(chatRefs, body.ChatRef)
			}
			chatRefs = append(chatRefs, body.ChatJIDs...)
			resolvedChatJIDs, err := resolveWebhookChatTargets(service, chatRefs)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			record, err := service.store.AddWebhook(WebhookRecord{
				URL:             body.URL,
				Secret:          body.Secret,
				Events:          body.Events,
				Scope:           body.Scope,
				ChatJIDs:        resolvedChatJIDs,
				MessageTypes:    body.MessageTypes,
				ContextLimit:    body.ContextLimit,
				Enabled:         enabled,
				IncludeMentions: body.IncludeMentions,
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"webhook": record})
		default:
			writeMethodNotAllowed(w)
		}
	})
	mux.HandleFunc("/webhooks/", func(w http.ResponseWriter, r *http.Request) {
		handleWebhookDetail(service, w, r)
	})
	mux.HandleFunc("/webhook_deliveries", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		limit := queryInt(r, "limit", 100)
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		event := strings.TrimSpace(r.URL.Query().Get("event"))
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		records, err := service.store.ListWebhookDeliveries(limit, status, event, query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"webhook_deliveries": records})
	})

	mux.HandleFunc("/openclaw_bridges", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openclaw_bridges" {
			handleOpenClawBridgeDetail(service, w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			bridges, err := service.store.ListOpenClawBridges()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"openclaw_bridges": bridges})
		case http.MethodPost:
			record, err := decodeOpenClawBridgeRequest(service, r)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			record, err = service.store.AddOpenClawBridge(record)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"openclaw_bridge": record})
		default:
			writeMethodNotAllowed(w)
		}
	})
	mux.HandleFunc("/openclaw_bridges/", func(w http.ResponseWriter, r *http.Request) {
		handleOpenClawBridgeDetail(service, w, r)
	})

	mux.HandleFunc("/openclaw_sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		limit := queryInt(r, "limit", 200)
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		sessions, err := service.store.ListOpenClawSessions(limit, query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"openclaw_sessions": sessions})
	})
	mux.HandleFunc("/openclaw_deliveries", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		limit := queryInt(r, "limit", 100)
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		records, err := service.store.ListOpenClawDeliveries(limit, status, query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"openclaw_deliveries": records})
	})
	mux.HandleFunc("/openclaw_replay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		var body struct {
			BridgeID  int64  `json:"bridge_id"`
			ChatRef   string `json:"chat_ref,omitempty"`
			ChatJID   string `json:"chat_jid,omitempty"`
			MessageID string `json:"message_id,omitempty"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		chatRef := strings.TrimSpace(body.ChatRef)
		if chatRef == "" {
			chatRef = strings.TrimSpace(body.ChatJID)
		}
		if body.BridgeID <= 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("bridge_id is required"))
			return
		}
		if chatRef == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("chat_ref is required"))
			return
		}
		result, err := service.ReplayOpenClawInbound(body.BridgeID, chatRef, body.MessageID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "openclaw_replay": result})
	})

	mux.HandleFunc("/auto_replies", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auto_replies" {
			handleAutoReplyDetail(service, w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			rules, err := service.store.ListAutoReplies()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"auto_replies": rules})
		case http.MethodPost:
			var body struct {
				Name          string `json:"name"`
				MatchType     string `json:"match_type,omitempty"`
				Pattern       string `json:"pattern,omitempty"`
				ReplyText     string `json:"reply_text,omitempty"`
				MediaPath     string `json:"media_path,omitempty"`
				Enabled       *bool  `json:"enabled,omitempty"`
				ApplyToDMs    *bool  `json:"apply_to_dms,omitempty"`
				ApplyToGroups *bool  `json:"apply_to_groups,omitempty"`
				Priority      int    `json:"priority,omitempty"`
			}
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			enabled := true
			if body.Enabled != nil {
				enabled = *body.Enabled
			}
			applyToDMs := true
			if body.ApplyToDMs != nil {
				applyToDMs = *body.ApplyToDMs
			}
			applyToGroups := false
			if body.ApplyToGroups != nil {
				applyToGroups = *body.ApplyToGroups
			}
			rule, err := service.store.AddAutoReply(AutoReplyRule{
				Name:          body.Name,
				MatchType:     body.MatchType,
				Pattern:       body.Pattern,
				ReplyText:     body.ReplyText,
				MediaPath:     body.MediaPath,
				Enabled:       enabled,
				ApplyToDMs:    applyToDMs,
				ApplyToGroups: applyToGroups,
				Priority:      body.Priority,
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"auto_reply": rule})
		default:
			writeMethodNotAllowed(w)
		}
	})
	mux.HandleFunc("/auto_replies/", func(w http.ResponseWriter, r *http.Request) {
		handleAutoReplyDetail(service, w, r)
	})

	return mux
}

func handleChatDetail(service *Service, w http.ResponseWriter, r *http.Request) {
	jid, err := pathParam(r.URL.Path, "/chats/")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if r.Method != http.MethodPut {
		writeMethodNotAllowed(w)
		return
	}
	var body struct {
		Locked bool `json:"locked"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := service.store.SetChatLocked(jid, body.Locked); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	chat, err := service.store.GetChat(jid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": chat})
}

func handleContactDetail(service *Service, w http.ResponseWriter, r *http.Request) {
	jid, err := pathParam(r.URL.Path, "/contacts/")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		contact, err := service.store.GetContact(jid)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"contact": contact})
	case http.MethodPut:
		var update ContactUpdate
		if err := decodeJSON(r, &update); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		contact, err := service.store.UpsertContactMemory(jid, update)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"contact": contact})
	default:
		writeMethodNotAllowed(w)
	}
}

func handleContactLookup(service *Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	resolved, err := service.ResolveBestTarget(ref, "contact", false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	contact, err := service.store.GetContact(resolved.JID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) && !resolved.IsGroup {
			contact = ContactRecord{
				JID:       resolved.JID,
				Phone:     resolved.Phone,
				FullName:  resolved.Name,
				PushName:  resolved.Name,
				Found:     resolved.ExistsInContacts || resolved.ExistsInChats,
				UpdatedAt: time.Now(),
			}
		} else {
			status := http.StatusInternalServerError
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolved": resolved, "contact": contact})
}

func handleContactUpdateByRef(service *Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var body struct {
		Ref          string  `json:"ref"`
		Bio          *string `json:"bio,omitempty"`
		Notes        *string `json:"notes,omitempty"`
		Memory       *string `json:"memory,omitempty"`
		MetadataJSON *string `json:"metadata_json,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolved, err := service.ResolveBestTarget(body.Ref, "contact", false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	contact, err := service.store.UpsertContactMemory(resolved.JID, ContactUpdate{
		Bio:          body.Bio,
		Notes:        body.Notes,
		Memory:       body.Memory,
		MetadataJSON: body.MetadataJSON,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolved": resolved, "contact": contact})
}

func handleWebhookDetail(service *Service, w http.ResponseWriter, r *http.Request) {
	idText, err := pathParam(r.URL.Path, "/webhooks/")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if r.Method != http.MethodDelete {
		writeMethodNotAllowed(w)
		return
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid webhook id"))
		return
	}
	if err := service.store.DeleteWebhook(id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func resolveWebhookChatTargets(service *Service, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		target, err := service.ResolveBestTarget(ref, "chat", false)
		if err != nil {
			return nil, err
		}
		if !target.ExistsInChats {
			return nil, fmt.Errorf("webhook chat %q is not in synced chats", ref)
		}
		if target.Locked {
			return nil, fmt.Errorf("webhook chat %q is locked; only unlocked chats may be subscribed", ref)
		}
		if _, ok := seen[target.JID]; ok {
			continue
		}
		seen[target.JID] = struct{}{}
		out = append(out, target.JID)
	}
	return out, nil
}

func handleOpenClawBridgeDetail(service *Service, w http.ResponseWriter, r *http.Request) {
	idText, err := pathParam(r.URL.Path, "/openclaw_bridges/")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid openclaw bridge id"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		record, err := service.store.GetOpenClawBridge(id)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"openclaw_bridge": record})
	case http.MethodPut:
		record, err := service.store.GetOpenClawBridge(id)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		updated, err := applyOpenClawBridgeUpdate(service, r, record)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		updated.ID = id
		updated, err = service.store.UpdateOpenClawBridge(updated)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"openclaw_bridge": updated})
	case http.MethodDelete:
		if err := service.store.DeleteOpenClawBridge(id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeMethodNotAllowed(w)
	}
}

func decodeOpenClawBridgeRequest(service *Service, r *http.Request) (OpenClawBridgeRecord, error) {
	var body struct {
		Command      string   `json:"command,omitempty"`
		Scope        string   `json:"scope,omitempty"`
		ChatRef      string   `json:"chat_ref,omitempty"`
		ChatRefs     []string `json:"chat_refs,omitempty"`
		ChatJIDs     []string `json:"chat_jids,omitempty"`
		MessageTypes []string `json:"message_types,omitempty"`
		ContextLimit int      `json:"context_limit,omitempty"`
		Instruction  string   `json:"instruction,omitempty"`
		Enabled      *bool    `json:"enabled,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return OpenClawBridgeRecord{}, err
	}
	chatRefs := append([]string{}, body.ChatRefs...)
	if strings.TrimSpace(body.ChatRef) != "" {
		chatRefs = append(chatRefs, body.ChatRef)
	}
	chatRefs = append(chatRefs, body.ChatJIDs...)
	resolvedChatJIDs, err := resolveWebhookChatTargets(service, chatRefs)
	if err != nil {
		return OpenClawBridgeRecord{}, err
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	return OpenClawBridgeRecord{
		Command:      body.Command,
		Scope:        body.Scope,
		ChatJIDs:     resolvedChatJIDs,
		MessageTypes: body.MessageTypes,
		ContextLimit: body.ContextLimit,
		Instruction:  body.Instruction,
		Enabled:      enabled,
	}, nil
}

func applyOpenClawBridgeUpdate(service *Service, r *http.Request, record OpenClawBridgeRecord) (OpenClawBridgeRecord, error) {
	var body struct {
		Command      *string   `json:"command,omitempty"`
		Scope        *string   `json:"scope,omitempty"`
		ChatRef      *string   `json:"chat_ref,omitempty"`
		ChatRefs     *[]string `json:"chat_refs,omitempty"`
		ChatJIDs     *[]string `json:"chat_jids,omitempty"`
		MessageTypes *[]string `json:"message_types,omitempty"`
		ContextLimit *int      `json:"context_limit,omitempty"`
		Instruction  *string   `json:"instruction,omitempty"`
		Enabled      *bool     `json:"enabled,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return OpenClawBridgeRecord{}, err
	}
	if body.Command != nil {
		record.Command = *body.Command
	}
	if body.Scope != nil {
		record.Scope = *body.Scope
	}
	if body.ContextLimit != nil {
		record.ContextLimit = *body.ContextLimit
	}
	if body.Instruction != nil {
		record.Instruction = *body.Instruction
	}
	if body.Enabled != nil {
		record.Enabled = *body.Enabled
	}
	chatRefsProvided := body.ChatRef != nil || body.ChatRefs != nil || body.ChatJIDs != nil
	if body.MessageTypes != nil {
		record.MessageTypes = append([]string{}, (*body.MessageTypes)...)
	}
	if chatRefsProvided {
		var chatRefs []string
		if body.ChatRefs != nil {
			chatRefs = append(chatRefs, (*body.ChatRefs)...)
		}
		if body.ChatRef != nil && strings.TrimSpace(*body.ChatRef) != "" {
			chatRefs = append(chatRefs, *body.ChatRef)
		}
		if body.ChatJIDs != nil {
			chatRefs = append(chatRefs, (*body.ChatJIDs)...)
		}
		resolvedChatJIDs, err := resolveWebhookChatTargets(service, chatRefs)
		if err != nil {
			return OpenClawBridgeRecord{}, err
		}
		record.ChatJIDs = resolvedChatJIDs
	}
	return record, nil
}

func handleAutoReplyDetail(service *Service, w http.ResponseWriter, r *http.Request) {
	idText, err := pathParam(r.URL.Path, "/auto_replies/")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if r.Method != http.MethodDelete {
		writeMethodNotAllowed(w)
		return
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid auto reply id"))
		return
	}
	if err := service.store.DeleteAutoReply(id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func decodeJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	return decoder.Decode(dest)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{
		"ok":    false,
		"error": err.Error(),
	})
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func queryInt(r *http.Request, key string, defaultValue int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

func queryBool(r *http.Request, key string, defaultValue bool) bool {
	value := strings.TrimSpace(strings.ToLower(r.URL.Query().Get(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultValue
	}
}

func queryTime(r *http.Request, key string) *time.Time {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return nil
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &ts
}

func pathParam(path, prefix string) (string, error) {
	if !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("invalid path")
	}
	value := strings.TrimPrefix(path, prefix)
	value = strings.Trim(value, "/")
	if value == "" {
		return "", fmt.Errorf("missing path parameter")
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return "", err
	}
	return decoded, nil
}

// callErrorStatus maps call control failures to HTTP statuses. An unknown or already-finished call
// is a 404 rather than a generic bad request, since clients poll /calls to find live ones.
func callErrorStatus(err error) int {
	if errors.Is(err, errCallNotActive) {
		return http.StatusNotFound
	}
	return automationStatus(err)
}

func automationStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	switch {
	case strings.Contains(err.Error(), "DND mode is off"):
		return http.StatusConflict
	case strings.Contains(err.Error(), "locked"):
		return http.StatusForbidden
	case strings.Contains(err.Error(), "not connected"):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}
