package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MelloB1989/karma/ai"
)

// Group, presence and reachability tools.
//
// Without these an agent could talk in a group but never manage one — no creating, no adding or
// removing anyone, no admin changes, no invite links — which is a large hole in "do anything on
// WhatsApp".

// GroupTools manage groups.
func (t *Tools) GroupTools() []ai.GoFunctionTool {
	return []ai.GoFunctionTool{
		tool("whatsapp_list_groups",
			"List every WhatsApp group this account belongs to, with names and JIDs. Use it to find a "+
				"group before acting on it.",
			object(map[string]any{}),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.ListGroups(ctx))
			}),

		tool("whatsapp_group_info",
			"Get one group's details: name, topic, owner, and the full participant list with who is admin.",
			object(map[string]any{"group": str("Group JID or name.")}, "group"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.GroupInfo(ctx, arg(p, "group")))
			}),

		tool("whatsapp_create_group",
			"Create a new WhatsApp group with a name and initial members. Members can be phone numbers, "+
				"JIDs, or contact names.",
			object(map[string]any{
				"name": str("Group name."),
				"participants": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Members to add: phone numbers, JIDs, or contact names.",
				},
			}, "name", "participants"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				members, err := stringList(p["participants"])
				if err != nil {
					return result(nil, err)
				}
				return result(t.c.CreateGroup(ctx, arg(p, "name"), members))
			}),

		tool("whatsapp_group_participants",
			"Add or remove people from a group, or promote and demote admins. Each person's outcome is "+
				"reported separately, since WhatsApp often accepts some changes and refuses others in one "+
				"request.",
			object(map[string]any{
				"group":  str("Group JID or name."),
				"action": enum("What to do.", "add", "remove", "promote", "demote"),
				"participants": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "People to act on: phone numbers, JIDs, or contact names.",
				},
			}, "group", "action", "participants"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				members, err := stringList(p["participants"])
				if err != nil {
					return result(nil, err)
				}
				return result(t.c.UpdateGroupParticipants(ctx, arg(p, "group"), arg(p, "action"), members))
			}),

		tool("whatsapp_rename_group",
			"Change a group's name, its topic (description), or both.",
			object(map[string]any{
				"group": str("Group JID or name."),
				"name":  str("New group name."),
				"topic": str("New group topic/description."),
			}, "group"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				if name := arg(p, "name"); name != "" {
					return result(t.c.SetGroupName(ctx, arg(p, "group"), name))
				}
				if topic := arg(p, "topic"); topic != "" {
					return result(t.c.SetGroupTopic(ctx, arg(p, "group"), topic))
				}
				return result(nil, fmt.Errorf("provide a name or a topic"))
			}),

		tool("whatsapp_group_invite_link",
			"Get a group's invite link so it can be shared. Set reset to revoke the old link first, "+
				"which stops anyone who already has it from joining.",
			object(map[string]any{
				"group": str("Group JID or name."),
				"reset": boolp("Revoke the existing link and issue a new one."),
			}, "group"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.GroupInviteLink(ctx, arg(p, "group"), argBool(p, "reset")))
			}),

		tool("whatsapp_join_group",
			"Join a group from an invite link, or preview the group behind a link without joining it. "+
				"Prefer previewing first when the link came from someone else.",
			object(map[string]any{
				"link":    str("Invite link or bare invite code."),
				"preview": boolp("Only look at the group; do not join."),
			}, "link"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.JoinGroup(ctx, arg(p, "link"), argBool(p, "preview")))
			}),

		tool("whatsapp_leave_group",
			"Leave a WhatsApp group. This cannot be undone without a new invite.",
			object(map[string]any{"group": str("Group JID or name.")}, "group"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.LeaveGroup(ctx, arg(p, "group")))
			}),
	}
}

// PresenceTools control how this account appears to others.
func (t *Tools) PresenceTools() []ai.GoFunctionTool {
	return []ai.GoFunctionTool{
		tool("whatsapp_set_typing",
			"Show or clear the typing indicator in a chat. Worth setting before a reply that takes a "+
				"moment to prepare, so the other person sees composing rather than silence.",
			object(map[string]any{
				"chat":      str("Chat to show the indicator in."),
				"typing":    boolp("True to show typing, false to clear it."),
				"recording": boolp("Show 'recording audio' instead of 'typing'."),
			}, "chat", "typing"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.SetTyping(ctx, arg(p, "chat"), argBool(p, "typing"), argBool(p, "recording")))
			}),

		tool("whatsapp_set_presence",
			"Announce this account as online or offline. Being available also affects which events "+
				"WhatsApp delivers to this device.",
			object(map[string]any{"available": boolp("True for online, false for offline.")}, "available"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				return result(t.c.SetPresence(ctx, argBool(p, "available")))
			}),

		tool("whatsapp_check_numbers",
			"Check which phone numbers actually have WhatsApp, and get their JIDs. Cheap for a whole "+
				"list at once, and worth doing before a bulk send, since messages to numbers without an "+
				"account fail one by one.",
			object(map[string]any{
				"phones": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Phone numbers in international format.",
				},
			}, "phones"),
			func(ctx context.Context, p ai.FuncParams) (string, error) {
				phones, err := stringList(p["phones"])
				if err != nil {
					return result(nil, err)
				}
				return result(t.c.CheckOnWhatsApp(ctx, phones))
			}),
	}
}

// stringList coerces a tool argument into a []string, tolerating the []any that JSON decoding gives.
func stringList(raw any) ([]string, error) {
	if raw == nil {
		return nil, fmt.Errorf("expected a list of strings")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("expected a list of strings: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the list is empty")
	}
	return out, nil
}
