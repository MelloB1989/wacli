package wa

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Trigger persistence, and the one-time migration off auto_replies.

const triggerSchema = `
CREATE TABLE IF NOT EXISTS triggers (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	priority INTEGER NOT NULL DEFAULT 100,
	events TEXT NOT NULL DEFAULT '[]',
	match_type TEXT NOT NULL DEFAULT 'always',
	pattern TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL DEFAULT 'all',
	chat_jids TEXT NOT NULL DEFAULT '[]',
	from_me INTEGER NOT NULL DEFAULT -1,
	actions TEXT NOT NULL DEFAULT '[]',
	stop_on_match INTEGER NOT NULL DEFAULT 1,
	cooldown_seconds INTEGER NOT NULL DEFAULT 0,
	fire_count INTEGER NOT NULL DEFAULT 0,
	last_fired_at INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_triggers_priority ON triggers(enabled DESC, priority ASC, id ASC);
`

// ensureTriggerSchema creates the table and, once, carries any old auto-reply rules across.
func (s *Store) ensureTriggerSchema() error {
	if _, err := s.db.Exec(triggerSchema); err != nil {
		return fmt.Errorf("create triggers table: %w", err)
	}
	return s.migrateAutoReplies()
}

// migrateAutoReplies converts legacy auto_replies rows into triggers.
//
// An auto-reply is exactly a trigger on incoming_message with one send_text (or send_media) action
// that stops evaluation, so nothing is lost. It runs once: rows are only copied when the triggers
// table is empty, so re-running cannot duplicate them, and auto_replies is left in place rather than
// dropped in case someone needs to look at what they had.
func (s *Store) migrateAutoReplies() error {
	var legacy int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM auto_replies`).Scan(&legacy); err != nil {
		return nil // no legacy table; nothing to carry over
	}
	if legacy == 0 {
		return nil
	}
	var existing int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM triggers`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	rules, err := s.ListAutoReplies()
	if err != nil {
		return err
	}
	for _, rule := range rules {
		scope := ScopeAll
		switch {
		case rule.ApplyToDMs && !rule.ApplyToGroups:
			scope = ScopeDMs
		case rule.ApplyToGroups && !rule.ApplyToDMs:
			scope = ScopeGroups
		}
		action := TriggerAction{Type: ActionSendText, Text: rule.ReplyText}
		if strings.TrimSpace(rule.MediaPath) != "" {
			action = TriggerAction{Type: ActionSendMedia, MediaPath: rule.MediaPath, Text: rule.ReplyText}
		}
		inbound := false
		t := Trigger{
			Name:        rule.Name,
			Enabled:     rule.Enabled,
			Priority:    rule.Priority,
			Events:      []string{EventIncomingMessage},
			MatchType:   normalizeLegacyMatch(rule.MatchType),
			Pattern:     rule.Pattern,
			Scope:       scope,
			FromMe:      &inbound,
			Actions:     []TriggerAction{action},
			StopOnMatch: true,
		}
		if _, err := s.CreateTrigger(t); err != nil {
			return fmt.Errorf("migrate auto-reply %q: %w", rule.Name, err)
		}
	}
	return nil
}

// normalizeLegacyMatch maps the old auto-reply vocabulary onto trigger match types.
func normalizeLegacyMatch(match string) string {
	switch strings.ToLower(strings.TrimSpace(match)) {
	case "always", "":
		return MatchAlways
	case "exact":
		return MatchExact
	case "prefix", "starts_with":
		return MatchPrefix
	case "suffix", "ends_with":
		return MatchSuffix
	case "regex":
		return MatchRegex
	default:
		return MatchContains
	}
}

func scanTrigger(scan func(dest ...any) error) (Trigger, error) {
	var (
		t                       Trigger
		events, chats, actions  string
		enabled, stopOnMatch    int
		fromMe                  int
		lastFired, created, upd int64
	)
	err := scan(&t.ID, &t.Name, &enabled, &t.Priority, &events, &t.MatchType, &t.Pattern,
		&t.Scope, &chats, &fromMe, &actions, &stopOnMatch, &t.CooldownSeconds,
		&t.FireCount, &lastFired, &created, &upd)
	if err != nil {
		return Trigger{}, err
	}
	t.Enabled = enabled != 0
	t.StopOnMatch = stopOnMatch != 0
	t.Events = decodeStrings(events)
	t.ChatJIDs = decodeStrings(chats)
	t.Actions = decodeActions(actions)
	if fromMe >= 0 {
		v := fromMe == 1
		t.FromMe = &v
	}
	if lastFired > 0 {
		t.LastFiredAt = time.Unix(lastFired, 0)
	}
	t.CreatedAt = time.Unix(created, 0)
	t.UpdatedAt = time.Unix(upd, 0)
	return t, nil
}

const triggerColumns = `id, name, enabled, priority, events, match_type, pattern, scope, chat_jids,
	from_me, actions, stop_on_match, cooldown_seconds, fire_count, last_fired_at, created_at, updated_at`

// ListTriggers returns every trigger in evaluation order: enabled first, then by priority, then ID.
func (s *Store) ListTriggers() ([]Trigger, error) {
	rows, err := s.db.Query(`SELECT ` + triggerColumns + ` FROM triggers
		ORDER BY enabled DESC, priority ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Trigger
	for rows.Next() {
		t, err := scanTrigger(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTrigger returns one trigger by ID.
func (s *Store) GetTrigger(id int64) (Trigger, error) {
	row := s.db.QueryRow(`SELECT `+triggerColumns+` FROM triggers WHERE id = ?`, id)
	t, err := scanTrigger(row.Scan)
	if err == sql.ErrNoRows {
		return Trigger{}, fmt.Errorf("no trigger %d", id)
	}
	return t, err
}

// CreateTrigger validates and stores a new trigger.
func (s *Store) CreateTrigger(t Trigger) (Trigger, error) {
	if err := t.Validate(); err != nil {
		return Trigger{}, err
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO triggers
		(name, enabled, priority, events, match_type, pattern, scope, chat_jids, from_me, actions,
		 stop_on_match, cooldown_seconds, fire_count, last_fired_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,0,0,?,?)`,
		t.Name, boolToInt(t.Enabled), t.Priority, encodeStrings(t.Events), t.MatchType, t.Pattern,
		t.Scope, encodeStrings(t.ChatJIDs), fromMeColumn(t.FromMe), encodeActions(t.Actions),
		boolToInt(t.StopOnMatch), t.CooldownSeconds, now, now)
	if err != nil {
		return Trigger{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Trigger{}, err
	}
	return s.GetTrigger(id)
}

// UpdateTrigger replaces a trigger's definition, keeping its fire statistics.
func (s *Store) UpdateTrigger(t Trigger) (Trigger, error) {
	if err := t.Validate(); err != nil {
		return Trigger{}, err
	}
	res, err := s.db.Exec(`UPDATE triggers SET
		name=?, enabled=?, priority=?, events=?, match_type=?, pattern=?, scope=?, chat_jids=?,
		from_me=?, actions=?, stop_on_match=?, cooldown_seconds=?, updated_at=?
		WHERE id=?`,
		t.Name, boolToInt(t.Enabled), t.Priority, encodeStrings(t.Events), t.MatchType, t.Pattern,
		t.Scope, encodeStrings(t.ChatJIDs), fromMeColumn(t.FromMe), encodeActions(t.Actions),
		boolToInt(t.StopOnMatch), t.CooldownSeconds, time.Now().Unix(), t.ID)
	if err != nil {
		return Trigger{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Trigger{}, fmt.Errorf("no trigger %d", t.ID)
	}
	return s.GetTrigger(t.ID)
}

// SetTriggerEnabled flips a trigger on or off without rewriting it.
func (s *Store) SetTriggerEnabled(id int64, enabled bool) (Trigger, error) {
	if _, err := s.db.Exec(`UPDATE triggers SET enabled=?, updated_at=? WHERE id=?`,
		boolToInt(enabled), time.Now().Unix(), id); err != nil {
		return Trigger{}, err
	}
	return s.GetTrigger(id)
}

// DeleteTrigger removes a trigger.
func (s *Store) DeleteTrigger(id int64) error {
	res, err := s.db.Exec(`DELETE FROM triggers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no trigger %d", id)
	}
	return nil
}

// recordTriggerFired bumps the fire counter and cooldown clock.
func (s *Store) recordTriggerFired(id int64) error {
	_, err := s.db.Exec(`UPDATE triggers SET fire_count = fire_count + 1, last_fired_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	return err
}

// fromMeColumn stores the tri-state direction filter: -1 either, 0 inbound, 1 outbound.
func fromMeColumn(v *bool) int {
	if v == nil {
		return -1
	}
	if *v {
		return 1
	}
	return 0
}
