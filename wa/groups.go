package wa

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Group management, presence, and reachability checks.
//
// wacli could read group chats but not act on them at all — no creating a group, no adding or
// removing anyone, no admin changes, no invite links. Anything driving wacli could talk in a group
// but never manage one, which is a large hole in "do anything on WhatsApp".

// GroupSummary is a group as wacli reports it. It is whatsmeow's GroupInfo flattened to the fields a
// caller acts on, so the JSON stays stable if the protocol struct grows.
type GroupSummary struct {
	JID          string             `json:"jid"`
	Name         string             `json:"name"`
	Topic        string             `json:"topic,omitempty"`
	Owner        string             `json:"owner,omitempty"`
	Created      string             `json:"created,omitempty"`
	IsAnnounce   bool               `json:"is_announce"`
	IsLocked     bool               `json:"is_locked"`
	Participants []GroupParticipant `json:"participants,omitempty"`
	// ParticipantCount survives the trim that listing does, so a caller can see
	// how big a group is without being handed everyone in it.
	ParticipantCount int `json:"participant_count,omitempty"`
}

// withoutParticipants is the listing form of a group.
//
// Listing every group with every member is 3.8 MB and a million tokens on this
// account — past the context limit of any model that might read it, for a call
// whose job is only to find a group by name. The membership is one
// GroupInfo/whatsapp_group_info call away once the caller knows which group it
// wants.
func (g GroupSummary) withoutParticipants() GroupSummary {
	g.ParticipantCount = len(g.Participants)
	g.Participants = nil
	// Topics are freeform and frequently long — a rules-of-the-group essay in a
	// listing is the same problem in smaller print.
	g.Topic = truncateTopic(g.Topic)
	return g
}

// maxListedTopic bounds a topic in a listing; the full text comes with the group.
const maxListedTopic = 140

func truncateTopic(topic string) string {
	runes := []rune(topic)
	if len(runes) <= maxListedTopic {
		return topic
	}
	return string(runes[:maxListedTopic]) + "…"
}

// GroupParticipant is one member and what they may do.
type GroupParticipant struct {
	JID          string `json:"jid"`
	IsAdmin      bool   `json:"is_admin"`
	IsSuperAdmin bool   `json:"is_super_admin"`
}

// summarizeGroup flattens whatsmeow's GroupInfo.
func summarizeGroup(info *types.GroupInfo) GroupSummary {
	if info == nil {
		return GroupSummary{}
	}
	out := GroupSummary{
		JID:        info.JID.String(),
		Name:       info.Name,
		Topic:      info.Topic,
		IsAnnounce: info.IsAnnounce,
		IsLocked:   info.IsLocked,
	}
	if !info.OwnerJID.IsEmpty() {
		out.Owner = info.OwnerJID.String()
	}
	if !info.GroupCreated.IsZero() {
		out.Created = info.GroupCreated.UTC().Format("2006-01-02T15:04:05Z")
	}
	for _, p := range info.Participants {
		out.Participants = append(out.Participants, GroupParticipant{
			JID: p.JID.String(), IsAdmin: p.IsAdmin, IsSuperAdmin: p.IsSuperAdmin,
		})
	}
	return out
}

// requireConnected is the precondition every group operation shares.
func (s *Service) requireConnected() error {
	if !s.client.IsConnected() {
		return errors.New("WhatsApp client is not connected")
	}
	return nil
}

// resolveParticipants turns loose references — phone numbers, names, JIDs — into JIDs.
//
// Each is resolved individually and reported by name on failure: adding the wrong person to a group
// is not something to paper over with a best guess.
func (s *Service) resolveParticipants(refs []string) ([]types.JID, error) {
	out := make([]types.JID, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		target, err := s.ResolveBestTarget(ref, "contact", true)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", ref, err)
		}
		jid, err := types.ParseJID(target.JID)
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", target.JID, err)
		}
		out = append(out, jid)
	}
	if len(out) == 0 {
		return nil, errors.New("no participants given")
	}
	return out, nil
}

// resolveGroupJID turns a group reference into a JID, refusing anything that is not a group.
func (s *Service) resolveGroupJID(ref string) (types.JID, error) {
	target, err := s.ResolveBestTarget(ref, "group", true)
	if err != nil {
		return types.JID{}, err
	}
	jid, err := types.ParseJID(target.JID)
	if err != nil {
		return types.JID{}, err
	}
	if jid.Server != types.GroupServer {
		return types.JID{}, fmt.Errorf("%s is not a group", target.JID)
	}
	return jid, nil
}

// ListGroups returns every group this account belongs to.
func (s *Service) ListGroups(ctx context.Context) ([]GroupSummary, error) {
	if err := s.requireConnected(); err != nil {
		return nil, err
	}
	groups, err := s.client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	out := make([]GroupSummary, 0, len(groups))
	for _, g := range groups {
		out = append(out, summarizeGroup(g).withoutParticipants())
	}
	return out, nil
}

// GroupInfo returns one group with its participants.
func (s *Service) GroupInfo(ctx context.Context, ref string) (GroupSummary, error) {
	if err := s.requireConnected(); err != nil {
		return GroupSummary{}, err
	}
	jid, err := s.resolveGroupJID(ref)
	if err != nil {
		return GroupSummary{}, err
	}
	info, err := s.client.GetGroupInfo(ctx, jid)
	if err != nil {
		return GroupSummary{}, fmt.Errorf("group info: %w", err)
	}
	return summarizeGroup(info), nil
}

// CreateGroup creates a group with the given members.
func (s *Service) CreateGroup(ctx context.Context, name string, participantRefs []string) (GroupSummary, error) {
	if err := s.requireConnected(); err != nil {
		return GroupSummary{}, err
	}
	if strings.TrimSpace(name) == "" {
		return GroupSummary{}, errors.New("group name is required")
	}
	participants, err := s.resolveParticipants(participantRefs)
	if err != nil {
		return GroupSummary{}, err
	}
	if err := s.ensureAutomationAllowed(participants[0]); err != nil {
		return GroupSummary{}, err
	}
	info, err := s.client.CreateGroup(ctx, whatsmeow.ReqCreateGroup{
		Name:         strings.TrimSpace(name),
		Participants: participants,
	})
	if err != nil {
		return GroupSummary{}, fmt.Errorf("create group: %w", err)
	}
	s.log.Infof("created group %s with %d participant(s)", info.JID, len(participants))
	return summarizeGroup(info), nil
}

// UpdateGroupParticipants adds, removes, promotes or demotes members.
//
// action is add, remove, promote or demote. The per-participant outcome is returned rather than a
// single error, because WhatsApp routinely accepts some changes and refuses others in one request —
// a number that is not on WhatsApp, or someone who left.
func (s *Service) UpdateGroupParticipants(ctx context.Context, ref, action string, participantRefs []string) ([]GroupParticipant, error) {
	if err := s.requireConnected(); err != nil {
		return nil, err
	}
	jid, err := s.resolveGroupJID(ref)
	if err != nil {
		return nil, err
	}
	participants, err := s.resolveParticipants(participantRefs)
	if err != nil {
		return nil, err
	}

	var change whatsmeow.ParticipantChange
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "add":
		change = whatsmeow.ParticipantChangeAdd
	case "remove":
		change = whatsmeow.ParticipantChangeRemove
	case "promote":
		change = whatsmeow.ParticipantChangePromote
	case "demote":
		change = whatsmeow.ParticipantChangeDemote
	default:
		return nil, fmt.Errorf("unknown action %q (want add, remove, promote or demote)", action)
	}

	result, err := s.client.UpdateGroupParticipants(ctx, jid, participants, change)
	if err != nil {
		return nil, fmt.Errorf("%s participants: %w", action, err)
	}
	out := make([]GroupParticipant, 0, len(result))
	for _, p := range result {
		out = append(out, GroupParticipant{
			JID: p.JID.String(), IsAdmin: p.IsAdmin, IsSuperAdmin: p.IsSuperAdmin,
		})
	}
	s.log.Infof("group %s: %s %d participant(s)", jid, action, len(participants))
	return out, nil
}

// SetGroupName renames a group.
func (s *Service) SetGroupName(ctx context.Context, ref, name string) error {
	if err := s.requireConnected(); err != nil {
		return err
	}
	jid, err := s.resolveGroupJID(ref)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("group name is required")
	}
	return s.client.SetGroupName(ctx, jid, strings.TrimSpace(name))
}

// SetGroupTopic sets a group's description.
func (s *Service) SetGroupTopic(ctx context.Context, ref, topic string) error {
	if err := s.requireConnected(); err != nil {
		return err
	}
	jid, err := s.resolveGroupJID(ref)
	if err != nil {
		return err
	}
	return s.client.SetGroupTopic(ctx, jid, "", "", topic)
}

// GroupInviteLink returns the group's invite link, optionally revoking the old one first.
func (s *Service) GroupInviteLink(ctx context.Context, ref string, reset bool) (string, error) {
	if err := s.requireConnected(); err != nil {
		return "", err
	}
	jid, err := s.resolveGroupJID(ref)
	if err != nil {
		return "", err
	}
	link, err := s.client.GetGroupInviteLink(ctx, jid, reset)
	if err != nil {
		return "", fmt.Errorf("invite link: %w", err)
	}
	return link, nil
}

// JoinGroupWithLink joins a group from an invite link or bare code.
func (s *Service) JoinGroupWithLink(ctx context.Context, link string) (GroupSummary, error) {
	if err := s.requireConnected(); err != nil {
		return GroupSummary{}, err
	}
	code := inviteCode(link)
	if code == "" {
		return GroupSummary{}, errors.New("invite link or code is required")
	}
	jid, err := s.client.JoinGroupWithLink(ctx, code)
	if err != nil {
		return GroupSummary{}, fmt.Errorf("join group: %w", err)
	}
	info, err := s.client.GetGroupInfo(ctx, jid)
	if err != nil {
		return GroupSummary{JID: jid.String()}, nil
	}
	return summarizeGroup(info), nil
}

// GroupInfoFromLink previews a group behind an invite link without joining it.
func (s *Service) GroupInfoFromLink(ctx context.Context, link string) (GroupSummary, error) {
	if err := s.requireConnected(); err != nil {
		return GroupSummary{}, err
	}
	code := inviteCode(link)
	if code == "" {
		return GroupSummary{}, errors.New("invite link or code is required")
	}
	info, err := s.client.GetGroupInfoFromLink(ctx, code)
	if err != nil {
		return GroupSummary{}, fmt.Errorf("preview group: %w", err)
	}
	return summarizeGroup(info), nil
}

// LeaveGroup leaves a group.
func (s *Service) LeaveGroup(ctx context.Context, ref string) error {
	if err := s.requireConnected(); err != nil {
		return err
	}
	jid, err := s.resolveGroupJID(ref)
	if err != nil {
		return err
	}
	return s.client.LeaveGroup(ctx, jid)
}

// inviteCode extracts the code from a chat.whatsapp.com link, or passes a bare code through.
func inviteCode(link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	if idx := strings.LastIndex(link, "/"); idx >= 0 {
		link = link[idx+1:]
	}
	if idx := strings.Index(link, "?"); idx >= 0 {
		link = link[:idx]
	}
	return strings.TrimSpace(link)
}

// --- presence ---

// SetPresence announces this account as available or unavailable.
//
// Presence is also a prerequisite rather than a nicety: WhatsApp only delivers some events to a
// device that has announced itself.
func (s *Service) SetPresence(ctx context.Context, available bool) error {
	if err := s.requireConnected(); err != nil {
		return err
	}
	state := types.PresenceUnavailable
	if available {
		state = types.PresenceAvailable
	}
	return s.client.SendPresence(ctx, state)
}

// SetTyping shows or clears the typing indicator in a chat, so an automated reply that takes a
// moment looks like someone composing rather than dead air.
func (s *Service) SetTyping(ctx context.Context, chatRef string, typing bool, recording bool) error {
	if err := s.requireConnected(); err != nil {
		return err
	}
	target, err := s.ResolveBestTarget(chatRef, "chat", true)
	if err != nil {
		return err
	}
	jid, err := types.ParseJID(target.JID)
	if err != nil {
		return err
	}
	state := types.ChatPresencePaused
	if typing {
		state = types.ChatPresenceComposing
	}
	media := types.ChatPresenceMediaText
	if recording {
		media = types.ChatPresenceMediaAudio
	}
	return s.client.SendChatPresence(ctx, jid, state, media)
}

// --- reachability ---

// OnWhatsAppResult reports whether a number has WhatsApp.
type OnWhatsAppResult struct {
	Query        string `json:"query"`
	JID          string `json:"jid,omitempty"`
	IsRegistered bool   `json:"is_registered"`
	VerifiedName string `json:"verified_name,omitempty"`
}

// CheckOnWhatsApp reports which of the given phone numbers are reachable on WhatsApp.
//
// Worth calling before a bulk send: sending to a number with no account fails per-message, and the
// answer is cheap to get for the whole list at once.
func (s *Service) CheckOnWhatsApp(ctx context.Context, phones []string) ([]OnWhatsAppResult, error) {
	if err := s.requireConnected(); err != nil {
		return nil, err
	}
	cleaned := make([]string, 0, len(phones))
	for _, p := range phones {
		if p = strings.TrimSpace(p); p != "" {
			if !strings.HasPrefix(p, "+") {
				p = "+" + NormalizePhone(p)
			}
			cleaned = append(cleaned, p)
		}
	}
	if len(cleaned) == 0 {
		return nil, errors.New("no phone numbers given")
	}
	responses, err := s.client.IsOnWhatsApp(ctx, cleaned)
	if err != nil {
		return nil, fmt.Errorf("check numbers: %w", err)
	}
	out := make([]OnWhatsAppResult, 0, len(responses))
	for _, r := range responses {
		res := OnWhatsAppResult{Query: r.Query, IsRegistered: r.IsIn}
		if r.VerifiedName != nil && r.VerifiedName.Details != nil {
			res.VerifiedName = r.VerifiedName.Details.GetVerifiedName()
		}
		if !r.JID.IsEmpty() {
			res.JID = r.JID.String()
		}
		out = append(out, res)
	}
	return out, nil
}
