package wa

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	waWeb "go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

var ErrHistoryAnchorUnavailable = errors.New("history sync anchor unavailable")

type Service struct {
	store      *Store
	client     *whatsmeow.Client
	sessionDB  *sql.DB
	log        waLog.Logger
	httpClient *http.Client

	mu                sync.RWMutex
	connected         bool
	historyEventCount int
	lastHistoryEvent  time.Time
	historySyncTypes  map[string]int

	// lastEventAt is the wall-clock time of the most recent event received from
	// WhatsApp (any kind). The connection watchdog uses it to detect a "zombie"
	// socket — one that still reports connected but has silently stopped
	// delivering messages — and force a reconnect.
	lastEventAt  time.Time
	watchdogOnce sync.Once

	// calls tracks in-flight and recently finished calls. See calls.go.
	calls *callRegistry
	// capture records raw call signaling stanzas when enabled. See callcapture.go.
	capture *callCapture
	// media carries actual call audio, via meowcaller. See callmedia.go.
	media *callMedia
	// queue serialises outgoing calls behind one active-call slot. See callqueue.go.
	queue *callQueue
}

// StartCallCapture begins recording raw call stanzas to the capture file.
func (s *Service) StartCallCapture() error { return s.capture.Enable() }

// StopCallCapture stops recording call stanzas.
func (s *Service) StopCallCapture() error { return s.capture.Close() }

func NewService(store *Store) (*Service, error) {
	// WARN keeps normal runs quiet, but whatsmeow reports things like a failed call-key decrypt at
	// DEBUG, so the level has to be reachable without rebuilding.
	level := os.Getenv("WACLI_LOG_LEVEL")
	if level == "" {
		level = "WARN"
	}
	baseLog := waLog.Stdout("wacli", level, true)
	// Wrap the logger so call stanzas can be tapped for protocol analysis. Capture stays off until
	// something calls StartCallCapture, so this costs one type switch per logged stanza.
	capture := newCallCapture(CapturePath)
	// Relay tokens are relay-scoped — a relay rejects a token minted for a different one with
	// code 456 "Failed to decode allocate request". Yet the real client reaches relays the call
	// offer never names, so it must obtain those tokens somewhere else. Capture normally starts
	// only once something calls StartCallCapture, which is always after the connection is up, so
	// anything exchanged during connection setup has never been visible. WACLI_CAPTURE=1 arms it
	// from process start to find out.
	if v := os.Getenv("WACLI_CAPTURE"); v == "1" || v == "2" {
		_ = capture.Enable()
	}
	log := newCaptureLogger(baseLog, capture)
	db, err := sql.Open("sqlite3", SessionDBPath+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open session db: %w", err)
	}
	container := sqlstore.NewWithDB(db, "sqlite3", log)
	if err := container.Upgrade(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("session db upgrade: %w", err)
	}
	dev, err := container.GetFirstDevice(context.Background())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			dev = container.NewDevice()
		} else {
			db.Close()
			return nil, fmt.Errorf("get first device: %w", err)
		}
	}
	client := whatsmeow.NewClient(dev, log)
	if client == nil {
		db.Close()
		return nil, errors.New("create whatsmeow client")
	}

	service := &Service{
		store:            store,
		client:           client,
		sessionDB:        db,
		log:              log,
		historySyncTypes: map[string]int{},
		calls:            newCallRegistry(),
		capture:          capture,
		queue:            &callQueue{},
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	service.registerEventHandlers()
	// Bring the media stack up before anything connects: meowcaller installs its handlers by
	// reaching into whatsmeow's unexported nodeHandlers, and refuses to once the receive loop is
	// running. Without it an outbound call never learns its relay and carries no audio.
	service.media = newCallMedia(service, mediaLogger(level))
	return service, nil
}

// mediaLogger adapts wacli's log level to the zerolog logger meowcaller takes.
//
// It floors at info: meowcaller reports the milestones that say whether a call will carry audio at
// all — the relay arriving in the offer ack, the first inbound RTP — at that level, and calls are
// rare enough that the noise costs nothing. WACLI_LOG_LEVEL=DEBUG still turns everything up.
func mediaLogger(level string) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(strings.ToLower(level))
	if err != nil || lvl == zerolog.NoLevel || lvl > zerolog.InfoLevel {
		lvl = zerolog.InfoLevel
	}
	return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05.000"}).
		Level(lvl).With().Timestamp().Logger()
}

func (s *Service) Close() error {
	if s.client != nil && s.client.IsConnected() {
		s.client.Disconnect()
	}
	if s.sessionDB != nil {
		return s.sessionDB.Close()
	}
	return nil
}

func ClearSession() {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(SessionDBPath + suffix)
	}
}

func (s *Service) registerEventHandlers() {
	s.client.AddEventHandler(func(evt any) {
		// Any event from WhatsApp proves the socket is alive — feed the watchdog.
		s.noteEvent()

		switch v := evt.(type) {
		case *events.Connected:
			s.onConnected()
		case *events.Disconnected:
			s.onDisconnected()
		case *events.LoggedOut:
			s.onLoggedOut()
		case *events.Message:
			s.onLiveMessage(v)
		case *events.HistorySync:
			s.onHistorySync(v)
		case *events.Receipt:
			s.onReceipt(v)
		case *events.CallOffer:
			s.onCallOffer(v)
		case *events.CallAccept:
			s.onCallAccept(v)
		case *events.CallTerminate:
			s.onCallTerminate(v)
		case *events.CallReject:
			s.onCallReject(v)
		case *events.KeepAliveTimeout:
			// Keepalive pings are failing: the connection is dead even though the
			// socket may still look open. Force a reconnect rather than sitting
			// there silently dropping every incoming message.
			s.log.Warnf("keepalive timeout — forcing reconnect")
			go s.forceReconnect("keepalive timeout")
		case *events.KeepAliveRestored:
			s.log.Infof("keepalive restored")
		}
	})
}

// noteEvent records that WhatsApp delivered something just now.
func (s *Service) noteEvent() {
	s.mu.Lock()
	s.lastEventAt = time.Now()
	s.mu.Unlock()
}

func (s *Service) sinceLastEvent() time.Duration {
	s.mu.RLock()
	last := s.lastEventAt
	s.mu.RUnlock()
	if last.IsZero() {
		return 0
	}
	return time.Since(last)
}

// forceReconnect tears the WhatsApp connection down and dials again. Safe to
// call repeatedly; whatsmeow tolerates a disconnect on an already-dead socket.
func (s *Service) forceReconnect(reason string) {
	s.log.Warnf("reconnecting to WhatsApp (%s)", reason)
	if s.client != nil {
		s.client.Disconnect()
	}
	time.Sleep(2 * time.Second)
	if err := s.Connect(); err != nil {
		s.log.Errorf("reconnect failed: %v", err)
		return
	}
	s.noteEvent() // give the fresh connection a grace period
}

// StartConnectionWatchdog guards against the failure mode where the WhatsApp
// socket stops delivering messages while still reporting "connected" — the
// daemon looks healthy but silently receives nothing. It reconnects when the
// client reports disconnected, or when nothing at all has arrived for
// watchdogStaleAfter (receipts/presence make a truly silent window unusual on
// an active account). Idempotent: only the first call starts the loop.
func (s *Service) StartConnectionWatchdog() {
	const (
		watchdogInterval   = 2 * time.Minute
		watchdogStaleAfter = 20 * time.Minute
	)
	s.watchdogOnce.Do(func() {
		s.noteEvent()
		go func() {
			ticker := time.NewTicker(watchdogInterval)
			defer ticker.Stop()
			for range ticker.C {
				if s.client == nil || s.client.Store.ID == nil {
					continue // not logged in; nothing to guard
				}
				if !s.client.IsConnected() {
					s.forceReconnect("client reports disconnected")
					continue
				}
				if idle := s.sinceLastEvent(); idle > watchdogStaleAfter {
					s.forceReconnect(fmt.Sprintf("no WhatsApp events for %s", idle.Round(time.Minute)))
				}
			}
		}()
	})
}

func (s *Service) Connect() error {
	if s.client.Store.ID == nil {
		return errors.New("not logged in; run `wacli login` first")
	}
	return s.client.Connect()
}

func (s *Service) Disconnect() {
	if s.client != nil && s.client.IsConnected() {
		s.client.Disconnect()
	}
}

func (s *Service) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected
}

func (s *Service) CurrentUserJID() string {
	if s.client.Store.ID == nil {
		return ""
	}
	return s.client.Store.ID.String()
}

// CurrentUserLID returns this device's LID, which call media needs: the peer derives the SSRC it
// expects from the LID, not from the phone-number JID (see ssrc.go).
func (s *Service) CurrentUserLID() string {
	if s.client.Store.LID.IsEmpty() {
		return ""
	}
	return s.client.Store.LID.String()
}

func (s *Service) HistoryMarker() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.historyEventCount
}

func (s *Service) WaitForHistoryQuiet(marker int, maxWait, quietPeriod time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for {
		s.mu.RLock()
		count := s.historyEventCount
		last := s.lastHistoryEvent
		s.mu.RUnlock()
		if count > marker && time.Since(last) >= quietPeriod {
			return true
		}
		if time.Now().After(deadline) {
			return count > marker
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Service) WaitForBootstrapSync(maxWait, quietPeriod time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for {
		s.mu.RLock()
		count := s.historyEventCount
		last := s.lastHistoryEvent
		recent := s.historySyncTypes["RECENT"] > 0
		bootstrap := s.historySyncTypes["INITIAL_BOOTSTRAP"] > 0
		nonBlocking := s.historySyncTypes["NON_BLOCKING_DATA"] > 0
		s.mu.RUnlock()

		if recent && time.Since(last) >= quietPeriod {
			return true
		}
		if bootstrap && time.Since(last) >= quietPeriod && !recent {
			return true
		}
		if time.Now().After(deadline) {
			return recent || bootstrap || nonBlocking || count > 0
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Service) RequestHistorySync(ctx context.Context, count int) error {
	if count <= 0 {
		count = 100
	}
	if !s.client.IsConnected() {
		return errors.New("client is not connected")
	}
	anchor, err := s.latestHistoryAnchor()
	if err != nil {
		return err
	}
	msg := s.client.BuildHistorySyncRequest(anchor, count)
	_, err = s.client.SendPeerMessage(ctx, msg)
	return err
}

func (s *Service) latestHistoryAnchor() (*types.MessageInfo, error) {
	record, err := s.store.LatestMessage()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHistoryAnchorUnavailable
		}
		return nil, err
	}
	chat, err := types.ParseJID(record.ChatJID)
	if err != nil {
		return nil, fmt.Errorf("parse anchor chat jid: %w", err)
	}
	senderJID := record.SenderJID
	if senderJID == "" {
		senderJID = record.ChatJID
	}
	sender, err := types.ParseJID(senderJID)
	if err != nil {
		sender = chat
	}
	return &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   sender,
			IsFromMe: record.IsFromMe,
			IsGroup:  strings.HasSuffix(record.ChatJID, "@g.us"),
		},
		ID:        record.ID,
		Timestamp: record.Timestamp,
	}, nil
}

func (s *Service) SyncContacts(ctx context.Context) error {
	if s.client.Store == nil || s.client.Store.Contacts == nil {
		return nil
	}
	contacts, err := s.client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return err
	}
	for jid, contact := range contacts {
		record := ContactRecord{
			JID:          jid.String(),
			Phone:        NormalizePhone(jid.User),
			FullName:     contact.FullName,
			FirstName:    contact.FirstName,
			PushName:     contact.PushName,
			BusinessName: contact.BusinessName,
			Found:        contact.Found,
			UpdatedAt:    time.Now(),
		}
		if err := s.store.UpsertContact(record); err != nil {
			s.log.Warnf("upsert contact %s: %v", jid.String(), err)
		}
		if existingChat, err := s.store.GetChat(jid.String()); err == nil && !existingChat.IsGroup {
			_, _ = s.store.EnsureChat(existingChat.JID, displayNameFromContact(record), false, existingChat.LastMessageAt, existingChat.LastMessagePreview, false)
		}
	}
	return nil
}

func displayNameFromContact(contact ContactRecord) string {
	switch {
	case strings.TrimSpace(contact.FullName) != "":
		return contact.FullName
	case strings.TrimSpace(contact.FirstName) != "":
		return contact.FirstName
	case strings.TrimSpace(contact.PushName) != "":
		return contact.PushName
	case strings.TrimSpace(contact.BusinessName) != "":
		return contact.BusinessName
	case strings.TrimSpace(contact.Phone) != "":
		return contact.Phone
	default:
		return contact.JID
	}
}

func (s *Service) onConnected() {
	s.mu.Lock()
	s.connected = true
	s.mu.Unlock()
	fmt.Println("connected to WhatsApp")
	_ = s.store.AddAppLog("info", "connection", "Connected to WhatsApp", map[string]any{
		"user_jid": s.CurrentUserJID(),
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.SyncContacts(ctx); err != nil {
			s.log.Warnf("sync contacts after connect: %v", err)
		}
		if err := s.RefreshMissingChatNames(ctx, 50); err != nil {
			s.log.Warnf("refresh chat names after connect: %v", err)
		}
		s.dispatchWebhook("connection_state", map[string]any{
			"state":    "connected",
			"user_jid": s.CurrentUserJID(),
		})
	}()
}

func (s *Service) onDisconnected() {
	s.mu.Lock()
	s.connected = false
	s.mu.Unlock()
	fmt.Println("disconnected from WhatsApp")
	_ = s.store.AddAppLog("warn", "connection", "Disconnected from WhatsApp", map[string]any{
		"user_jid": s.CurrentUserJID(),
	})
	s.dispatchWebhook("connection_state", map[string]any{
		"state":    "disconnected",
		"user_jid": s.CurrentUserJID(),
	})
}

func (s *Service) onLoggedOut() {
	s.mu.Lock()
	s.connected = false
	s.mu.Unlock()
	fmt.Println("logged out; run `wacli login` again")
	ClearSession()
	_ = s.store.AddAppLog("error", "connection", "WhatsApp session logged out", map[string]any{
		"user_jid": s.CurrentUserJID(),
	})
	s.dispatchWebhook("connection_state", map[string]any{
		"state":    "logged_out",
		"user_jid": s.CurrentUserJID(),
	})
}

func (s *Service) onHistorySync(evt *events.HistorySync) {
	if evt == nil || evt.Data == nil {
		return
	}
	duringInitialSync, _ := s.store.InitialAccessConfigured()
	duringInitialSync = !duringInitialSync

	conversations := evt.Data.GetConversations()
	storedMessages := 0
	syncType := evt.Data.GetSyncType().String()
	for _, conv := range conversations {
		count, err := s.storeConversation(conv, duringInitialSync)
		if err != nil {
			s.log.Warnf("history sync conversation %s: %v", conv.GetID(), err)
			continue
		}
		storedMessages += count
	}
	_ = s.store.SetLastHistorySync(time.Now())
	_ = s.store.AddAppLog("info", "history_sync", fmt.Sprintf("History sync %s stored %d chats and %d messages", syncType, len(conversations), storedMessages), map[string]any{
		"sync_type":        syncType,
		"conversation_cnt": len(conversations),
		"message_cnt":      storedMessages,
	})
	s.mu.Lock()
	s.historyEventCount++
	s.lastHistoryEvent = time.Now()
	s.historySyncTypes[syncType]++
	s.mu.Unlock()
	fmt.Printf("history sync %s: %d chats, %d messages\n", syncType, len(conversations), storedMessages)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = s.SyncContacts(ctx)
		_ = s.RefreshMissingChatNames(ctx, 50)
	}()
	s.dispatchWebhook("sync_complete", map[string]any{
		"sync_type":        syncType,
		"conversation_cnt": len(conversations),
		"message_cnt":      storedMessages,
	})
}

func (s *Service) storeConversation(conv *waHistorySync.Conversation, duringInitialSync bool) (int, error) {
	jidStr := conv.GetID()
	if jidStr == "" {
		return 0, nil
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return 0, err
	}
	chatName := s.resolveConversationName(jid, conv)
	lastTS := time.Unix(int64(conv.GetConversationTimestamp()), 0)
	if lastTS.IsZero() {
		lastTS = time.Unix(int64(conv.GetLastMsgTimestamp()), 0)
	}
	if lastTS.IsZero() {
		lastTS = time.Now()
	}
	_, err = s.store.EnsureChat(jidStr, chatName, jid.Server == types.GroupServer, lastTS, "", duringInitialSync)
	if err != nil {
		return 0, err
	}

	storedMessages := 0
	for _, hsMsg := range conv.GetMessages() {
		webMsg := hsMsg.GetMessage()
		if webMsg == nil || webMsg.GetMessage() == nil {
			continue
		}
		record := s.recordFromWebMessage(jidStr, webMsg)
		if record.ID == "" || (record.Content == "" && record.MediaType == "" && record.MessageType == "") {
			continue
		}
		preview := buildPreview(record)
		if _, err := s.store.EnsureChat(jidStr, chatName, jid.Server == types.GroupServer, record.Timestamp, preview, duringInitialSync); err != nil {
			s.log.Warnf("ensure chat %s: %v", jidStr, err)
		}
		if err := s.store.StoreMessage(record); err != nil {
			s.log.Warnf("store history message %s: %v", record.ID, err)
			continue
		}
		s.ensureContactFromMessage(record, webMsg.GetPushName())
		storedMessages++
	}
	return storedMessages, nil
}

func (s *Service) resolveConversationName(jid types.JID, conv *waHistorySync.Conversation) string {
	if existing, err := s.store.GetChat(jid.String()); err == nil && isUsableChatName(existing.Name, existing.JID) {
		return existing.Name
	}
	if conv != nil {
		switch {
		case conv.GetDisplayName() != "":
			return conv.GetDisplayName()
		case conv.GetName() != "":
			return conv.GetName()
		}
	}
	return s.resolveJIDName(jid, "")
}

func (s *Service) resolveJIDName(jid types.JID, fallback string) string {
	if existing, err := s.store.GetChat(jid.String()); err == nil && isUsableChatName(existing.Name, existing.JID) {
		return existing.Name
	}
	if jid.Server == types.GroupServer {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if info, err := s.client.GetGroupInfo(ctx, jid); err == nil && strings.TrimSpace(info.Name) != "" {
			return info.Name
		}
		if fallback != "" {
			return fallback
		}
		return jid.User
	}
	if contact, err := s.store.GetContact(jid.String()); err == nil {
		if name := displayNameFromContact(contact); name != "" && name != contact.JID {
			return name
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if contact, err := s.client.Store.Contacts.GetContact(ctx, jid); err == nil {
		record := ContactRecord{
			JID:          jid.String(),
			Phone:        jid.User,
			FullName:     contact.FullName,
			FirstName:    contact.FirstName,
			PushName:     contact.PushName,
			BusinessName: contact.BusinessName,
			Found:        contact.Found,
			UpdatedAt:    time.Now(),
		}
		_ = s.store.UpsertContact(record)
		if name := displayNameFromContact(record); name != "" && name != record.JID {
			return name
		}
	}
	if fallback != "" {
		return fallback
	}
	return jid.User
}

func isUsableChatName(name, jid string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == jid {
		return false
	}
	jidUser := jid
	if idx := strings.Index(jid, "@"); idx > 0 {
		jidUser = jid[:idx]
	}
	if name == jidUser || isNumericName(name) {
		return false
	}
	return true
}

func (s *Service) RefreshMissingChatNames(ctx context.Context, limit int) error {
	chats, err := s.store.ListChats("all", 0, "")
	if err != nil {
		return err
	}
	refreshed := 0
	for _, chat := range chats {
		if isUsableChatName(chat.Name, chat.JID) {
			continue
		}
		jid, err := types.ParseJID(chat.JID)
		if err != nil {
			continue
		}
		name := s.resolveJIDName(jid, "")
		if !isUsableChatName(name, chat.JID) {
			continue
		}
		if _, err := s.store.EnsureChat(chat.JID, name, chat.IsGroup, chat.LastMessageAt, chat.LastMessagePreview, false); err != nil {
			s.log.Warnf("refresh chat name %s: %v", chat.JID, err)
			continue
		}
		refreshed++
		if limit > 0 && refreshed >= limit {
			break
		}
	}
	return nil
}

func (s *Service) onLiveMessage(evt *events.Message) {
	if evt == nil || evt.Message == nil {
		return
	}
	chatJID := evt.Info.Chat.String()
	record := s.recordFromEventMessage(evt)
	if record.ID == "" || (record.Content == "" && record.MediaType == "" && record.MessageType == "") {
		return
	}
	chatName := s.resolveJIDName(evt.Info.Chat, evt.Info.PushName)
	chat, err := s.store.EnsureChat(chatJID, chatName, evt.Info.Chat.Server == types.GroupServer, record.Timestamp, buildPreview(record), false)
	if err != nil {
		s.log.Warnf("ensure live chat %s: %v", chatJID, err)
		return
	}
	if err := s.store.StoreMessage(record); err != nil {
		s.log.Warnf("store live message %s: %v", record.ID, err)
		return
	}
	s.ensureContactFromMessage(record, evt.Info.PushName)
	if record.IsFromMe {
		_ = s.store.AddAppLog("info", "message", fmt.Sprintf("Observed outgoing %s in %s", record.MessageType, chat.JID), map[string]any{
			"chat_jid":     chat.JID,
			"message_id":   record.ID,
			"message_type": record.MessageType,
			"is_from_me":   true,
			"source":       "whatsapp_event",
		})
	} else {
		_ = s.store.AddAppLog("info", "message", fmt.Sprintf("Received incoming %s in %s", record.MessageType, chat.JID), map[string]any{
			"chat_jid":     chat.JID,
			"message_id":   record.ID,
			"message_type": record.MessageType,
			"is_from_me":   false,
			"source":       "whatsapp_event",
		})
	}

	if record.IsFromMe {
		if !chat.Locked {
			s.dispatchWebhook("outgoing_message", s.buildMessageWebhookPayload(chat, record, "whatsapp_event"))
		}
		return
	}
	if chat.Locked {
		return
	}
	dndMode, err := s.store.GetDNDMode()
	if err != nil || !dndMode {
		return
	}
	s.dispatchWebhook("incoming_message", s.buildMessageWebhookPayload(chat, record, "whatsapp_event"))
	go s.maybeSendAutoReply(chat, record)
}

func (s *Service) maybeSendAutoReply(chat ChatRecord, incoming MessageRecord) {
	rules, err := s.store.ListAutoReplies()
	if err != nil {
		s.log.Warnf("list auto replies: %v", err)
		return
	}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if chat.IsGroup && !rule.ApplyToGroups {
			continue
		}
		if !chat.IsGroup && !rule.ApplyToDMs {
			continue
		}
		if !autoReplyMatches(rule, incoming.Content) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		record, err := s.sendMessageDirect(ctx, mustParseJID(chat.JID), rule.ReplyText, rule.MediaPath, false, "")
		cancel()
		if err != nil {
			s.log.Warnf("send auto reply to %s: %v", chat.JID, err)
			return
		}
		payload := s.buildMessageWebhookPayload(chat, record, "auto_reply")
		payload["trigger_message"] = incoming
		payload["reply_rule"] = rule
		payload["reply_message"] = record
		s.dispatchWebhook("auto_reply_sent", payload)
		return
	}
}

func autoReplyMatches(rule AutoReplyRule, text string) bool {
	switch strings.ToLower(strings.TrimSpace(rule.MatchType)) {
	case "always":
		return true
	case "exact":
		return strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(rule.Pattern))
	case "contains":
		return strings.Contains(strings.ToLower(text), strings.ToLower(rule.Pattern))
	case "prefix":
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), strings.ToLower(strings.TrimSpace(rule.Pattern)))
	case "suffix":
		return strings.HasSuffix(strings.ToLower(strings.TrimSpace(text)), strings.ToLower(strings.TrimSpace(rule.Pattern)))
	case "regex":
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return false
		}
		return re.MatchString(text)
	default:
		return false
	}
}

func mustParseJID(jid string) types.JID {
	parsed, _ := types.ParseJID(jid)
	return parsed
}

func (s *Service) ensureContactFromMessage(record MessageRecord, fallbackName string) {
	if strings.HasSuffix(record.ChatJID, "@g.us") {
		return
	}
	if record.SenderJID == "" {
		return
	}
	phone := record.SenderJID
	if strings.Contains(phone, "@") {
		phone = strings.Split(phone, "@")[0]
	}
	phone = NormalizePhone(phone)
	contact := ContactRecord{
		JID:       record.SenderJID,
		Phone:     phone,
		FullName:  fallbackName,
		PushName:  fallbackName,
		Found:     true,
		UpdatedAt: time.Now(),
	}
	_ = s.store.UpsertContact(contact)
}

func buildPreview(record MessageRecord) string {
	if record.Content != "" {
		return record.Content
	}
	if record.MediaType != "" {
		if record.FileName != "" {
			return fmt.Sprintf("[%s] %s", record.MediaType, record.FileName)
		}
		return fmt.Sprintf("[%s]", record.MediaType)
	}
	if record.MessageType != "" {
		return fmt.Sprintf("[%s]", record.MessageType)
	}
	return ""
}

func (s *Service) recordFromEventMessage(evt *events.Message) MessageRecord {
	record := extractMessageRecord(evt.Message)
	record.ID = evt.Info.ID
	record.ChatJID = evt.Info.Chat.String()
	record.Timestamp = evt.Info.Timestamp
	record.IsFromMe = evt.Info.IsFromMe
	if evt.Info.IsFromMe {
		record.SenderJID = s.CurrentUserJID()
	} else {
		record.SenderJID = evt.Info.Sender.String()
	}
	s.annotateAddressing(&record, evt.Message)
	return record
}

// selfUserParts returns the user-parts of THIS account's identities (phone JID
// and LID). Group mentions/quotes reference either, so both are checked.
func (s *Service) selfUserParts() map[string]struct{} {
	out := map[string]struct{}{}
	if s.client == nil || s.client.Store == nil {
		return out
	}
	if s.client.Store.ID != nil && s.client.Store.ID.User != "" {
		out[s.client.Store.ID.User] = struct{}{}
	}
	if !s.client.Store.LID.IsEmpty() && s.client.Store.LID.User != "" {
		out[s.client.Store.LID.User] = struct{}{}
	}
	return out
}

// messageContextInfo returns the ContextInfo of whichever message body carries
// it (text/media), so quote+mention detection works across message kinds.
func messageContextInfo(msg *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case msg.GetExtendedTextMessage() != nil:
		return msg.GetExtendedTextMessage().GetContextInfo()
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetContextInfo()
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetContextInfo()
	case msg.GetDocumentMessage() != nil:
		return msg.GetDocumentMessage().GetContextInfo()
	case msg.GetAudioMessage() != nil:
		return msg.GetAudioMessage().GetContextInfo()
	}
	return nil
}

// annotateAddressing sets MentionsMe / QuotedIsFromMe by comparing the message's
// mentioned JIDs and quoted-message sender against THIS account's own identities
// — fully generic, no configured numbers.
func (s *Service) annotateAddressing(record *MessageRecord, msg *waE2E.Message) {
	if msg == nil {
		return
	}
	ctx := messageContextInfo(msg)
	if ctx == nil {
		return
	}
	self := s.selfUserParts()
	if len(self) == 0 {
		return
	}
	if p := ctx.GetParticipant(); p != "" {
		if jid, err := types.ParseJID(p); err == nil {
			if _, ok := self[jid.User]; ok {
				record.QuotedIsFromMe = true
			}
		}
	}
	mentioned := ctx.GetMentionedJID()
	record.MentionCount = len(mentioned)
	for _, m := range mentioned {
		if jid, err := types.ParseJID(m); err == nil {
			if _, ok := self[jid.User]; ok {
				record.MentionsMe = true
				break
			}
		}
	}
}

func (s *Service) recordFromWebMessage(chatJID string, msg *waWeb.WebMessageInfo) MessageRecord {
	record := extractMessageRecord(msg.GetMessage())
	record.ChatJID = chatJID
	record.Timestamp = time.Unix(int64(msg.GetMessageTimestamp()), 0)
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	if key := msg.GetKey(); key != nil {
		record.ID = key.GetID()
		record.IsFromMe = key.GetFromMe()
		switch {
		case record.IsFromMe:
			record.SenderJID = s.CurrentUserJID()
		case key.GetParticipant() != "":
			record.SenderJID = key.GetParticipant()
		case msg.GetParticipant() != "":
			record.SenderJID = msg.GetParticipant()
		default:
			record.SenderJID = chatJID
		}
	}
	return record
}

func extractMessageRecord(msg *waE2E.Message) MessageRecord {
	record := MessageRecord{
		MessageType: "text",
	}
	if msg == nil {
		return record
	}

	if quoted := extractQuotedContent(msg); quoted != "" {
		record.Content = fmt.Sprintf("[replying to: %s] %s", quoted, extractPrimaryContent(msg))
	} else {
		record.Content = extractPrimaryContent(msg)
	}

	switch {
	case msg.GetImageMessage() != nil:
		image := msg.GetImageMessage()
		record.MessageType = "image"
		record.MediaType = "image"
		record.MimeType = image.GetMimetype()
		record.URL = image.GetURL()
		record.DirectPath = image.GetDirectPath()
		record.FileLength = image.GetFileLength()
		record.MediaKey = image.GetMediaKey()
		record.FileSHA256 = image.GetFileSHA256()
		record.FileEncSHA256 = image.GetFileEncSHA256()
		if record.FileName == "" {
			record.FileName = generatedMediaFilename("image", ".jpg")
		}
	case msg.GetVideoMessage() != nil:
		video := msg.GetVideoMessage()
		record.MessageType = "video"
		record.MediaType = "video"
		record.MimeType = video.GetMimetype()
		record.URL = video.GetURL()
		record.DirectPath = video.GetDirectPath()
		record.FileLength = video.GetFileLength()
		record.MediaKey = video.GetMediaKey()
		record.FileSHA256 = video.GetFileSHA256()
		record.FileEncSHA256 = video.GetFileEncSHA256()
		if record.FileName == "" {
			record.FileName = generatedMediaFilename("video", ".mp4")
		}
	case msg.GetDocumentMessage() != nil:
		doc := msg.GetDocumentMessage()
		record.MessageType = "document"
		record.MediaType = "document"
		record.MimeType = doc.GetMimetype()
		record.FileName = doc.GetFileName()
		record.URL = doc.GetURL()
		record.DirectPath = doc.GetDirectPath()
		record.FileLength = doc.GetFileLength()
		record.MediaKey = doc.GetMediaKey()
		record.FileSHA256 = doc.GetFileSHA256()
		record.FileEncSHA256 = doc.GetFileEncSHA256()
		if record.FileName == "" {
			record.FileName = generatedMediaFilename("document", "")
		}
	case msg.GetAudioMessage() != nil:
		audio := msg.GetAudioMessage()
		record.MessageType = "audio"
		record.MediaType = "audio"
		record.MimeType = audio.GetMimetype()
		record.FileName = generatedMediaFilename("audio", ".ogg")
		record.URL = audio.GetURL()
		record.DirectPath = audio.GetDirectPath()
		record.FileLength = audio.GetFileLength()
		record.MediaKey = audio.GetMediaKey()
		record.FileSHA256 = audio.GetFileSHA256()
		record.FileEncSHA256 = audio.GetFileEncSHA256()
	case msg.GetStickerMessage() != nil:
		sticker := msg.GetStickerMessage()
		record.MessageType = "sticker"
		record.MediaType = "sticker"
		record.MimeType = sticker.GetMimetype()
		record.FileName = generatedMediaFilename("sticker", ".webp")
		record.URL = sticker.GetURL()
		record.DirectPath = sticker.GetDirectPath()
		record.FileLength = sticker.GetFileLength()
		record.MediaKey = sticker.GetMediaKey()
		record.FileSHA256 = sticker.GetFileSHA256()
		record.FileEncSHA256 = sticker.GetFileEncSHA256()
		if record.Content == "" {
			record.Content = "[sticker]"
		}
	case msg.GetLocationMessage() != nil:
		location := msg.GetLocationMessage()
		record.MessageType = "location"
		record.Content = fmt.Sprintf("[location] %.6f, %.6f", location.GetDegreesLatitude(), location.GetDegreesLongitude())
	case msg.GetLiveLocationMessage() != nil:
		location := msg.GetLiveLocationMessage()
		record.MessageType = "live_location"
		record.Content = fmt.Sprintf("[live location] %.6f, %.6f", location.GetDegreesLatitude(), location.GetDegreesLongitude())
	case msg.GetContactMessage() != nil:
		contact := msg.GetContactMessage()
		record.MessageType = "contact"
		record.Content = fmt.Sprintf("[contact] %s", contact.GetDisplayName())
	case msg.GetConversation() != "":
		record.MessageType = "text"
	case msg.GetExtendedTextMessage() != nil:
		record.MessageType = "text"
	default:
		record.MessageType = "unknown"
	}

	if record.Content == "" && record.MediaType != "" {
		record.Content = fmt.Sprintf("[%s]", record.MediaType)
	}
	return record
}

func extractPrimaryContent(msg *waE2E.Message) string {
	switch {
	case msg == nil:
		return ""
	case msg.GetConversation() != "":
		return msg.GetConversation()
	case msg.GetExtendedTextMessage() != nil:
		return msg.GetExtendedTextMessage().GetText()
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetCaption()
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetCaption()
	case msg.GetDocumentMessage() != nil:
		return msg.GetDocumentMessage().GetCaption()
	case msg.GetAudioMessage() != nil:
		return "[audio]"
	default:
		return ""
	}
}

func extractQuotedContent(msg *waE2E.Message) string {
	if msg == nil || msg.GetExtendedTextMessage() == nil {
		return ""
	}
	ctx := msg.GetExtendedTextMessage().GetContextInfo()
	if ctx == nil || ctx.GetQuotedMessage() == nil {
		return ""
	}
	return extractPrimaryContent(ctx.GetQuotedMessage())
}

func generatedMediaFilename(prefix, suffix string) string {
	return fmt.Sprintf("%s_%s%s", prefix, time.Now().UTC().Format("20060102_150405"), suffix)
}

func mimeTypeFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".avi":
		return "video/avi"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".ogg":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	}
	if detected := mime.TypeByExtension(ext); detected != "" {
		return detected
	}
	return "application/octet-stream"
}

func mediaTypeForKind(kind string) (whatsmeow.MediaType, string) {
	switch kind {
	case "image", "sticker":
		return whatsmeow.MediaImage, "image"
	case "video":
		return whatsmeow.MediaVideo, "video"
	case "audio":
		return whatsmeow.MediaAudio, "audio"
	default:
		return whatsmeow.MediaDocument, "document"
	}
}

func mediaKindFromPath(path string) (whatsmeow.MediaType, string, string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return whatsmeow.MediaImage, "image", mimeTypeFromPath(path), nil
	case ".mp4", ".mov", ".avi", ".mkv", ".webm":
		return whatsmeow.MediaVideo, "video", mimeTypeFromPath(path), nil
	case ".ogg", ".mp3", ".wav", ".m4a", ".aac":
		return whatsmeow.MediaDocument, "audio", mimeTypeFromPath(path), nil
	default:
		return whatsmeow.MediaDocument, "document", mimeTypeFromPath(path), nil
	}
}

// digitsOnly strips a JID down to its numeric user part ("9199…@lid" -> "9199…").
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r == '@' || r == ':' {
			break
		}
	}
	return b.String()
}

// mentionPattern matches "@<6+ digits>" — how a mention is written in text.
var mentionPattern = regexp.MustCompile(`@(\d{6,})`)

// attachMentions scans the outgoing text for "@<number>" tokens, resolves each
// to a real JID, and records them in the message's ContextInfo.MentionedJID so
// WhatsApp renders them as proper @Name mentions instead of raw numbers. Any
// number that can't be resolved is left as-is (plain text, no worse than
// before). A plain Conversation is upgraded to an ExtendedTextMessage in place,
// since only that (or a media message) can carry mention context.
func (s *Service) attachMentions(msg *waE2E.Message, text, chatJID string) {
	matches := mentionPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return
	}
	seen := map[string]bool{}
	var jids []string
	for _, m := range matches {
		number := m[1]
		// Prefer the REAL JID from history (gets @lid vs @s.whatsapp.net right);
		// fall back to the resolver only if the number is otherwise unknown.
		jid := s.store.JIDForNumber(number, chatJID)
		if jid == "" {
			if target, err := s.ResolveBestTarget(number, "chat", true); err == nil && target.JID != "" && !target.IsGroup {
				jid = target.JID
			}
		}
		if jid == "" || seen[jid] {
			continue
		}
		seen[jid] = true
		jids = append(jids, jid)
	}
	if len(jids) == 0 {
		return
	}
	setMentionedJIDs(msg, jids)
}

// setMentionedJIDs attaches the mentioned JIDs to whichever message body can
// carry ContextInfo, creating/merging ContextInfo as needed.
func setMentionedJIDs(msg *waE2E.Message, jids []string) {
	ensure := func(ci *waE2E.ContextInfo) *waE2E.ContextInfo {
		if ci == nil {
			ci = &waE2E.ContextInfo{}
		}
		ci.MentionedJID = append(ci.MentionedJID, jids...)
		return ci
	}
	switch {
	case msg.Conversation != nil:
		msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{
			Text:        proto.String(msg.GetConversation()),
			ContextInfo: ensure(nil),
		}
		msg.Conversation = nil
	case msg.ExtendedTextMessage != nil:
		msg.ExtendedTextMessage.ContextInfo = ensure(msg.ExtendedTextMessage.GetContextInfo())
	case msg.ImageMessage != nil:
		msg.ImageMessage.ContextInfo = ensure(msg.ImageMessage.GetContextInfo())
	case msg.VideoMessage != nil:
		msg.VideoMessage.ContextInfo = ensure(msg.VideoMessage.GetContextInfo())
	case msg.DocumentMessage != nil:
		msg.DocumentMessage.ContextInfo = ensure(msg.DocumentMessage.GetContextInfo())
	}
}

// attachQuotedReply turns an outgoing message into a reply that quotes
// replyToID. WhatsApp can only carry quote context on an ExtendedTextMessage
// (or a media message), so a plain Conversation is upgraded in place.
func (s *Service) attachQuotedReply(msg *waE2E.Message, chatJID types.JID, replyToID string) error {
	quoted, err := s.store.GetMessage(replyToID, chatJID.String())
	if err != nil {
		return fmt.Errorf("quoted message not found: %w", err)
	}
	participant := strings.TrimSpace(quoted.SenderJID)
	if participant == "" {
		participant = chatJID.String()
	}
	info := &waE2E.ContextInfo{
		StanzaID:      proto.String(replyToID),
		Participant:   proto.String(participant),
		QuotedMessage: &waE2E.Message{Conversation: proto.String(quoted.Content)},
	}
	switch {
	case msg.Conversation != nil:
		msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{
			Text:        proto.String(msg.GetConversation()),
			ContextInfo: info,
		}
		msg.Conversation = nil
	case msg.ExtendedTextMessage != nil:
		msg.ExtendedTextMessage.ContextInfo = info
	case msg.ImageMessage != nil:
		msg.ImageMessage.ContextInfo = info
	case msg.VideoMessage != nil:
		msg.VideoMessage.ContextInfo = info
	case msg.DocumentMessage != nil:
		msg.DocumentMessage.ContextInfo = info
	case msg.AudioMessage != nil:
		msg.AudioMessage.ContextInfo = info
	default:
		return errors.New("message kind cannot carry a quote")
	}
	return nil
}

func (s *Service) buildOutgoingMessage(ctx context.Context, text, mediaPath string) (*waE2E.Message, string, string, error) {
	text = strings.TrimSpace(text)
	if mediaPath == "" {
		if text == "" {
			return nil, "", "", errors.New("text or media_path required")
		}
		return &waE2E.Message{Conversation: proto.String(text)}, "text", "", nil
	}
	data, err := os.ReadFile(mediaPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("read media file: %w", err)
	}
	mediaType, kind, mimeType, err := mediaKindFromPath(mediaPath)
	if err != nil {
		return nil, "", "", err
	}
	upload, err := s.client.Upload(ctx, data, mediaType)
	if err != nil {
		return nil, "", "", fmt.Errorf("upload media: %w", err)
	}

	switch kind {
	case "image":
		return &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Caption:       proto.String(text),
				Mimetype:      proto.String(mimeType),
				URL:           &upload.URL,
				DirectPath:    &upload.DirectPath,
				MediaKey:      upload.MediaKey,
				FileEncSHA256: upload.FileEncSHA256,
				FileSHA256:    upload.FileSHA256,
				FileLength:    &upload.FileLength,
			},
		}, "image", kind, nil
	case "video":
		return &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				Caption:       proto.String(text),
				Mimetype:      proto.String(mimeType),
				URL:           &upload.URL,
				DirectPath:    &upload.DirectPath,
				MediaKey:      upload.MediaKey,
				FileEncSHA256: upload.FileEncSHA256,
				FileSHA256:    upload.FileSHA256,
				FileLength:    &upload.FileLength,
			},
		}, "video", kind, nil
	default:
		return &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				FileName:      proto.String(filepath.Base(mediaPath)),
				Caption:       proto.String(text),
				Mimetype:      proto.String(mimeType),
				URL:           &upload.URL,
				DirectPath:    &upload.DirectPath,
				MediaKey:      upload.MediaKey,
				FileEncSHA256: upload.FileEncSHA256,
				FileSHA256:    upload.FileSHA256,
				FileLength:    &upload.FileLength,
				Title:         proto.String(filepath.Base(mediaPath)),
			},
		}, kind, kind, nil
	}
}

func (s *Service) ensureAutomationAllowed(jid types.JID) error {
	dndMode, err := s.store.GetDNDMode()
	if err != nil {
		return err
	}
	if !dndMode {
		return errors.New("DND mode is off; automation is disabled")
	}
	chat, err := s.store.GetChat(jid.String())
	if err == nil && chat.Locked {
		return fmt.Errorf("chat %s is locked", jid.String())
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func (s *Service) ResolveTargets(ref, kind string, limit int, allowDirect bool) ([]ResolvedTarget, error) {
	return ResolveTargets(s.store, ref, ResolveOptions{
		Kind:        kind,
		Limit:       limit,
		AllowDirect: allowDirect,
	})
}

func (s *Service) ResolveBestTarget(ref, kind string, allowDirect bool) (ResolvedTarget, error) {
	return ResolveBestTarget(s.store, ref, ResolveOptions{
		Kind:        kind,
		Limit:       10,
		AllowDirect: allowDirect,
	})
}

func (s *Service) SendMessage(ctx context.Context, recipient, text, mediaPath string) (MessageRecord, error) {
	return s.SendMessageReplying(ctx, recipient, text, mediaPath, "")
}

// SendMessageReplying sends a message that, when replyToID is a message ID in
// the same chat, is delivered as a WhatsApp REPLY quoting that message — so the
// recipient sees which message it answers.
func (s *Service) SendMessageReplying(ctx context.Context, recipient, text, mediaPath, replyToID string) (MessageRecord, error) {
	target, err := s.ResolveBestTarget(recipient, "chat", true)
	if err != nil {
		return MessageRecord{}, err
	}
	// A bare phone number resolves to "<digits>@s.whatsapp.net" by default, but
	// many contacts only exist under an "@lid" identity — sending to the wrong
	// server fails with "no LID found for ...". If the resolved target isn't a
	// chat/contact we actually know, prefer the real JID seen in history.
	resolved := target.JID
	if !target.ExistsInChats && !target.ExistsInContacts {
		if real := s.store.JIDForNumber(digitsOnly(resolved), ""); real != "" && real != resolved {
			s.log.Infof("send: %s is not a known chat; using %s from history", resolved, real)
			resolved = real
		}
	}
	jid, err := types.ParseJID(resolved)
	if err != nil {
		return MessageRecord{}, err
	}
	if err := s.ensureAutomationAllowed(jid); err != nil {
		return MessageRecord{}, err
	}
	return s.sendMessageDirect(ctx, jid, text, mediaPath, true, replyToID)
}

func (s *Service) sendMessageDirect(ctx context.Context, jid types.JID, text, mediaPath string, emitWebhook bool, replyToID string) (MessageRecord, error) {
	if !s.client.IsConnected() {
		return MessageRecord{}, errors.New("WhatsApp client is not connected")
	}
	msg, messageType, _, err := s.buildOutgoingMessage(ctx, text, mediaPath)
	if err != nil {
		return MessageRecord{}, err
	}
	if strings.TrimSpace(replyToID) != "" {
		if err := s.attachQuotedReply(msg, jid, strings.TrimSpace(replyToID)); err != nil {
			// A missing/unknown quote target shouldn't block the message.
			s.log.Warnf("reply-to %s: %v (sending unquoted)", replyToID, err)
		}
	}
	// Turn "@<number>" tokens in the text into real WhatsApp mentions, so they
	// render as the contact's name instead of a raw number.
	s.attachMentions(msg, text, jid.String())
	resp, err := s.client.SendMessage(ctx, jid, msg)
	if err != nil {
		return MessageRecord{}, err
	}
	record := extractMessageRecord(msg)
	record.ID = string(resp.ID)
	record.ChatJID = jid.String()
	record.SenderJID = s.CurrentUserJID()
	record.Timestamp = resp.Timestamp
	record.IsFromMe = true
	if record.MessageType == "" {
		record.MessageType = messageType
	}
	if record.FileName == "" && mediaPath != "" {
		record.FileName = filepath.Base(mediaPath)
	}
	chatName := s.resolveJIDName(jid, "")
	chat, err := s.store.EnsureChat(jid.String(), chatName, jid.Server == types.GroupServer, record.Timestamp, buildPreview(record), false)
	if err != nil {
		return MessageRecord{}, err
	}
	if err := s.store.StoreMessage(record); err != nil {
		return MessageRecord{}, err
	}
	_ = s.store.AddAppLog("info", "message", fmt.Sprintf("Sent %s to %s", record.MessageType, jid.String()), map[string]any{
		"chat_jid":     jid.String(),
		"message_id":   record.ID,
		"message_type": record.MessageType,
		"media_path":   mediaPath,
		"source":       "wacli_send",
	})
	if emitWebhook && !chat.Locked {
		s.dispatchWebhook("outgoing_message", s.buildMessageWebhookPayload(chat, record, "wacli_api"))
	}
	return record, nil
}

// EditMessage edits a previously-sent message's text (WhatsApp shows "edited").
func (s *Service) EditMessage(ctx context.Context, chatRef, messageID, newText string) (MessageRecord, error) {
	if !s.client.IsConnected() {
		return MessageRecord{}, errors.New("WhatsApp client is not connected")
	}
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(newText) == "" {
		return MessageRecord{}, errors.New("chat, id and text are required")
	}
	resolved, err := s.ResolveBestTarget(chatRef, "chat", true)
	if err != nil {
		return MessageRecord{}, err
	}
	chatJID, err := types.ParseJID(resolved.JID)
	if err != nil {
		return MessageRecord{}, err
	}
	edit := s.client.BuildEdit(chatJID, types.MessageID(messageID), &waE2E.Message{
		Conversation: proto.String(newText),
	})
	if _, err := s.client.SendMessage(ctx, chatJID, edit); err != nil {
		return MessageRecord{}, err
	}
	if err := s.store.UpdateMessageContent(messageID, chatJID.String(), newText); err != nil {
		return MessageRecord{}, err
	}
	_ = s.store.AddAppLog("info", "message", fmt.Sprintf("Edited message %s in %s", messageID, chatJID.String()), map[string]any{
		"chat_jid": chatJID.String(), "message_id": messageID, "source": "wacli_edit",
	})
	rec, _ := s.store.GetMessage(messageID, chatJID.String())
	return rec, nil
}

// RevokeMessage deletes/revokes a message for everyone.
func (s *Service) RevokeMessage(ctx context.Context, chatRef, messageID string) error {
	if !s.client.IsConnected() {
		return errors.New("WhatsApp client is not connected")
	}
	if strings.TrimSpace(messageID) == "" {
		return errors.New("chat and id are required")
	}
	resolved, err := s.ResolveBestTarget(chatRef, "chat", true)
	if err != nil {
		return err
	}
	chatJID, err := types.ParseJID(resolved.JID)
	if err != nil {
		return err
	}
	// EmptyJID as sender revokes our own message.
	revoke := s.client.BuildRevoke(chatJID, types.EmptyJID, types.MessageID(messageID))
	if _, err := s.client.SendMessage(ctx, chatJID, revoke); err != nil {
		return err
	}
	_ = s.store.MarkMessageDeleted(messageID, chatJID.String())
	_ = s.store.AddAppLog("info", "message", fmt.Sprintf("Deleted message %s in %s", messageID, chatJID.String()), map[string]any{
		"chat_jid": chatJID.String(), "message_id": messageID, "source": "wacli_delete",
	})
	return nil
}

// MessageReceipts returns the delivery/read receipts recorded for a message.
func (s *Service) MessageReceipts(messageID string) ([]ReceiptRecord, error) {
	if strings.TrimSpace(messageID) == "" {
		return nil, errors.New("id is required")
	}
	return s.store.ListReceipts(messageID)
}

// onReceipt records delivery/read receipts so we can report who has seen a
// message. evt.Sender is the recipient reporting the receipt.
func (s *Service) onReceipt(evt *events.Receipt) {
	recipient := evt.Sender.ToNonAD().String()
	rtype := receiptTypeLabel(evt.Type)
	for _, id := range evt.MessageIDs {
		if err := s.store.StoreReceipt(ReceiptRecord{
			MessageID:    string(id),
			ChatJID:      evt.Chat.String(),
			RecipientJID: recipient,
			Type:         rtype,
			Timestamp:    evt.Timestamp,
		}); err != nil {
			s.log.Warnf("store receipt: %v", err)
		}
	}
}

// receiptTypeLabel maps whatsmeow receipt types to friendly labels.
func receiptTypeLabel(t types.ReceiptType) string {
	switch t {
	case types.ReceiptTypeDelivered:
		return "delivered"
	case types.ReceiptTypeRead:
		return "read"
	case types.ReceiptTypeReadSelf:
		return "read-self"
	case types.ReceiptTypePlayed:
		return "played"
	case types.ReceiptTypePlayedSelf:
		return "played-self"
	default:
		return string(t)
	}
}

type BulkSendItem struct {
	To        string `json:"to"`
	Text      string `json:"text,omitempty"`
	Message   string `json:"message,omitempty"`
	MediaPath string `json:"media_path,omitempty"`
}

type BulkSendResult struct {
	To       string         `json:"to"`
	Resolved ResolvedTarget `json:"resolved,omitempty"`
	Success  bool           `json:"success"`
	Error    string         `json:"error,omitempty"`
	Message  MessageRecord  `json:"message,omitempty"`
}

func (s *Service) BulkSend(ctx context.Context, items []BulkSendItem, interval time.Duration) ([]BulkSendResult, error) {
	results := make([]BulkSendResult, 0, len(items))
	for idx, item := range items {
		text := item.Text
		if text == "" {
			text = item.Message
		}
		target, resolveErr := s.ResolveBestTarget(item.To, "chat", true)
		if resolveErr != nil {
			results = append(results, BulkSendResult{
				To:      item.To,
				Success: false,
				Error:   resolveErr.Error(),
			})
			continue
		}
		record, err := s.SendMessage(ctx, target.JID, text, item.MediaPath)
		if err != nil {
			results = append(results, BulkSendResult{
				To:       item.To,
				Resolved: target,
				Success:  false,
				Error:    err.Error(),
			})
		} else {
			results = append(results, BulkSendResult{
				To:       item.To,
				Resolved: target,
				Success:  true,
				Message:  record,
			})
		}
		if interval > 0 && idx < len(items)-1 {
			select {
			case <-ctx.Done():
				return results, ctx.Err()
			case <-time.After(interval):
			}
		}
	}
	return results, nil
}

func (s *Service) SearchMessages(chatRef, senderRef, query string, limit int, mediaOnly bool, fromMe string) ([]MessageRecord, *ResolvedTarget, *ResolvedTarget, error) {
	return s.SearchMessagesAdvanced(chatRef, senderRef, query, limit, mediaOnly, fromMe, nil, nil)
}

func (s *Service) SearchMessagesAdvanced(chatRef, senderRef, query string, limit int, mediaOnly bool, fromMe string, before, after *time.Time) ([]MessageRecord, *ResolvedTarget, *ResolvedTarget, error) {
	opts := MessageSearchOptions{
		Query:     query,
		Limit:     limit,
		MediaOnly: mediaOnly,
		Before:    before,
		After:     after,
	}
	var resolvedChat *ResolvedTarget
	var resolvedSender *ResolvedTarget

	if strings.TrimSpace(chatRef) != "" {
		target, err := s.ResolveBestTarget(chatRef, "chat", false)
		if err != nil {
			return nil, nil, nil, err
		}
		opts.ChatJID = target.JID
		copy := target
		resolvedChat = &copy
	}
	if strings.TrimSpace(senderRef) != "" {
		target, err := s.ResolveBestTarget(senderRef, "contact", false)
		if err != nil {
			return nil, resolvedChat, nil, err
		}
		opts.SenderJID = target.JID
		copy := target
		resolvedSender = &copy
	}
	switch strings.ToLower(strings.TrimSpace(fromMe)) {
	case "yes", "true", "from_me":
		value := true
		opts.FromMe = &value
	case "no", "false", "not_me":
		value := false
		opts.FromMe = &value
	}

	records, err := s.store.SearchMessages(opts)
	return records, resolvedChat, resolvedSender, err
}

func (s *Service) SendStory(ctx context.Context, text, mediaPath string) error {
	dndMode, err := s.store.GetDNDMode()
	if err != nil {
		return err
	}
	if !dndMode {
		return errors.New("DND mode is off; automation is disabled")
	}
	if !s.client.IsConnected() {
		return errors.New("WhatsApp client is not connected")
	}
	var message *waE2E.Message
	if mediaPath == "" {
		if strings.TrimSpace(text) == "" {
			return errors.New("text or media_path required")
		}
		message = &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(text),
			},
		}
	} else {
		ext := strings.ToLower(filepath.Ext(mediaPath))
		if !strings.Contains(".jpg,.jpeg,.png,.gif,.webp,.mp4,.mov,.avi,.mkv,.webm", ext) {
			return errors.New("stories currently support image and video files only")
		}
		data, err := os.ReadFile(mediaPath)
		if err != nil {
			return err
		}
		mediaType, kind, mimeType, err := mediaKindFromPath(mediaPath)
		if err != nil {
			return err
		}
		upload, err := s.client.Upload(ctx, data, mediaType)
		if err != nil {
			return err
		}
		switch kind {
		case "image":
			message = &waE2E.Message{
				ImageMessage: &waE2E.ImageMessage{
					URL:           &upload.URL,
					DirectPath:    &upload.DirectPath,
					MediaKey:      upload.MediaKey,
					Mimetype:      proto.String(mimeType),
					FileEncSHA256: upload.FileEncSHA256,
					FileSHA256:    upload.FileSHA256,
					FileLength:    &upload.FileLength,
					Caption:       proto.String(text),
				},
			}
		default:
			message = &waE2E.Message{
				VideoMessage: &waE2E.VideoMessage{
					URL:           &upload.URL,
					DirectPath:    &upload.DirectPath,
					MediaKey:      upload.MediaKey,
					Mimetype:      proto.String(mimeType),
					FileEncSHA256: upload.FileEncSHA256,
					FileSHA256:    upload.FileSHA256,
					FileLength:    &upload.FileLength,
					Caption:       proto.String(text),
				},
			}
		}
	}
	_, err = s.client.SendMessage(ctx, types.StatusBroadcastJID, message)
	return err
}

func (s *Service) DownloadMedia(ctx context.Context, messageID, chatJID string) (string, error) {
	record, err := s.store.GetMessage(messageID, chatJID)
	if err != nil {
		return "", err
	}
	if record.MediaType == "" {
		return "", errors.New("message does not contain media")
	}
	if record.MediaPath != "" && fileExists(record.MediaPath) {
		return record.MediaPath, nil
	}
	if !s.client.IsConnected() {
		return "", errors.New("WhatsApp client is not connected")
	}
	directPath := record.DirectPath
	if directPath == "" {
		directPath = extractDirectPathFromURL(record.URL)
	}
	mediaType, mmsType := mediaTypeForKind(record.MediaType)
	data, err := s.client.DownloadMediaWithPath(ctx, directPath, record.FileEncSHA256, record.FileSHA256, record.MediaKey, mediaType, mmsType, false)
	if err != nil {
		return "", err
	}
	filename := record.FileName
	if filename == "" {
		filename = sanitizeFileName(fmt.Sprintf("%s_%s", record.MediaType, record.ID))
	}
	chatDir := filepath.Join(MediaDir, sanitizePathPart(chatJID))
	if err := os.MkdirAll(chatDir, 0o700); err != nil {
		return "", err
	}
	fullPath := filepath.Join(chatDir, sanitizeFileName(filename))
	if err := os.WriteFile(fullPath, data, 0o600); err != nil {
		return "", err
	}
	if err := s.store.UpdateMessageMediaPath(messageID, chatJID, fullPath); err != nil {
		s.log.Warnf("update media path for %s/%s: %v", chatJID, messageID, err)
	}
	return fullPath, nil
}

func (s *Service) buildMessageWebhookPayload(chat ChatRecord, message MessageRecord, source string) map[string]any {
	recent, _ := s.store.ListMessages(chat.JID, 12)
	payload := map[string]any{
		"chat":            chat,
		"message":         message,
		"recent_messages": recent,
		"message_kinds":   webhookMessageKinds(message),
		"source":          source,
	}
	if contact := s.lookupContactByJID(chat.JID); contact != nil {
		payload["chat_contact"] = contact
	}
	if contact := s.lookupContactByJID(message.SenderJID); contact != nil {
		payload["sender_contact"] = contact
	}
	return payload
}

func (s *Service) lookupContactByJID(jid string) *ContactRecord {
	if strings.TrimSpace(jid) == "" {
		return nil
	}
	contact, err := s.store.GetContact(jid)
	if err != nil {
		return nil
	}
	return cloneContact(contact)
}

func extractDirectPathFromURL(url string) string {
	if url == "" {
		return ""
	}
	parts := strings.SplitN(url, ".net/", 2)
	if len(parts) != 2 {
		return ""
	}
	pathPart := strings.SplitN(parts[1], "?", 2)[0]
	return "/" + pathPart
}

func (s *Service) dispatchWebhook(event string, payload map[string]any) {
	dndMode, err := s.store.GetDNDMode()
	if err != nil || !dndMode {
		return
	}
	webhooks, err := s.store.ListWebhooks()
	if err != nil {
		s.log.Warnf("list webhooks: %v", err)
		return
	}
	if len(webhooks) == 0 {
		return
	}
	generatedAt := time.Now().UTC().Format(time.RFC3339)
	for _, webhook := range webhooks {
		if !webhook.Enabled || !webhookWantsEvent(webhook, event) || !webhookMatchesPayload(webhook, payload) {
			continue
		}
		envelopePayload := s.webhookPayloadForDelivery(webhook, payload)
		body, err := json.Marshal(map[string]any{
			"event":        event,
			"generated_at": generatedAt,
			"webhook": map[string]any{
				"id":            webhook.ID,
				"scope":         webhook.Scope,
				"chat_jids":     webhook.ChatJIDs,
				"message_types": webhook.MessageTypes,
				"context_limit": webhook.ContextLimit,
			},
			"payload": envelopePayload,
		})
		if err != nil {
			continue
		}
		chatJID, messageID := deliveryPayloadIdentifiers(envelopePayload)
		deliveryID, err := s.store.StartWebhookDelivery(webhook.ID, webhook.URL, event, chatJID, messageID, string(body))
		if err != nil {
			s.log.Warnf("start webhook delivery %d/%s/%s: %v", webhook.ID, chatJID, messageID, err)
			deliveryID = 0
		}
		go s.postWebhook(webhook, event, deliveryID, body)
	}
}

func webhookWantsEvent(webhook WebhookRecord, event string) bool {
	for _, candidate := range webhook.Events {
		candidate = strings.TrimSpace(strings.ToLower(candidate))
		if candidate == "*" || candidate == strings.ToLower(event) {
			return true
		}
	}
	return false
}

func webhookMatchesPayload(webhook WebhookRecord, payload map[string]any) bool {
	message, hasMessage := webhookPayloadMessage(payload)
	if chat, ok := webhookPayloadChat(payload); ok {
		if chat.Locked {
			return false
		}
		if webhook.Scope == "selected_chats" && !stringInList(chat.JID, webhook.ChatJIDs) {
			// Out of scope. Still deliver if this webhook opted into mention
			// delivery and the account was @-mentioned — being summoned should
			// reach the consumer from anywhere. What to do with it (e.g. ignore
			// "@all" blasts) is the consumer's policy, not ours.
			if !(webhook.IncludeMentions && hasMessage && message.MentionsMe) {
				return false
			}
		}
	}
	if hasMessage {
		return webhookWantsMessageType(webhook, message)
	}
	return true
}

func webhookPayloadChat(payload map[string]any) (ChatRecord, bool) {
	if payload == nil {
		return ChatRecord{}, false
	}
	chat, ok := payload["chat"].(ChatRecord)
	return chat, ok
}

func webhookPayloadMessage(payload map[string]any) (MessageRecord, bool) {
	if payload == nil {
		return MessageRecord{}, false
	}
	keys := []string{"message", "reply_message", "incoming_message", "trigger_message"}
	for _, key := range keys {
		if message, ok := payload[key].(MessageRecord); ok {
			return message, true
		}
	}
	return MessageRecord{}, false
}

func webhookWantsMessageType(webhook WebhookRecord, message MessageRecord) bool {
	return messageKindsMatch(webhook.MessageTypes, message)
}

func webhookMessageKinds(message MessageRecord) []string {
	values := []string{"message"}
	if kind := strings.ToLower(strings.TrimSpace(message.MessageType)); kind != "" {
		values = append(values, kind)
	}
	if media := strings.ToLower(strings.TrimSpace(message.MediaType)); media != "" {
		values = append(values, "media", media)
	}
	return normalizeStringList(values, true)
}

func stringInList(value string, values []string) bool {
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func (s *Service) webhookPayloadForDelivery(webhook WebhookRecord, payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	if chat, ok := webhookPayloadChat(payload); ok && webhook.ContextLimit > 0 {
		recent, _ := s.store.ListMessages(chat.JID, webhook.ContextLimit)
		out["recent_messages"] = recent
	}
	return out
}

func deliveryPayloadIdentifiers(payload map[string]any) (string, string) {
	var chatJID, messageID string
	if chat, ok := webhookPayloadChat(payload); ok {
		chatJID = chat.JID
	}
	if message, ok := webhookPayloadMessage(payload); ok {
		messageID = message.ID
		if chatJID == "" {
			chatJID = message.ChatJID
		}
	}
	return chatJID, messageID
}

func (s *Service) postWebhook(webhook WebhookRecord, event string, deliveryID int64, body []byte) {
	req, err := http.NewRequest(http.MethodPost, webhook.URL, strings.NewReader(string(body)))
	if err != nil {
		s.log.Warnf("create webhook request %s: %v", webhook.URL, err)
		if deliveryID > 0 {
			_ = s.store.FinishWebhookDelivery(deliveryID, "failed", 0, err.Error(), "")
		}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wacli/2")
	if webhook.Secret != "" {
		mac := hmac.New(sha256.New, []byte(webhook.Secret))
		_, _ = mac.Write(body)
		req.Header.Set("X-WACLI-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.log.Warnf("deliver webhook %s: %v", webhook.URL, err)
		if deliveryID > 0 {
			_ = s.store.FinishWebhookDelivery(deliveryID, "failed", 0, err.Error(), "")
		}
		_ = s.store.AddAppLog("error", "webhook", fmt.Sprintf("Webhook %d delivery failed", webhook.ID), map[string]any{
			"webhook_id": webhook.ID,
			"url":        webhook.URL,
			"event":      event,
			"error":      strings.TrimSpace(err.Error()),
		})
		fmt.Printf("webhook delivery webhook_id=%d event=%s status=failed url=%s error=%s\n", webhook.ID, event, webhook.URL, strings.TrimSpace(err.Error()))
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	responseBody := strings.TrimSpace(string(data))
	if resp.StatusCode >= 300 {
		s.log.Warnf("webhook %s returned %d: %s", webhook.URL, resp.StatusCode, responseBody)
		if deliveryID > 0 {
			_ = s.store.FinishWebhookDelivery(deliveryID, "failed", resp.StatusCode, responseBody, responseBody)
		}
		_ = s.store.AddAppLog("error", "webhook", fmt.Sprintf("Webhook %d returned %d", webhook.ID, resp.StatusCode), map[string]any{
			"webhook_id":  webhook.ID,
			"url":         webhook.URL,
			"event":       event,
			"http_status": resp.StatusCode,
			"response":    responseBody,
		})
		fmt.Printf("webhook delivery webhook_id=%d event=%s status=failed http_status=%d url=%s\n", webhook.ID, event, resp.StatusCode, webhook.URL)
		return
	}
	if deliveryID > 0 {
		_ = s.store.FinishWebhookDelivery(deliveryID, "done", resp.StatusCode, "", responseBody)
	}
	_ = s.store.AddAppLog("info", "webhook", fmt.Sprintf("Webhook %d delivered", webhook.ID), map[string]any{
		"webhook_id":  webhook.ID,
		"url":         webhook.URL,
		"event":       event,
		"http_status": resp.StatusCode,
	})
	fmt.Printf("webhook delivery webhook_id=%d event=%s status=done http_status=%d url=%s\n", webhook.ID, event, resp.StatusCode, webhook.URL)
}

// --- shared helpers ---
// Recovered when the AI bridge was removed: both are used by webhook and trigger filtering, and only
// happened to live in that file.

func messageKindsMatch(filters []string, message MessageRecord) bool {
	if len(filters) == 0 {
		return true
	}
	kinds := webhookMessageKinds(message)
	for _, candidate := range filters {
		candidate = normalizeWebhookMessageType(candidate)
		if candidate == "" {
			continue
		}
		if candidate == "*" || candidate == "all" || candidate == "any" {
			return true
		}
		for _, kind := range kinds {
			if candidate == kind {
				return true
			}
		}
	}
	return false
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

// Client exposes the underlying whatsmeow client for callers that need to drive login or reach a
// protocol feature wacli does not wrap.
func (s *Service) Client() *whatsmeow.Client { return s.client }

// Store exposes the local database: chats, messages, contacts, webhooks, triggers and settings.
func (s *Service) Store() *Store { return s.store }
