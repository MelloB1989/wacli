package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/MelloB1989/wacli/wa"
)

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func (e *Env) cmdResolve(args []string) {
	fs := e.newFlagSet("resolve")
	kind := fs.String("kind", "any", "any|chat|contact")
	limit := fs.Int("limit", 10, "maximum matches to return")
	allowDirect := fs.Bool("allow-direct", true, "allow direct JID/phone resolution")
	e.mustParse(fs, args)

	ref := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if ref == "" {
		e.die("usage: wacli resolve [--kind any|chat|contact] [--limit N] [--allow-direct=true|false] <reference>")
	}

	query := url.Values{}
	query.Set("ref", ref)
	query.Set("kind", *kind)
	query.Set("limit", strconv.Itoa(*limit))
	query.Set("allow_direct", strconv.FormatBool(*allowDirect))

	var response map[string]any
	if err := e.callAPIQuery(http.MethodGet, "/resolve", query, nil, &response); err != nil {
		e.die("resolve: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdMessages(args []string) {
	fs := e.newFlagSet("messages")
	chatRef := fs.String("chat", "", "chat name, phone, chat ID, or JID")
	senderRef := fs.String("sender", "", "sender name, phone, or JID")
	queryText := fs.String("query", "", "search within content, filenames, and message type")
	limit := fs.Int("limit", 100, "maximum messages to return")
	mediaOnly := fs.Bool("media-only", false, "only return media messages")
	fromMe := fs.String("from-me", "", "yes|no|true|false")
	before := fs.String("before", "", "latest timestamp, RFC3339")
	after := fs.String("after", "", "earliest timestamp, RFC3339")
	e.mustParse(fs, args)

	query := url.Values{}
	if strings.TrimSpace(*chatRef) != "" {
		query.Set("chat_ref", strings.TrimSpace(*chatRef))
	}
	if strings.TrimSpace(*senderRef) != "" {
		query.Set("sender_ref", strings.TrimSpace(*senderRef))
	}
	if strings.TrimSpace(*queryText) != "" {
		query.Set("query", strings.TrimSpace(*queryText))
	}
	if *limit > 0 {
		query.Set("limit", strconv.Itoa(*limit))
	}
	if *mediaOnly {
		query.Set("media_only", "true")
	}
	if strings.TrimSpace(*fromMe) != "" {
		query.Set("from_me", strings.TrimSpace(*fromMe))
	}
	if strings.TrimSpace(*before) != "" {
		query.Set("before", strings.TrimSpace(*before))
	}
	if strings.TrimSpace(*after) != "" {
		query.Set("after", strings.TrimSpace(*after))
	}

	var response map[string]any
	if err := e.callAPIQuery(http.MethodGet, "/messages", query, nil, &response); err != nil {
		e.die("messages: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdEdit(args []string) {
	fs := e.newFlagSet("edit")
	chat := fs.String("chat", "", "chat name, phone, chat ID, or JID")
	id := fs.String("id", "", "message ID to edit")
	text := fs.String("text", "", "new message text")
	e.mustParse(fs, args)
	if *text == "" && fs.NArg() > 0 {
		*text = strings.Join(fs.Args(), " ")
	}
	if *chat == "" || *id == "" || *text == "" {
		e.die("usage: wacli edit --chat <ref> --id <message-id> --text <new text>")
	}
	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/messages/edit", map[string]any{
		"chat": *chat, "id": *id, "text": *text,
	}, &response); err != nil {
		e.die("edit: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdDelete(args []string) {
	fs := e.newFlagSet("delete")
	chat := fs.String("chat", "", "chat name, phone, chat ID, or JID")
	id := fs.String("id", "", "message ID to delete (revoke for everyone)")
	e.mustParse(fs, args)
	if *chat == "" || *id == "" {
		e.die("usage: wacli delete --chat <ref> --id <message-id>")
	}
	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/messages/delete", map[string]any{
		"chat": *chat, "id": *id,
	}, &response); err != nil {
		e.die("delete: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdReceipts(args []string) {
	fs := e.newFlagSet("receipts")
	id := fs.String("id", "", "message ID to inspect")
	e.mustParse(fs, args)
	if *id == "" && fs.NArg() > 0 {
		*id = strings.TrimSpace(fs.Args()[0])
	}
	if *id == "" {
		e.die("usage: wacli receipts --id <message-id>")
	}
	query := url.Values{}
	query.Set("id", *id)
	var response map[string]any
	if err := e.callAPIQuery(http.MethodGet, "/messages/receipts", query, nil, &response); err != nil {
		e.die("receipts: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdContacts(args []string) {
	if len(args) == 0 {
		e.die("usage: wacli contacts <list|lookup|update>")
	}
	switch args[0] {
	case "list":
		e.cmdContactsList(args[1:])
	case "lookup", "get":
		e.cmdContactsLookup(args[1:])
	case "update":
		e.cmdContactsUpdate(args[1:])
	default:
		e.die("usage: wacli contacts <list|lookup|update>")
	}
}

func (e *Env) cmdContactsList(args []string) {
	fs := e.newFlagSet("contacts list")
	limit := fs.Int("limit", 200, "maximum contacts to return")
	queryText := fs.String("query", "", "search by name, phone, or JID")
	e.mustParse(fs, args)

	query := url.Values{}
	query.Set("limit", strconv.Itoa(*limit))
	if strings.TrimSpace(*queryText) != "" {
		query.Set("query", strings.TrimSpace(*queryText))
	}

	var response map[string]any
	if err := e.callAPIQuery(http.MethodGet, "/contacts", query, nil, &response); err != nil {
		e.die("contacts list: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdContactsLookup(args []string) {
	fs := e.newFlagSet("contacts lookup")
	refFlag := fs.String("ref", "", "contact/chat reference")
	e.mustParse(fs, args)

	ref := strings.TrimSpace(*refFlag)
	if ref == "" {
		ref = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if ref == "" {
		e.die("usage: wacli contacts lookup [--ref <reference>] <reference>")
	}

	query := url.Values{}
	query.Set("ref", ref)
	var response map[string]any
	if err := e.callAPIQuery(http.MethodGet, "/contacts/lookup", query, nil, &response); err != nil {
		e.die("contacts lookup: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdContactsUpdate(args []string) {
	fs := e.newFlagSet("contacts update")
	refFlag := fs.String("ref", "", "contact/chat reference")
	bio := fs.String("bio", "", "bio value")
	notes := fs.String("notes", "", "freeform notes")
	memory := fs.String("memory", "", "AI memory summary")
	metadataJSON := fs.String("metadata-json", "", "arbitrary JSON string")
	e.mustParse(fs, args)

	ref := strings.TrimSpace(*refFlag)
	if ref == "" {
		ref = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if ref == "" {
		e.die("usage: wacli contacts update [--ref <reference>] [--bio ...] [--notes ...] [--memory ...] [--metadata-json ...]")
	}

	body := map[string]any{"ref": ref}
	if flagWasSet(fs, "bio") {
		body["bio"] = *bio
	}
	if flagWasSet(fs, "notes") {
		body["notes"] = *notes
	}
	if flagWasSet(fs, "memory") {
		body["memory"] = *memory
	}
	if flagWasSet(fs, "metadata-json") {
		body["metadata_json"] = *metadataJSON
	}
	if len(body) == 1 {
		e.die("contacts update: at least one of --bio, --notes, --memory, or --metadata-json is required")
	}

	var response map[string]any
	if err := e.callAPI(http.MethodPut, "/contacts/update", body, &response); err != nil {
		e.die("contacts update: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdBulkSend(args []string) {
	fs := e.newFlagSet("bulk-send")
	intervalMS := fs.Int("interval-ms", 0, "delay between sends in milliseconds")
	itemsFile := fs.String("items-file", "", "JSON array/object or newline-delimited items file")
	stdinJSON := fs.Bool("stdin-json", false, "read JSON array/object or newline-delimited items from stdin")
	var itemFlags stringListFlag
	fs.Var(&itemFlags, "item", "bulk item as JSON object or to=...,text=...,media=...")
	e.mustParse(fs, args)

	var items []wa.BulkSendItem
	for _, raw := range itemFlags {
		item, err := parseBulkSendItemSpec(raw)
		if err != nil {
			e.die("bulk-send item: %v", err)
		}
		items = append(items, item)
	}
	if strings.TrimSpace(*itemsFile) != "" {
		data, err := os.ReadFile(strings.TrimSpace(*itemsFile))
		if err != nil {
			e.die("bulk-send read items-file: %v", err)
		}
		parsed, err := parseBulkSendInput(data)
		if err != nil {
			e.die("bulk-send parse items-file: %v", err)
		}
		items = append(items, parsed...)
	}
	if *stdinJSON {
		data, err := e.readAllStdin()
		if err != nil {
			e.die("bulk-send read stdin: %v", err)
		}
		parsed, err := parseBulkSendInput(data)
		if err != nil {
			e.die("bulk-send parse stdin: %v", err)
		}
		items = append(items, parsed...)
	}
	if len(items) == 0 {
		e.die("usage: wacli bulk-send [--item '{\"to\":\"...\",\"text\":\"...\"}'] [--items-file path] [--stdin-json]")
	}

	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/bulk_send", map[string]any{
		"items":       items,
		"interval_ms": *intervalMS,
	}, &response); err != nil {
		e.die("bulk-send: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdMedia(args []string) {
	if len(args) == 0 {
		e.die("usage: wacli media <download>")
	}
	switch args[0] {
	case "download":
		e.cmdMediaDownload(args[1:])
	default:
		e.die("usage: wacli media <download>")
	}
}

func (e *Env) cmdMediaDownload(args []string) {
	fs := e.newFlagSet("media download")
	chatRef := fs.String("chat", "", "chat name, phone, chat ID, or JID")
	messageID := fs.String("message-id", "", "message id")
	e.mustParse(fs, args)

	if strings.TrimSpace(*chatRef) == "" || strings.TrimSpace(*messageID) == "" {
		e.die("usage: wacli media download --chat <reference> --message-id <id>")
	}

	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/media/download", map[string]any{
		"chat_ref":   strings.TrimSpace(*chatRef),
		"message_id": strings.TrimSpace(*messageID),
	}, &response); err != nil {
		e.die("media download: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdWebhooks(args []string) {
	if len(args) == 0 {
		e.die("usage: wacli webhooks <list|add|remove|test|replay|deliveries>")
	}
	switch args[0] {
	case "list":
		var response map[string]any
		if err := e.callAPI(http.MethodGet, "/webhooks", nil, &response); err != nil {
			e.die("webhooks list: %v", err)
		}
		e.printJSON(response)
	case "logs", "deliveries":
		e.cmdWebhookLogs(args[1:])
	case "add":
		e.cmdWebhooksAdd(args[1:])
	case "remove", "rm", "delete":
		e.cmdWebhooksRemove(args[1:])
	case "test":
		e.cmdWebhooksTest(args[1:])
	case "replay":
		e.cmdWebhooksReplay(args[1:])
	default:
		e.die("usage: wacli webhooks <list|add|remove|test|replay|deliveries>")
	}
}

// cmdWebhooksTest fires a synthetic event so an endpoint can be verified before a real one arrives.
func (e *Env) cmdWebhooksTest(args []string) {
	fs := e.newFlagSet("webhooks test")
	id := fs.Int64("id", 0, "webhook ID")
	e.mustParse(fs, args)
	if *id == 0 && fs.NArg() > 0 {
		*id, _ = strconv.ParseInt(fs.Arg(0), 10, 64)
	}
	if *id == 0 {
		e.die("usage: wacli webhooks test <id>")
	}
	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/webhooks/test", map[string]any{"id": *id}, &response); err != nil {
		e.die("webhooks test: %v", err)
	}
	e.printJSON(response)
}

// cmdWebhooksReplay re-sends a delivery that is already on disk.
func (e *Env) cmdWebhooksReplay(args []string) {
	fs := e.newFlagSet("webhooks replay")
	id := fs.Int64("id", 0, "delivery ID, from `wacli webhooks deliveries`")
	e.mustParse(fs, args)
	if *id == 0 && fs.NArg() > 0 {
		*id, _ = strconv.ParseInt(fs.Arg(0), 10, 64)
	}
	if *id == 0 {
		e.die("usage: wacli webhooks replay <delivery-id>")
	}
	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/webhook_deliveries/replay", map[string]any{"id": *id}, &response); err != nil {
		e.die("webhooks replay: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdWebhooksAdd(args []string) {
	fs := e.newFlagSet("webhooks add")
	urlValue := fs.String("url", "", "destination URL")
	secret := fs.String("secret", "", "optional HMAC secret")
	events := fs.String("events", "incoming_message", "comma-separated event names")
	scope := fs.String("scope", "", "all_unlocked|selected_chats")
	messageTypes := fs.String("message-types", "*", "comma-separated message kinds: text,image,video,document,audio,sticker,media,*")
	contextLimit := fs.Int("context-limit", 12, "recent message context window size")
	disabled := fs.Bool("disabled", false, "create disabled webhook")
	includeMentions := fs.Bool("include-mentions", false, "also deliver messages from chats outside the scope when this account is @-mentioned")
	var chats stringListFlag
	fs.Var(&chats, "chat", "chat reference to subscribe; repeat for multiple chats")
	e.mustParse(fs, args)

	if strings.TrimSpace(*urlValue) == "" {
		e.die("usage: wacli webhooks add --url <url> [--chat <ref> ...|--scope all_unlocked] [--events ...] [--message-types ...]")
	}
	webhookScope := strings.TrimSpace(*scope)
	if webhookScope == "" {
		if len(chats) > 0 {
			webhookScope = "selected_chats"
		} else {
			webhookScope = "all_unlocked"
		}
	}

	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/webhooks", map[string]any{
		"url":              strings.TrimSpace(*urlValue),
		"secret":           strings.TrimSpace(*secret),
		"events":           splitCSV(*events),
		"scope":            webhookScope,
		"chat_refs":        []string(chats),
		"message_types":    splitCSV(*messageTypes),
		"context_limit":    *contextLimit,
		"enabled":          !*disabled,
		"include_mentions": *includeMentions,
	}, &response); err != nil {
		e.die("webhooks add: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdWebhooksRemove(args []string) {
	if len(args) == 0 {
		e.die("usage: wacli webhooks remove <id>")
	}
	id := strings.TrimSpace(args[0])
	var response map[string]any
	if err := e.callAPI(http.MethodDelete, "/webhooks/"+url.PathEscape(id), nil, &response); err != nil {
		e.die("webhooks remove: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdWebhookLogs(args []string) {
	fs := e.newFlagSet("webhooks logs")
	limit := fs.Int("limit", 100, "maximum delivery logs to return")
	status := fs.String("status", "", "pending|done|failed")
	event := fs.String("event", "", "incoming_message|outgoing_message|...")
	queryText := fs.String("query", "", "search by URL, chat jid, message id, or error text")
	e.mustParse(fs, args)

	query := url.Values{}
	query.Set("limit", strconv.Itoa(*limit))
	if strings.TrimSpace(*status) != "" {
		query.Set("status", strings.TrimSpace(*status))
	}
	if strings.TrimSpace(*event) != "" {
		query.Set("event", strings.TrimSpace(*event))
	}
	if strings.TrimSpace(*queryText) != "" {
		query.Set("query", strings.TrimSpace(*queryText))
	}
	var response map[string]any
	if err := e.callAPIQuery(http.MethodGet, "/webhook_deliveries", query, nil, &response); err != nil {
		e.die("webhooks logs: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdAutoReplies(args []string) {
	if len(args) == 0 {
		e.die("usage: wacli auto-replies <list|add|remove>")
	}
	switch args[0] {
	case "list":
		var response map[string]any
		if err := e.callAPI(http.MethodGet, "/auto_replies", nil, &response); err != nil {
			e.die("auto-replies list: %v", err)
		}
		e.printJSON(response)
	case "add":
		e.cmdAutoRepliesAdd(args[1:])
	case "remove", "rm", "delete":
		e.cmdAutoRepliesRemove(args[1:])
	default:
		e.die("usage: wacli auto-replies <list|add|remove>")
	}
}

func (e *Env) cmdAutoRepliesAdd(args []string) {
	fs := e.newFlagSet("auto-replies add")
	name := fs.String("name", "", "rule name")
	matchType := fs.String("match-type", "contains", "always|exact|contains|prefix|suffix|regex")
	pattern := fs.String("pattern", "", "match pattern")
	replyText := fs.String("reply-text", "", "reply text")
	mediaPath := fs.String("media", "", "optional media file")
	disabled := fs.Bool("disabled", false, "create disabled rule")
	dms := fs.Bool("dms", true, "apply to DMs")
	groups := fs.Bool("groups", false, "apply to groups")
	priority := fs.Int("priority", 100, "lower values run earlier")
	e.mustParse(fs, args)

	if strings.TrimSpace(*name) == "" {
		e.die("usage: wacli auto-replies add --name <name> [--match-type ...] [--pattern ...] [--reply-text ...]")
	}

	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/auto_replies", map[string]any{
		"name":            strings.TrimSpace(*name),
		"match_type":      strings.TrimSpace(*matchType),
		"pattern":         *pattern,
		"reply_text":      *replyText,
		"media_path":      *mediaPath,
		"enabled":         !*disabled,
		"apply_to_dms":    *dms,
		"apply_to_groups": *groups,
		"priority":        *priority,
	}, &response); err != nil {
		e.die("auto-replies add: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdAutoRepliesRemove(args []string) {
	if len(args) == 0 {
		e.die("usage: wacli auto-replies remove <id>")
	}
	id := strings.TrimSpace(args[0])
	var response map[string]any
	if err := e.callAPI(http.MethodDelete, "/auto_replies/"+url.PathEscape(id), nil, &response); err != nil {
		e.die("auto-replies remove: %v", err)
	}
	e.printJSON(response)
}

func (e *Env) cmdAPI(args []string) {
	fs := e.newFlagSet("api")
	bodyFile := fs.String("body-file", "", "read JSON body from file")
	stdinJSON := fs.Bool("stdin-json", false, "read JSON body from stdin")
	e.mustParse(fs, args)

	if fs.NArg() < 2 {
		e.die("usage: wacli api [--body-file path|--stdin-json] <METHOD> </path> [json-body]")
	}
	method := strings.ToUpper(strings.TrimSpace(fs.Arg(0)))
	path := strings.TrimSpace(fs.Arg(1))
	if path == "" {
		e.die("api path is required")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	body, err := e.loadOptionalJSONBody(fs.Args()[2:], *bodyFile, *stdinJSON)
	if err != nil {
		e.die("api body: %v", err)
	}

	var response any
	if err := e.callAPI(method, path, body, &response); err != nil {
		e.die("api %s %s: %v", method, path, err)
	}
	e.printJSON(response)
}

func parseBulkSendInput(data []byte) ([]wa.BulkSendItem, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("no bulk send items provided")
	}

	if strings.HasPrefix(trimmed, "[") {
		var items []wa.BulkSendItem
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var wrapped struct {
			Items []wa.BulkSendItem `json:"items"`
		}
		if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil && len(wrapped.Items) > 0 {
			return wrapped.Items, nil
		}
		var item wa.BulkSendItem
		if err := json.Unmarshal([]byte(trimmed), &item); err != nil {
			return nil, err
		}
		return []wa.BulkSendItem{item}, nil
	}

	lines := strings.Split(trimmed, "\n")
	items := make([]wa.BulkSendItem, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		item, err := parseBulkSendItemSpec(line)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func parseBulkSendItemSpec(input string) (wa.BulkSendItem, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return wa.BulkSendItem{}, fmt.Errorf("empty bulk item")
	}
	if strings.HasPrefix(trimmed, "{") {
		var item wa.BulkSendItem
		if err := json.Unmarshal([]byte(trimmed), &item); err != nil {
			return wa.BulkSendItem{}, err
		}
		if strings.TrimSpace(item.To) == "" {
			return wa.BulkSendItem{}, fmt.Errorf("bulk item missing to")
		}
		return item, nil
	}

	item := wa.BulkSendItem{}
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return wa.BulkSendItem{}, fmt.Errorf("invalid bulk item fragment %q", part)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "to", "chat", "ref":
			item.To = value
		case "text", "message":
			item.Text = value
		case "media", "media_path":
			item.MediaPath = value
		default:
			return wa.BulkSendItem{}, fmt.Errorf("unsupported bulk item field %q", key)
		}
	}
	if strings.TrimSpace(item.To) == "" {
		return wa.BulkSendItem{}, fmt.Errorf("bulk item missing to")
	}
	return item, nil
}

func splitCSV(input string) []string {
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	seen := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

// readAllStdin drains the host's stdin. Reading /dev/stdin worked when the only host was a shell;
// Env.In makes it work anywhere, and yields empty rather than failing where there is no stdin.
func (e *Env) readAllStdin() ([]byte, error) {
	return io.ReadAll(e.stdin())
}

func (e *Env) loadOptionalJSONBody(args []string, bodyFile string, stdinJSON bool) (any, error) {
	if strings.TrimSpace(bodyFile) != "" {
		data, err := os.ReadFile(strings.TrimSpace(bodyFile))
		if err != nil {
			return nil, err
		}
		return decodeArbitraryJSON(data)
	}
	if stdinJSON {
		data, err := e.readAllStdin()
		if err != nil {
			return nil, err
		}
		return decodeArbitraryJSON(data)
	}
	if len(args) == 0 {
		return nil, nil
	}
	return decodeArbitraryJSON([]byte(strings.Join(args, " ")))
}

func decodeArbitraryJSON(data []byte) (any, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	var body any
	if err := json.Unmarshal([]byte(trimmed), &body); err != nil {
		return nil, err
	}
	return body, nil
}

// cmdGroups manages WhatsApp groups.
func (e *Env) cmdGroups(args []string) {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		var response map[string]any
		if err := e.callAPI(http.MethodGet, "/groups", nil, &response); err != nil {
			e.die("groups: %v", err)
		}
		e.printJSON(response)
	case "info":
		fs := e.newFlagSet("groups info")
		group := fs.String("group", "", "group JID or name")
		e.mustParse(fs, args)
		if *group == "" && fs.NArg() > 0 {
			*group = fs.Arg(0)
		}
		if *group == "" {
			e.die("usage: wacli groups info <group>")
		}
		var response map[string]any
		if err := e.callAPI(http.MethodGet, "/groups?ref="+url.QueryEscape(*group), nil, &response); err != nil {
			e.die("groups info: %v", err)
		}
		e.printJSON(response)
	case "create":
		fs := e.newFlagSet("groups create")
		name := fs.String("name", "", "group name")
		members := fs.String("members", "", "comma-separated phones, JIDs or contact names")
		e.mustParse(fs, args)
		if *name == "" || *members == "" {
			e.die("usage: wacli groups create --name <name> --members <a,b,c>")
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/groups",
			map[string]any{"name": *name, "participants": splitCommaList(*members)}, &response); err != nil {
			e.die("groups create: %v", err)
		}
		e.printJSON(response)
	case "add", "remove", "promote", "demote":
		fs := e.newFlagSet("groups " + sub)
		group := fs.String("group", "", "group JID or name")
		members := fs.String("members", "", "comma-separated participants")
		e.mustParse(fs, args)
		if *group == "" || *members == "" {
			e.die("usage: wacli groups %s --group <group> --members <a,b>", sub)
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/groups/participants", map[string]any{
			"group": *group, "action": sub, "participants": splitCommaList(*members),
		}, &response); err != nil {
			e.die("groups %s: %v", sub, err)
		}
		e.printJSON(response)
	case "rename":
		fs := e.newFlagSet("groups rename")
		group := fs.String("group", "", "group JID or name")
		name := fs.String("name", "", "new name")
		topic := fs.String("topic", "", "new topic/description")
		e.mustParse(fs, args)
		if *group == "" || (*name == "" && *topic == "") {
			e.die("usage: wacli groups rename --group <group> [--name <name>] [--topic <topic>]")
		}
		body := map[string]any{"group": *group}
		if *name != "" {
			body["name"] = *name
		} else {
			body["topic"] = *topic
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/groups/update", body, &response); err != nil {
			e.die("groups rename: %v", err)
		}
		e.printJSON(response)
	case "invite":
		fs := e.newFlagSet("groups invite")
		group := fs.String("group", "", "group JID or name")
		reset := fs.Bool("reset", false, "revoke the old link and issue a new one")
		e.mustParse(fs, args)
		if *group == "" && fs.NArg() > 0 {
			*group = fs.Arg(0)
		}
		if *group == "" {
			e.die("usage: wacli groups invite <group> [--reset]")
		}
		q := "/groups/invite?group=" + url.QueryEscape(*group)
		if *reset {
			q += "&reset=true"
		}
		var response map[string]any
		if err := e.callAPI(http.MethodGet, q, nil, &response); err != nil {
			e.die("groups invite: %v", err)
		}
		e.printJSON(response)
	case "join":
		fs := e.newFlagSet("groups join")
		link := fs.String("link", "", "invite link or code")
		preview := fs.Bool("preview", false, "look without joining")
		e.mustParse(fs, args)
		if *link == "" && fs.NArg() > 0 {
			*link = fs.Arg(0)
		}
		if *link == "" {
			e.die("usage: wacli groups join <link> [--preview]")
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/groups/invite",
			map[string]any{"link": *link, "preview": *preview}, &response); err != nil {
			e.die("groups join: %v", err)
		}
		e.printJSON(response)
	case "leave":
		fs := e.newFlagSet("groups leave")
		group := fs.String("group", "", "group JID or name")
		e.mustParse(fs, args)
		if *group == "" && fs.NArg() > 0 {
			*group = fs.Arg(0)
		}
		if *group == "" {
			e.die("usage: wacli groups leave <group>")
		}
		var response map[string]any
		if err := e.callAPI(http.MethodPost, "/groups/update",
			map[string]any{"group": *group, "leave": true}, &response); err != nil {
			e.die("groups leave: %v", err)
		}
		e.printJSON(response)
	default:
		e.die("unknown groups subcommand %q (want: list, info, create, add, remove, promote, demote, rename, invite, join, leave)", sub)
	}
}

// cmdCheckNumbers reports which phone numbers have WhatsApp.
func (e *Env) cmdCheckNumbers(args []string) {
	fs := e.newFlagSet("check")
	phones := fs.String("phones", "", "comma-separated phone numbers")
	e.mustParse(fs, args)
	list := splitCommaList(*phones)
	if len(list) == 0 {
		list = fs.Args()
	}
	if len(list) == 0 {
		e.die("usage: wacli check +15551234567,+15559876543")
	}
	var response map[string]any
	if err := e.callAPI(http.MethodPost, "/contacts/check", map[string]any{"phones": list}, &response); err != nil {
		e.die("check: %v", err)
	}
	e.printJSON(response)
}
