package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MelloB1989/wacli/wa"
)

// absPath resolves a user-supplied file path against the host's working directory.
//
// Audio and recording paths are handed to the service as text, and it resolves what it receives
// against its own working directory — which is wherever it was started, not where the command was
// typed. Making them absolute here keeps "--record peer.wav" meaning the directory the user is
// standing in.
func absPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// cmdSync runs a deep history sync.
//
// On a host with a local store this opens its own service and drives the sync directly, which is
// what `wacli sync` has always done and is more thorough than the API route. Where there is no
// store to open — the mobile bindings, where a service is already running and holds the database —
// it asks the running service instead via POST /sync.
func (e *Env) cmdSync() {
	if !e.hasStore() {
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/sync", map[string]any{}, &response); err != nil {
			e.die("sync: %v", err)
		}
		e.printJSON(response)
		return
	}

	store := e.store()
	defer store.Close()
	service, err := wa.NewService(store)
	if err != nil {
		e.die("start service: %v", err)
	}
	defer service.Close()

	if err := service.Connect(); err != nil {
		e.die("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	marker := service.HistoryMarker()
	if err := service.RequestHistorySync(ctx, 100); err != nil {
		if errors.Is(err, wa.ErrHistoryAnchorUnavailable) {
			e.println("No local history anchor is available yet, so on-demand sync cannot be requested. Bootstrap sync only happens after login from the primary device.")
		} else {
			e.die("request history sync: %v", err)
		}
	}
	seen := service.WaitForHistoryQuiet(marker, 40*time.Second, 4*time.Second)
	if err := service.SyncContacts(ctx); err != nil {
		e.printf("contact sync failed: %v\n", err)
	}
	if err := service.RefreshMissingChatNames(ctx, 100); err != nil {
		e.printf("chat name refresh failed: %v\n", err)
	}
	status, err := store.BuildStatus(service.IsConnected(), service.CurrentUserJID())
	if err != nil {
		e.die("build status: %v", err)
	}
	e.printf("Manual sync complete. Chats: %d, messages: %d, history seen: %v\n", status.ChatCount, status.MessageCount, seen)
}

func (e *Env) cmdStatus() {
	var status wa.StatusSnapshot
	apiErr := e.callAPI(http.MethodGet, "/status", nil, &status)
	if apiErr == nil {
		e.printf("connected: %v\n", status.Connected)
		e.printf("user: %s\n", status.UserJID)
		e.printf("dnd: %v\n", status.DNDMode)
		e.printf("initial access configured: %v\n", status.InitialAccessConfigured)
		e.printf("chats: %d\n", status.ChatCount)
		e.printf("messages: %d\n", status.MessageCount)
		if status.LastHistorySync != nil {
			e.printf("last history sync: %s\n", status.LastHistorySync.Format(time.RFC3339))
		}
		return
	}

	// Without a local store there is nothing to fall back to, so report why the API call failed
	// rather than the generic "no database here".
	if !e.hasStore() {
		e.die("status: %v", apiErr)
	}
	store := e.store()
	defer store.Close()
	status, err := store.BuildStatus(false, "")
	if err != nil {
		e.die("status: %v", err)
	}
	e.println("daemon: not reachable")
	e.printf("dnd: %v\n", status.DNDMode)
	e.printf("initial access configured: %v\n", status.InitialAccessConfigured)
	e.printf("chats: %d\n", status.ChatCount)
	e.printf("messages: %d\n", status.MessageCount)
}

func (e *Env) cmdDND(args []string) {
	if !e.hasStore() {
		e.dndOverAPI(args)
		return
	}

	store := e.store()
	defer store.Close()

	if len(args) == 0 {
		enabled, err := store.GetDNDMode()
		if err != nil {
			e.die("dnd status: %v", err)
		}
		if enabled {
			e.println("DND mode: ON")
		} else {
			e.println("DND mode: OFF")
		}
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "on":
		if err := store.SetDNDMode(true); err != nil {
			e.die("set dnd: %v", err)
		}
		e.println("DND mode enabled")
	case "off":
		if err := store.SetDNDMode(false); err != nil {
			e.die("set dnd: %v", err)
		}
		e.println("DND mode disabled")
	default:
		e.die("usage: wacli dnd [on|off]")
	}
}

// dndOverAPI is the DND path for hosts with no local store, reading and writing the switch through
// the running service instead of the database.
func (e *Env) dndOverAPI(args []string) {
	var response struct {
		Enabled bool `json:"enabled"`
	}
	if len(args) == 0 {
		if err := e.callAPI(http.MethodGet, "/dnd", nil, &response); err != nil {
			e.die("dnd status: %v", err)
		}
		if response.Enabled {
			e.println("DND mode: ON")
		} else {
			e.println("DND mode: OFF")
		}
		return
	}
	var enabled bool
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "on":
		enabled = true
	case "off":
		enabled = false
	default:
		e.die("usage: wacli dnd [on|off]")
	}
	if err := e.callAPI(http.MethodPut, "/dnd", map[string]any{"enabled": enabled}, &response); err != nil {
		e.die("set dnd: %v", err)
	}
	if response.Enabled {
		e.println("DND mode enabled")
	} else {
		e.println("DND mode disabled")
	}
}

// listChats reads the chat list from the local database where the host has one, and from the
// running service otherwise. Both return the same records, so the rendering below is shared.
func (e *Env) listChats(filter string, limit int, query string) []wa.ChatRecord {
	if !e.hasStore() {
		params := url.Values{}
		if filter != "" {
			params.Set("filter", filter)
		}
		if limit > 0 {
			params.Set("limit", strconv.Itoa(limit))
		}
		if query != "" {
			params.Set("query", query)
		}
		var response struct {
			Chats []wa.ChatRecord `json:"chats"`
		}
		if err := e.callAPIQuery(http.MethodGet, "/chats", params, nil, &response); err != nil {
			e.die("list chats: %v", err)
		}
		return response.Chats
	}

	store := e.store()
	defer store.Close()
	chats, err := store.ListChats(filter, limit, query)
	if err != nil {
		e.die("list chats: %v", err)
	}
	return chats
}

func (e *Env) cmdChats(args []string) {
	fs := e.newFlagSet("chats")
	filter := fs.String("filter", "all", "all|locked|unlocked|groups|dms")
	limit := fs.Int("limit", 200, "maximum number of chats to list")
	query := fs.String("query", "", "search by name or jid")
	asJSON := fs.Bool("json", false, "output chats as a JSON array (for tooling)")
	e.mustParse(fs, args)

	chats := e.listChats(*filter, *limit, *query)
	if *asJSON {
		out, err := json.MarshalIndent(chats, "", "  ")
		if err != nil {
			e.die("marshal chats: %v", err)
		}
		e.println(string(out))
		return
	}
	if len(chats) == 0 {
		e.println("No chats found.")
		return
	}
	for _, chat := range chats {
		state := "UNLOCKED"
		if chat.Locked {
			state = "LOCKED"
		}
		kind := "DM"
		if chat.IsGroup {
			kind = "GROUP"
		}
		e.printf("[%s] %s (%s)\n", state, chat.Name, kind)
		e.printf("  JID: %s\n", chat.JID)
		e.printf("  Last Activity: %s\n", chat.LastMessageAt.Format("2006-01-02 15:04:05"))
		if chat.LastMessagePreview != "" {
			e.printf("  Preview: %s\n", chat.LastMessagePreview)
		}
	}
}

func (e *Env) cmdSend(args []string) {
	fs := e.newFlagSet("send")
	to := fs.String("to", "", "recipient JID or phone number")
	text := fs.String("text", "", "message text")
	mediaPath := fs.String("media", "", "optional local media path")
	replyTo := fs.String("reply-to", "", "message ID in the same chat to reply to (quotes it)")
	e.mustParse(fs, args)
	if *to == "" {
		e.die("usage: wacli send --to <jid|phone> [--text <text>] [--media <path>] [--reply-to <message-id>]")
	}
	if *text == "" && fs.NArg() > 0 {
		*text = strings.Join(fs.Args(), " ")
	}

	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/send", map[string]any{
		"to":         *to,
		"text":       *text,
		"media_path": *mediaPath,
		"reply_to":   *replyTo,
	}, &response); err != nil {
		e.die("send: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdCall(args []string) {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		fs := e.newFlagSet("call list")
		activeOnly := fs.Bool("active", false, "only show calls that are still ringing or connected")
		e.mustParse(fs, args)
		path := "/calls"
		if *activeOnly {
			path += "?active=true"
		}
		var response map[string]any
		if err := e.callAPI(http.MethodGet, path, nil, &response); err != nil {
			e.die("call list: %v", err)
		}
		e.printJSON(response)
	case "end", "hangup":
		fs := e.newFlagSet("call end")
		id := fs.String("id", "", "call ID to hang up")
		reason := fs.String("reason", "", "termination reason (default: hangup)")
		e.mustParse(fs, args)
		if *id == "" {
			e.die("usage: wacli call end --id <call-id> [--reason <reason>]")
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/calls/end", map[string]any{
			"call_id": *id,
			"reason":  *reason,
		}, &response); err != nil {
			e.die("call end: %v", err)
		}
		e.printJSON(response)
	case "status":
		fs := e.newFlagSet("call status")
		e.mustParse(fs, args)
		ref := ""
		if fs.NArg() > 0 {
			ref = fs.Arg(0)
		}
		var response map[string]any
		if err := e.callAPI(http.MethodGet,
			"/calls/status?ref="+url.QueryEscape(ref), nil, &response); err != nil {
			e.die("call status: %v", err)
		}
		e.printJSON(response)
	case "queue":
		fs := e.newFlagSet("call queue")
		e.mustParse(fs, args)
		var response map[string]any
		if err := e.callAPI(http.MethodGet, "/calls/queue", nil, &response); err != nil {
			e.die("call queue: %v", err)
		}
		e.printJSON(response)
	case "capture":
		fs := e.newFlagSet("call capture")
		off := fs.Bool("off", false, "stop capturing")
		e.mustParse(fs, args)
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/calls/capture", map[string]any{
			"enabled": !*off,
		}, &response); err != nil {
			e.die("call capture: %v", err)
		}
		e.printJSON(response)
	case "dump":
		fs := e.newFlagSet("call dump")
		last := fs.Int("last", 0, "only show the last N stanzas")
		e.mustParse(fs, args)
		records, err := wa.LoadCaptures(wa.CapturePath)
		if err != nil {
			e.die("call dump: %v", err)
		}
		if len(records) == 0 {
			e.println("no captured call stanzas yet — run 'wacli call capture' first, then make a call")
			return
		}
		if *last > 0 && *last < len(records) {
			records = records[len(records)-*last:]
		}
		for _, rec := range records {
			fmt.Print(wa.DescribeCapture(rec))
		}
		e.printf("\n%d stanza(s) from %s\n", len(records), wa.CapturePath)
	case "answer":
		fs := e.newFlagSet("call answer")
		id := fs.String("id", "", "call ID to answer (default: the only ringing call)")
		say := fs.String("say", "", "speak this text into the call")
		voice := fs.String("voice", "", "voice for --say (see: say -v '?')")
		audio := fs.String("audio", "", ".wav/.mp3/.opus to play instead of --say")
		repeat := fs.Bool("repeat", false, "loop the audio instead of hanging up when it ends")
		record := fs.String("record", "", "write the other party's voice to this .wav")
		e.mustParse(fs, args)
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/calls/answer", map[string]any{
			"call_id": *id,
			"say":     *say,
			"voice":   *voice,
			"audio":   absPath(*audio),
			"repeat":  *repeat,
			"record":  absPath(*record),
		}, &response); err != nil {
			e.die("call answer: %v", err)
		}
		e.printJSON(response)
	case "reject", "decline":
		fs := e.newFlagSet("call reject")
		id := fs.String("id", "", "call ID to decline")
		e.mustParse(fs, args)
		if *id == "" {
			e.die("usage: wacli call reject --id <call-id>")
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/calls/reject", map[string]any{
			"call_id": *id,
		}, &response); err != nil {
			e.die("call reject: %v", err)
		}
		e.printJSON(response)
	case "", "place", "dial":
		fs := e.newFlagSet("call")
		to := fs.String("to", "", "recipient JID or phone number")
		video := fs.Bool("video", false, "place a video call instead of a voice call")
		ringFor := fs.Int("ring-for", 0, "seconds to ring before hanging up automatically (default 45)")
		noExpire := fs.Bool("no-expire", false, "keep ringing until explicitly ended")
		say := fs.String("say", "", "speak this text once they answer")
		voice := fs.String("voice", "", "voice for --say (see: say -v '?')")
		audio := fs.String("audio", "", ".wav/.mp3/.opus to play instead of --say")
		repeat := fs.Bool("repeat", false, "loop the audio instead of hanging up when it ends")
		record := fs.String("record", "", "write the other party's voice to this .wav")
		e.mustParse(fs, args)
		if *to == "" && fs.NArg() > 0 {
			*to = fs.Arg(0)
		}
		if *to == "" {
			e.die("usage: wacli call --to <jid|phone> [--say <text> | --audio <file>] [--video]")
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/calls", map[string]any{
			"to":               *to,
			"video":            *video,
			"ring_for_seconds": *ringFor,
			"no_expire":        *noExpire,
			"say":              *say,
			"voice":            *voice,
			"audio":            absPath(*audio),
			"repeat":           *repeat,
			"record":           absPath(*record),
		}, &response); err != nil {
			e.die("call: %v", err)
		}
		e.printJSON(response)
		if *say == "" && *audio == "" {
			e.println("note: no --say/--audio, so this call rings but carries no audio.")
		}
	default:
		e.die("unknown call subcommand %q (want: place, answer, status, queue, list, end, reject, capture, dump)", sub)
	}
}

func (e *Env) cmdStory(args []string) {
	fs := e.newFlagSet("story")
	text := fs.String("text", "", "story text")
	mediaPath := fs.String("media", "", "optional image/video path")
	e.mustParse(fs, args)
	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/stories", map[string]any{
		"text":       *text,
		"media_path": *mediaPath,
	}, &response); err != nil {
		e.die("story: %v", err)
	}
	e.printJSON(response)
}

// cmdTriggers manages the rule engine: match an event, run actions. See wa/triggers.go.
func (e *Env) cmdTriggers(args []string) {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "list":
		var response map[string]any
		if err := e.callAPI(http.MethodGet, "/triggers", nil, &response); err != nil {
			e.die("triggers: %v", err)
		}
		e.printJSON(response)
	case "add":
		fs := e.newFlagSet("triggers add")
		name := fs.String("name", "", "rule name")
		match := fs.String("match", "contains", "always|contains|exact|prefix|suffix|regex")
		pattern := fs.String("pattern", "", "text to match")
		scope := fs.String("scope", "all", "all|dms|groups|list")
		chats := fs.String("chats", "", "comma-separated chat JIDs when --scope=list")
		events := fs.String("events", "", "comma-separated event kinds (default incoming_message)")
		reply := fs.String("reply", "", "send this text when it matches")
		media := fs.String("media", "", "send this file when it matches")
		react := fs.String("react", "", "react with this emoji")
		forward := fs.String("forward", "", "forward the message to this chat")
		hook := fs.String("webhook", "", "POST the event to this URL")
		markRead := fs.Bool("mark-read", false, "mark the chat read")
		priority := fs.Int("priority", 100, "evaluation order, lowest first")
		cooldown := fs.Int("cooldown", 0, "seconds to wait before this rule may fire again per chat")
		keepGoing := fs.Bool("continue", false, "let lower-priority rules run too")
		e.mustParse(fs, args)
		if *name == "" {
			e.die("usage: wacli triggers add --name <name> [--match ...] [--pattern ...] --reply <text>")
		}
		actions := []map[string]any{}
		if *reply != "" {
			actions = append(actions, map[string]any{"type": "send_text", "text": *reply})
		}
		if *media != "" {
			actions = append(actions, map[string]any{"type": "send_media", "media_path": absPath(*media), "text": *reply})
		}
		if *react != "" {
			actions = append(actions, map[string]any{"type": "react", "emoji": *react})
		}
		if *forward != "" {
			actions = append(actions, map[string]any{"type": "forward", "to": *forward})
		}
		if *hook != "" {
			actions = append(actions, map[string]any{"type": "webhook", "url": *hook})
		}
		if *markRead {
			actions = append(actions, map[string]any{"type": "mark_read"})
		}
		if len(actions) == 0 {
			e.die("a trigger needs at least one action (--reply, --media, --react, --forward, --webhook, --mark-read)")
		}
		body := map[string]any{
			"name": *name, "enabled": true, "priority": *priority,
			"match_type": *match, "pattern": *pattern, "scope": *scope,
			"actions": actions, "stop_on_match": !*keepGoing, "cooldown_seconds": *cooldown,
		}
		if *chats != "" {
			body["chat_jids"] = splitCommaList(*chats)
		}
		if *events != "" {
			body["events"] = splitCommaList(*events)
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/triggers", body, &response); err != nil {
			e.die("triggers add: %v", err)
		}
		e.printJSON(response)
	case "enable", "disable":
		fs := e.newFlagSet("triggers " + sub)
		id := fs.Int64("id", 0, "trigger ID")
		e.mustParse(fs, args)
		if *id == 0 && fs.NArg() > 0 {
			*id, _ = strconv.ParseInt(fs.Arg(0), 10, 64)
		}
		if *id == 0 {
			e.die("usage: wacli triggers %s <id>", sub)
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPatch, fmt.Sprintf("/triggers/%d", *id),
			map[string]any{"enabled": sub == "enable"}, &response); err != nil {
			e.die("triggers %s: %v", sub, err)
		}
		e.printJSON(response)
	case "remove", "delete":
		fs := e.newFlagSet("triggers remove")
		id := fs.Int64("id", 0, "trigger ID")
		e.mustParse(fs, args)
		if *id == 0 && fs.NArg() > 0 {
			*id, _ = strconv.ParseInt(fs.Arg(0), 10, 64)
		}
		if *id == 0 {
			e.die("usage: wacli triggers remove <id>")
		}
		var response map[string]any
		if err := e.callAPI(http.MethodDelete, fmt.Sprintf("/triggers/%d", *id), nil, &response); err != nil {
			e.die("triggers remove: %v", err)
		}
		e.printJSON(response)
	case "test":
		fs := e.newFlagSet("triggers test")
		id := fs.Int64("id", 0, "trigger ID")
		chat := fs.String("chat", "", "chat to test against")
		text := fs.String("text", "", "message text to test")
		e.mustParse(fs, args)
		if *id == 0 {
			e.die("usage: wacli triggers test --id <id> --chat <ref> --text <message>")
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/triggers/test",
			map[string]any{"id": *id, "chat": *chat, "text": *text}, &response); err != nil {
			e.die("triggers test: %v", err)
		}
		e.printJSON(response)
	default:
		e.die("unknown triggers subcommand %q (want: list, add, enable, disable, remove, test)", sub)
	}
}

// splitCommaList turns "a,b, c" into []string{"a","b","c"}.
func splitCommaList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
