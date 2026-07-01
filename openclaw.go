package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const legacyDefaultOpenClawInstruction = `You are handling an inbound WhatsApp conversation on behalf of the owner.
Use the local wacli CLI for any follow-up actions.
- To reply with text: wacli send --to "<chat reference or jid>" --text "<message>"
- To reply with media: wacli send --to "<chat reference or jid>" --text "<caption>" --media /absolute/path/file
- To inspect more context: wacli messages --chat "<chat reference or jid>" --limit 50
- To inspect or update memory: wacli contacts lookup "<chat reference or jid>" and wacli contacts update --ref "<chat reference or jid>" ...
The JSON below is the exact inbound event envelope from wacli.`

type OpenClawExecutionResult struct {
	BridgeID       int64  `json:"bridge_id"`
	ChatJID        string `json:"chat_jid"`
	MessageID      string `json:"message_id"`
	SessionID      string `json:"session_id"`
	Command        string `json:"command"`
	RequestMessage string `json:"request_message"`
	ResponseOutput string `json:"response_output,omitempty"`
	Status         string `json:"status"`
	LastError      string `json:"last_error,omitempty"`
}

type OpenClawReplayResult struct {
	Bridge            OpenClawBridgeRecord    `json:"bridge"`
	Session           OpenClawSessionRecord   `json:"session"`
	Chat              ChatRecord              `json:"chat"`
	SourceMessage     MessageRecord           `json:"source_message"`
	DeliveryMessageID string                  `json:"delivery_message_id"`
	Execution         OpenClawExecutionResult `json:"execution"`
}

func defaultOpenClawInstruction(settings AssistantSettings) string {
	wacliCommand := shellCommandLiteral(currentExecutablePath())
	base := fmt.Sprintf(`You are handling an inbound WhatsApp conversation on behalf of the owner.
Your primary job is to decide whether to send a WhatsApp reply right now.
Use the local wacli CLI for all WhatsApp follow-up actions.
- If a reply is appropriate, send exactly one WhatsApp reply with: %s send --to "<chat jid from payload.chat.jid>" --text "<message>"
- If media is needed, use: %s send --to "<chat jid from payload.chat.jid>" --text "<caption>" --media /absolute/path/file
- To inspect more context, use: %s messages --chat "<payload.chat.jid>" --limit 50
- To inspect or update memory, use: %s contacts lookup "<payload.chat.jid>" and %s contacts update --ref "<payload.chat.jid>" ...
- Prefer a short, useful reply. Do not browse the web or call external fetch/search tools unless the latest inbound message explicitly requires fresh outside information.
- Do not use OpenClaw's own WhatsApp channel for replies. Use wacli only.
- After you send a reply with wacli, stop.
The JSON below is the exact inbound event envelope from wacli.`, wacliCommand, wacliCommand, wacliCommand, wacliCommand, wacliCommand)
	var extras []string
	if trimmed := strings.TrimSpace(settings.AssistantName); trimmed != "" {
		extras = append(extras, "Assistant name: "+trimmed)
	}
	if trimmed := strings.TrimSpace(settings.Personality); trimmed != "" {
		extras = append(extras, "Personality: "+trimmed)
	}
	if trimmed := strings.TrimSpace(settings.Behavior); trimmed != "" {
		extras = append(extras, "Behavior: "+trimmed)
	}
	if trimmed := strings.TrimSpace(settings.ReplyStyle); trimmed != "" {
		extras = append(extras, "Reply style: "+trimmed)
	}
	if trimmed := strings.TrimSpace(settings.ReplyInstruction); trimmed != "" {
		extras = append(extras, "Reply instruction: "+trimmed)
	}
	if len(extras) == 0 {
		return base
	}
	return base + "\n\nAssistant configuration:\n- " + strings.Join(extras, "\n- ")
}

func (s *Service) dispatchOpenClawInbound(chat ChatRecord, message MessageRecord) {
	bridges, err := s.store.ListOpenClawBridges()
	if err != nil {
		s.log.Warnf("list openclaw bridges: %v", err)
		return
	}
	if len(bridges) == 0 {
		return
	}

	basePayload := s.buildMessageWebhookPayload(chat, message, "whatsapp_event")
	generatedAt := time.Now().UTC().Format(time.RFC3339)
	for _, bridge := range bridges {
		if !bridge.Enabled || !openClawBridgeMatches(bridge, chat, message) {
			continue
		}
		session, err := s.store.GetOrCreateOpenClawSession(chat.JID)
		if err != nil {
			s.log.Warnf("openclaw session %s: %v", chat.JID, err)
			continue
		}
		payload := s.openClawPayloadForDelivery(bridge, basePayload)
		envelope := map[string]any{
			"event":        "incoming_message",
			"generated_at": generatedAt,
			"openclaw_bridge": map[string]any{
				"id":            bridge.ID,
				"scope":         bridge.Scope,
				"chat_jids":     bridge.ChatJIDs,
				"message_types": bridge.MessageTypes,
				"context_limit": bridge.ContextLimit,
				"session_id":    session.SessionID,
				"command":       bridge.Command,
			},
			"payload": payload,
		}
		go s.runOpenClawBridge(bridge, session, chat.JID, message.ID, envelope)
	}
}

func (s *Service) ReplayOpenClawInbound(bridgeID int64, chatRef, messageID string) (OpenClawReplayResult, error) {
	dndMode, err := s.store.GetDNDMode()
	if err != nil {
		return OpenClawReplayResult{}, err
	}
	if !dndMode {
		return OpenClawReplayResult{}, fmt.Errorf("DND mode is off; openclaw bridge is inactive")
	}

	bridge, err := s.store.GetOpenClawBridge(bridgeID)
	if err != nil {
		return OpenClawReplayResult{}, err
	}
	if !bridge.Enabled {
		return OpenClawReplayResult{}, fmt.Errorf("openclaw bridge %d is disabled", bridgeID)
	}

	chatTarget, err := s.ResolveBestTarget(chatRef, "chat", false)
	if err != nil {
		return OpenClawReplayResult{}, err
	}
	chat, err := s.store.GetChat(chatTarget.JID)
	if err != nil {
		return OpenClawReplayResult{}, err
	}

	var sourceMessage MessageRecord
	if strings.TrimSpace(messageID) == "" {
		inboundOnly := false
		messages, err := s.store.SearchMessages(MessageSearchOptions{
			ChatJID: chat.JID,
			Limit:   1,
			FromMe:  &inboundOnly,
		})
		if err != nil {
			return OpenClawReplayResult{}, err
		}
		if len(messages) == 0 {
			return OpenClawReplayResult{}, fmt.Errorf("no inbound messages found in %s", chat.JID)
		}
		sourceMessage = messages[len(messages)-1]
	} else {
		sourceMessage, err = s.store.GetMessage(strings.TrimSpace(messageID), chat.JID)
		if err != nil {
			return OpenClawReplayResult{}, err
		}
	}
	if sourceMessage.IsFromMe {
		return OpenClawReplayResult{}, fmt.Errorf("message %s in %s is outbound; replay requires an inbound message", sourceMessage.ID, chat.JID)
	}
	if !openClawBridgeMatches(bridge, chat, sourceMessage) {
		return OpenClawReplayResult{}, fmt.Errorf("bridge %d does not match chat/message filters for %s/%s", bridge.ID, chat.JID, sourceMessage.ID)
	}

	session, err := s.store.GetOrCreateOpenClawSession(chat.JID)
	if err != nil {
		return OpenClawReplayResult{}, err
	}
	basePayload := s.buildMessageWebhookPayload(chat, sourceMessage, "manual_replay")
	generatedAt := time.Now().UTC().Format(time.RFC3339)
	payload := s.openClawPayloadForDelivery(bridge, basePayload)
	envelope := map[string]any{
		"event":        "incoming_message",
		"generated_at": generatedAt,
		"openclaw_bridge": map[string]any{
			"id":            bridge.ID,
			"scope":         bridge.Scope,
			"chat_jids":     bridge.ChatJIDs,
			"message_types": bridge.MessageTypes,
			"context_limit": bridge.ContextLimit,
			"session_id":    session.SessionID,
			"command":       bridge.Command,
		},
		"payload": payload,
	}
	deliveryMessageID := replayDeliveryMessageID(sourceMessage.ID)
	execution := s.runOpenClawBridge(bridge, session, chat.JID, deliveryMessageID, envelope)
	return OpenClawReplayResult{
		Bridge:            bridge,
		Session:           session,
		Chat:              chat,
		SourceMessage:     sourceMessage,
		DeliveryMessageID: deliveryMessageID,
		Execution:         execution,
	}, nil
}

func openClawBridgeMatches(bridge OpenClawBridgeRecord, chat ChatRecord, message MessageRecord) bool {
	if chat.Locked {
		return false
	}
	if strings.EqualFold(chat.JID, "status@broadcast") {
		return false
	}
	if bridge.Scope == "selected_chats" && !stringInList(chat.JID, bridge.ChatJIDs) {
		return false
	}
	return messageKindsMatch(bridge.MessageTypes, message)
}

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

func (s *Service) openClawPayloadForDelivery(bridge OpenClawBridgeRecord, payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	if chat, ok := webhookPayloadChat(payload); ok && bridge.ContextLimit > 0 {
		recent, _ := s.store.ListMessages(chat.JID, bridge.ContextLimit)
		out["recent_messages"] = recent
	}
	return out
}

func (s *Service) runOpenClawBridge(bridge OpenClawBridgeRecord, session OpenClawSessionRecord, chatJID, messageID string, envelope map[string]any) OpenClawExecutionResult {
	result := OpenClawExecutionResult{
		BridgeID:  bridge.ID,
		ChatJID:   chatJID,
		MessageID: messageID,
		SessionID: session.SessionID,
		Status:    "pending",
	}
	settings, _ := s.store.GetAssistantSettings()
	messageText, err := buildOpenClawAgentMessage(bridge, settings, envelope)
	if err != nil {
		s.log.Warnf("build openclaw message %d/%s/%s: %v", bridge.ID, chatJID, messageID, err)
		_ = s.store.FinishOpenClawDelivery(bridge.ID, chatJID, messageID, "failed", err.Error(), "")
		result.Status = "failed"
		result.LastError = err.Error()
		return result
	}
	result.RequestMessage = messageText
	started, err := s.store.TryStartOpenClawDelivery(bridge.ID, chatJID, messageID, session.SessionID, bridge.Command, messageText)
	if err != nil {
		s.log.Warnf("openclaw delivery begin %d/%s/%s: %v", bridge.ID, chatJID, messageID, err)
		result.Status = "failed"
		result.LastError = err.Error()
		return result
	}
	if !started {
		result.Status = "duplicate"
		result.LastError = "duplicate delivery ignored"
		return result
	}
	commandPath, err := resolveOpenClawCommand(bridge.Command)
	if err != nil {
		result.Command = strings.TrimSpace(bridge.Command)
		result.Status = "failed"
		result.LastError = err.Error()
		_ = s.store.FinishOpenClawDelivery(bridge.ID, chatJID, messageID, "failed", truncateString(result.LastError, 1000), "")
		_ = s.store.AddAppLog("error", "assistant", fmt.Sprintf("OpenClaw bridge %d failed before launch", bridge.ID), map[string]any{
			"bridge_id":  bridge.ID,
			"chat_jid":   chatJID,
			"message_id": messageID,
			"error":      truncateString(result.LastError, 1000),
		})
		s.log.Warnf("openclaw bridge %d for %s/%s failed: %s", bridge.ID, chatJID, messageID, result.LastError)
		fmt.Printf("openclaw delivery bridge_id=%d session_id=%s chat_jid=%s message_id=%s status=failed error=%s\n", bridge.ID, session.SessionID, chatJID, messageID, truncateString(result.LastError, 500))
		return result
	}
	result.Command = commandPath

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, commandPath, "agent", "--session-id", session.SessionID, "--message", messageText, "--json")
	output, err := cmd.CombinedOutput()
	outputText := strings.TrimSpace(string(output))
	result.ResponseOutput = outputText
	if err != nil {
		lastError := outputText
		if lastError == "" {
			lastError = err.Error()
		} else {
			lastError = fmt.Sprintf("%v: %s", err, lastError)
		}
		s.log.Warnf("openclaw bridge %d for %s/%s failed: %s", bridge.ID, chatJID, messageID, lastError)
		_ = s.store.FinishOpenClawDelivery(bridge.ID, chatJID, messageID, "failed", truncateString(lastError, 1000), outputText)
		_ = s.store.AddAppLog("error", "assistant", fmt.Sprintf("OpenClaw bridge %d failed", bridge.ID), map[string]any{
			"bridge_id":  bridge.ID,
			"chat_jid":   chatJID,
			"message_id": messageID,
			"error":      truncateString(lastError, 1000),
		})
		fmt.Printf("openclaw delivery bridge_id=%d session_id=%s chat_jid=%s message_id=%s status=failed error=%s\n", bridge.ID, session.SessionID, chatJID, messageID, truncateString(lastError, 500))
		result.Status = "failed"
		result.LastError = truncateString(lastError, 1000)
		return result
	}
	_ = s.store.FinishOpenClawDelivery(bridge.ID, chatJID, messageID, "done", "", outputText)
	_ = s.store.AddAppLog("info", "assistant", fmt.Sprintf("OpenClaw bridge %d completed", bridge.ID), map[string]any{
		"bridge_id":  bridge.ID,
		"chat_jid":   chatJID,
		"message_id": messageID,
		"status":     "done",
	})
	fmt.Printf("openclaw delivery bridge_id=%d session_id=%s chat_jid=%s message_id=%s status=done\n", bridge.ID, session.SessionID, chatJID, messageID)
	result.Status = "done"
	return result
}

func buildOpenClawAgentMessage(bridge OpenClawBridgeRecord, settings AssistantSettings, envelope map[string]any) (string, error) {
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return "", err
	}
	instruction := strings.TrimSpace(bridge.Instruction)
	if instruction == "" || instruction == legacyDefaultOpenClawInstruction {
		instruction = defaultOpenClawInstruction(settings)
	}
	return strings.TrimSpace(instruction) + "\n\nInbound WhatsApp event JSON:\n" + string(data), nil
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func replayDeliveryMessageID(messageID string) string {
	base := strings.TrimSpace(messageID)
	if base == "" {
		base = "unknown"
	}
	return fmt.Sprintf("%s#replay-%d", base, time.Now().UnixNano())
}

func currentExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "wacli"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && strings.TrimSpace(resolved) != "" {
		exe = resolved
	}
	if abs, err := filepath.Abs(exe); err == nil && strings.TrimSpace(abs) != "" {
		exe = abs
	}
	return exe
}

func shellCommandLiteral(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "wacli"
	}
	if strings.ContainsAny(value, " \t\"'") {
		return strconv.Quote(value)
	}
	return value
}

func resolveOpenClawCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		command = "openclaw"
	}
	if strings.ContainsRune(command, os.PathSeparator) {
		if _, err := os.Stat(command); err != nil {
			return "", fmt.Errorf("openclaw command %q: %w", command, err)
		}
		return command, nil
	}
	if resolved, err := exec.LookPath(command); err == nil {
		return resolved, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".npm-global", "bin", command),
		filepath.Join(home, ".local", "bin", command),
		filepath.Join(home, "bin", command),
		filepath.Join("/usr", "local", "bin", command),
		filepath.Join("/usr", "bin", command),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("openclaw command %q was not found in PATH or common install locations", command)
}
