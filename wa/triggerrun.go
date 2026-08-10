package wa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Running triggers.

// TriggerResult reports what one trigger did, for the API, the CLI and dry runs.
type TriggerResult struct {
	TriggerID   int64          `json:"trigger_id"`
	TriggerName string         `json:"trigger_name"`
	Matched     bool           `json:"matched"`
	Actions     []ActionResult `json:"actions,omitempty"`
	Skipped     string         `json:"skipped,omitempty"`
}

// ActionResult reports one action's outcome.
type ActionResult struct {
	Type   string `json:"type"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// triggerCooldowns tracks the last fire per (trigger, chat) so a rule cannot answer itself into a
// loop. Kept in memory: a cooldown is about the last few seconds, and a restart clearing it is fine.
var triggerCooldowns sync.Map // string -> time.Time

func cooldownKey(triggerID int64, chatJID string) string {
	return fmt.Sprintf("%d|%s", triggerID, chatJID)
}

// RunTriggers evaluates every trigger against an event and runs the ones that match.
//
// Exported so an embedding caller can feed it events wacli did not generate itself. dryRun evaluates
// and reports without sending anything, which is what /triggers/test uses.
func (s *Service) RunTriggers(event string, chat ChatRecord, message MessageRecord, dryRun bool) []TriggerResult {
	triggers, err := s.store.ListTriggers()
	if err != nil {
		s.log.Warnf("list triggers: %v", err)
		return nil
	}

	var results []TriggerResult
	for _, t := range triggers {
		if !t.Matches(event, chat, message) {
			continue
		}
		res := TriggerResult{TriggerID: t.ID, TriggerName: t.Name, Matched: true}

		if t.CooldownSeconds > 0 && !dryRun {
			key := cooldownKey(t.ID, chat.JID)
			if last, ok := triggerCooldowns.Load(key); ok {
				if since := time.Since(last.(time.Time)); since < time.Duration(t.CooldownSeconds)*time.Second {
					res.Skipped = fmt.Sprintf("cooling down, %.0fs left",
						(time.Duration(t.CooldownSeconds)*time.Second - since).Seconds())
					results = append(results, res)
					if t.StopOnMatch {
						break
					}
					continue
				}
			}
			triggerCooldowns.Store(key, time.Now())
		}

		if dryRun {
			for _, a := range t.Actions {
				res.Actions = append(res.Actions, ActionResult{
					Type: a.Type, OK: true,
					Detail: "dry run: " + s.describeAction(a, chat, message),
				})
			}
		} else {
			for _, a := range t.Actions {
				res.Actions = append(res.Actions, s.runAction(a, chat, message))
			}
			if err := s.store.recordTriggerFired(t.ID); err != nil {
				s.log.Warnf("record trigger %d fired: %v", t.ID, err)
			}
			s.dispatchWebhook(EventTriggerFired, map[string]any{
				"trigger": t,
				"chat":    chat,
				"message": message,
				"actions": res.Actions,
			})
		}

		results = append(results, res)
		if t.StopOnMatch {
			break
		}
	}
	return results
}

// describeAction renders what an action would do, for dry runs.
func (s *Service) describeAction(a TriggerAction, chat ChatRecord, message MessageRecord) string {
	switch a.Type {
	case ActionSendText:
		return fmt.Sprintf("send %q to %s", expandPlaceholders(a.Text, chat, message), chat.JID)
	case ActionSendMedia:
		return fmt.Sprintf("send %s to %s", a.MediaPath, chat.JID)
	case ActionReact:
		return fmt.Sprintf("react %s to %s", a.Emoji, message.ID)
	case ActionMarkRead:
		return fmt.Sprintf("mark %s read", chat.JID)
	case ActionWebhook:
		return "POST to " + a.URL
	case ActionForward:
		return fmt.Sprintf("forward to %s", a.To)
	default:
		return "unknown action"
	}
}

// runAction performs a single action, returning what happened rather than failing the whole trigger:
// one bad action should not stop the rest of the rule.
func (s *Service) runAction(a TriggerAction, chat ChatRecord, message MessageRecord) ActionResult {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res := ActionResult{Type: a.Type}

	switch a.Type {
	case ActionSendText, ActionSendMedia:
		text := expandPlaceholders(a.Text, chat, message)
		record, err := s.sendMessageDirect(ctx, mustParseJID(chat.JID), text, a.MediaPath, false, "")
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
		res.Detail = "sent " + record.ID
		return res

	case ActionReact:
		if err := s.ReactToMessage(ctx, chat.JID, message.ID, a.Emoji); err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
		res.Detail = "reacted " + a.Emoji
		return res

	case ActionMarkRead:
		if err := s.MarkChatRead(ctx, chat.JID); err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
		res.Detail = "marked read"
		return res

	case ActionForward:
		text := expandPlaceholders(nonEmptyString(a.Text, message.Content), chat, message)
		target, err := s.ResolveBestTarget(a.To, "chat", true)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		record, err := s.sendMessageDirect(ctx, mustParseJID(target.JID), text, "", false, "")
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
		res.Detail = "forwarded to " + target.JID + " as " + record.ID
		return res

	case ActionWebhook:
		payload := map[string]any{"chat": chat, "message": message, "source": "trigger"}
		body, err := json.Marshal(payload)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL, bytes.NewReader(body))
		if err != nil {
			res.Error = err.Error()
			return res
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.httpClient.Do(req)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		defer func() { _ = resp.Body.Close() }()
		res.OK = resp.StatusCode < 300
		res.Detail = resp.Status
		if !res.OK {
			res.Error = "webhook returned " + resp.Status
		}
		return res

	default:
		res.Error = "unknown action type " + a.Type
		return res
	}
}

// TestTrigger evaluates one trigger against a hypothetical message without sending anything.
func (s *Service) TestTrigger(id int64, chatRef, text string) (TriggerResult, error) {
	t, err := s.store.GetTrigger(id)
	if err != nil {
		return TriggerResult{}, err
	}
	chat := ChatRecord{JID: chatRef, Name: chatRef}
	if resolved, err := s.ResolveBestTarget(chatRef, "chat", true); err == nil {
		if stored, err := s.store.GetChat(resolved.JID); err == nil {
			chat = stored
		} else {
			chat = ChatRecord{JID: resolved.JID, Name: resolved.Name}
		}
	}
	msg := MessageRecord{Content: text, ChatJID: chat.JID}

	res := TriggerResult{TriggerID: t.ID, TriggerName: t.Name}
	if !t.Matches(EventIncomingMessage, chat, msg) {
		if !t.Enabled {
			res.Skipped = "trigger is disabled"
		} else {
			res.Skipped = "no match"
		}
		return res, nil
	}
	res.Matched = true
	for _, a := range t.Actions {
		res.Actions = append(res.Actions, ActionResult{
			Type: a.Type, OK: true, Detail: "would " + s.describeAction(a, chat, msg),
		})
	}
	return res, nil
}

// triggerEventForMessage feeds an inbound or outbound message through the trigger engine.
func (s *Service) triggerEventForMessage(event string, chat ChatRecord, message MessageRecord) {
	if strings.TrimSpace(chat.JID) == "" {
		return
	}
	s.RunTriggers(event, chat, message, false)
}
