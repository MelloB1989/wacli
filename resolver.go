package main

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

type ResolveOptions struct {
	Kind        string
	Limit       int
	AllowDirect bool
}

type ResolvedTarget struct {
	Input            string         `json:"input"`
	JID              string         `json:"jid"`
	Name             string         `json:"name"`
	Phone            string         `json:"phone,omitempty"`
	IsGroup          bool           `json:"is_group"`
	Locked           bool           `json:"locked"`
	ExistsInChats    bool           `json:"exists_in_chats"`
	ExistsInContacts bool           `json:"exists_in_contacts"`
	MatchType        string         `json:"match_type"`
	Score            int            `json:"score"`
	Chat             *ChatRecord    `json:"chat,omitempty"`
	Contact          *ContactRecord `json:"contact,omitempty"`
}

type AmbiguousReferenceError struct {
	Reference  string
	Candidates []ResolvedTarget
}

func (e *AmbiguousReferenceError) Error() string {
	parts := make([]string, 0, len(e.Candidates))
	for idx, candidate := range e.Candidates {
		if idx >= 5 {
			break
		}
		label := candidate.Name
		if label == "" {
			label = candidate.JID
		}
		parts = append(parts, fmt.Sprintf("%s <%s>", label, candidate.JID))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("reference %q is ambiguous", e.Reference)
	}
	return fmt.Sprintf("reference %q is ambiguous: %s", e.Reference, strings.Join(parts, ", "))
}

func ResolveTargets(store *Store, ref string, opts ResolveOptions) ([]ResolvedTarget, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("reference is required")
	}
	if opts.Kind == "" {
		opts.Kind = "any"
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}

	chats, err := store.ListChats("all", 0, "")
	if err != nil {
		return nil, err
	}
	contacts, err := store.ListContacts(0, "")
	if err != nil {
		return nil, err
	}

	candidates := map[string]ResolvedTarget{}
	queryLower := strings.ToLower(ref)
	queryPhone := normalizePhone(ref)
	phoneLike := looksLikePhoneReference(ref)
	queryUser := chatIDFromRef(ref)

	addCandidate := func(candidate ResolvedTarget) {
		if !candidateAllowed(candidate, opts.Kind) {
			return
		}
		if candidate.Name == "" {
			switch {
			case candidate.Chat != nil && candidate.Chat.Name != "":
				candidate.Name = candidate.Chat.Name
			case candidate.Contact != nil:
				candidate.Name = displayNameFromContact(*candidate.Contact)
			case candidate.Phone != "":
				candidate.Name = candidate.Phone
			default:
				candidate.Name = candidate.JID
			}
		}
		existing, ok := candidates[candidate.JID]
		if !ok || candidate.Score > existing.Score {
			if existing.Chat != nil && candidate.Chat == nil {
				candidate.Chat = existing.Chat
			}
			if existing.Contact != nil && candidate.Contact == nil {
				candidate.Contact = existing.Contact
			}
			if candidate.Phone == "" {
				candidate.Phone = existing.Phone
			}
			if candidate.Name == "" {
				candidate.Name = existing.Name
			}
			if !candidate.ExistsInChats {
				candidate.ExistsInChats = existing.ExistsInChats
				candidate.Locked = existing.Locked
			}
			if !candidate.ExistsInContacts {
				candidate.ExistsInContacts = existing.ExistsInContacts
			}
			candidates[candidate.JID] = candidate
			return
		}
		if existing.Chat == nil && candidate.Chat != nil {
			existing.Chat = candidate.Chat
			existing.ExistsInChats = true
			existing.Locked = candidate.Locked
		}
		if existing.Contact == nil && candidate.Contact != nil {
			existing.Contact = candidate.Contact
			existing.ExistsInContacts = true
		}
		if existing.Phone == "" && candidate.Phone != "" {
			existing.Phone = candidate.Phone
		}
		if existing.Name == "" && candidate.Name != "" {
			existing.Name = candidate.Name
		}
		candidates[candidate.JID] = existing
	}

	for _, chat := range chats {
		userPart := chatIDFromRef(chat.JID)
		score, match := scoreReferenceAgainstChat(ref, queryLower, queryPhone, phoneLike, queryUser, chat, userPart)
		if score == 0 {
			continue
		}
		candidate := ResolvedTarget{
			Input:            ref,
			JID:              chat.JID,
			Name:             chat.Name,
			Phone:            userPart,
			IsGroup:          chat.IsGroup,
			Locked:           chat.Locked,
			ExistsInChats:    true,
			ExistsInContacts: false,
			MatchType:        match,
			Score:            score,
			Chat:             cloneChat(chat),
		}
		addCandidate(candidate)
	}

	for _, contact := range contacts {
		displayName := displayNameFromContact(contact)
		score, match := scoreReferenceAgainstContact(ref, queryLower, queryPhone, phoneLike, queryUser, contact, displayName)
		if score == 0 {
			continue
		}
		candidate := ResolvedTarget{
			Input:            ref,
			JID:              contact.JID,
			Name:             displayName,
			Phone:            contact.Phone,
			IsGroup:          strings.HasSuffix(contact.JID, "@g.us"),
			Locked:           false,
			ExistsInChats:    false,
			ExistsInContacts: true,
			MatchType:        match,
			Score:            score,
			Contact:          cloneContact(contact),
		}
		addCandidate(candidate)
	}

	if opts.AllowDirect {
		if jid, ok := directJIDCandidate(ref); ok {
			addCandidate(ResolvedTarget{
				Input:            ref,
				JID:              jid.String(),
				Name:             jid.User,
				Phone:            jid.User,
				IsGroup:          jid.Server == types.GroupServer,
				Locked:           false,
				ExistsInChats:    false,
				ExistsInContacts: false,
				MatchType:        "direct_input",
				Score:            990,
			})
		} else if phoneLike && queryPhone != "" {
			jid := types.NewJID(queryPhone, types.DefaultUserServer)
			addCandidate(ResolvedTarget{
				Input:            ref,
				JID:              jid.String(),
				Name:             queryPhone,
				Phone:            queryPhone,
				IsGroup:          false,
				Locked:           false,
				ExistsInChats:    false,
				ExistsInContacts: false,
				MatchType:        "direct_phone_input",
				Score:            985,
			})
		}
	}

	out := make([]ResolvedTarget, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].ExistsInChats != out[j].ExistsInChats {
			return out[i].ExistsInChats
		}
		if out[i].ExistsInContacts != out[j].ExistsInContacts {
			return out[i].ExistsInContacts
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func ResolveBestTarget(store *Store, ref string, opts ResolveOptions) (ResolvedTarget, error) {
	candidates, err := ResolveTargets(store, ref, opts)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if len(candidates) == 0 {
		return ResolvedTarget{}, fmt.Errorf("no matches found for %q", ref)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if candidates[0].Score >= 940 && candidates[0].Score > candidates[1].Score {
		return candidates[0], nil
	}
	return ResolvedTarget{}, &AmbiguousReferenceError{
		Reference:  ref,
		Candidates: candidates,
	}
}

func candidateAllowed(candidate ResolvedTarget, kind string) bool {
	switch strings.ToLower(kind) {
	case "", "any":
		return true
	case "chat":
		return candidate.ExistsInChats || !candidate.IsGroup
	case "contact":
		return !candidate.IsGroup
	default:
		return true
	}
}

func scoreReferenceAgainstChat(raw, lower, phone string, phoneLike bool, user string, chat ChatRecord, chatUser string) (int, string) {
	nameLower := strings.ToLower(chat.Name)
	jidLower := strings.ToLower(chat.JID)
	switch {
	case strings.EqualFold(raw, chat.JID):
		return 1000, "jid_exact"
	case user != "" && strings.EqualFold(user, chatUser):
		return 980, "chat_id_exact"
	case phoneLike && !chat.IsGroup && phone != "" && normalizePhone(chatUser) == phone:
		return 970, "phone_exact"
	case lower != "" && lower == nameLower:
		return 950, "chat_name_exact"
	case lower != "" && strings.Contains(nameLower, lower):
		return 760, "chat_name_contains"
	case user != "" && strings.Contains(strings.ToLower(chatUser), strings.ToLower(user)):
		return 710, "chat_id_contains"
	case lower != "" && strings.Contains(jidLower, lower):
		return 680, "jid_contains"
	default:
		return 0, ""
	}
}

func scoreReferenceAgainstContact(raw, lower, phone string, phoneLike bool, user string, contact ContactRecord, displayName string) (int, string) {
	displayLower := strings.ToLower(displayName)
	jidLower := strings.ToLower(contact.JID)
	phoneNormalized := normalizePhone(contact.Phone)
	switch {
	case strings.EqualFold(raw, contact.JID):
		return 995, "contact_jid_exact"
	case phoneLike && phone != "" && phoneNormalized == phone:
		return 975, "contact_phone_exact"
	case user != "" && strings.EqualFold(user, chatIDFromRef(contact.JID)):
		return 965, "contact_id_exact"
	case lower != "" && lower == displayLower:
		return 940, "contact_name_exact"
	case lower != "" && strings.Contains(displayLower, lower):
		return 740, "contact_name_contains"
	case lower != "" && strings.Contains(jidLower, lower):
		return 670, "contact_jid_contains"
	default:
		return 0, ""
	}
}

func directJIDCandidate(ref string) (types.JID, bool) {
	ref = strings.TrimSpace(ref)
	if !strings.Contains(ref, "@") {
		return types.JID{}, false
	}
	jid, err := types.ParseJID(ref)
	if err != nil {
		return types.JID{}, false
	}
	return jid, true
}

func looksLikePhoneReference(ref string) bool {
	ref = strings.TrimSpace(ref)
	if len(normalizePhone(ref)) < 6 {
		return false
	}
	pattern := regexp.MustCompile(`^[+0-9()\-\s]+$`)
	return pattern.MatchString(ref)
}

func chatIDFromRef(value string) string {
	value = strings.TrimSpace(value)
	if idx := strings.Index(value, "@"); idx >= 0 {
		return value[:idx]
	}
	return value
}

func cloneChat(chat ChatRecord) *ChatRecord {
	copy := chat
	return &copy
}

func cloneContact(contact ContactRecord) *ContactRecord {
	copy := contact
	return &copy
}
