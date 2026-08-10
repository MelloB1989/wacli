package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func cmdResolve(args []string) {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	kind := fs.String("kind", "any", "any|chat|contact")
	limit := fs.Int("limit", 10, "maximum matches to return")
	allowDirect := fs.Bool("allow-direct", true, "allow direct JID/phone resolution")
	_ = fs.Parse(args)

	ref := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if ref == "" {
		die("usage: wacli resolve [--kind any|chat|contact] [--limit N] [--allow-direct=true|false] <reference>")
	}

	query := url.Values{}
	query.Set("ref", ref)
	query.Set("kind", *kind)
	query.Set("limit", strconv.Itoa(*limit))
	query.Set("allow_direct", strconv.FormatBool(*allowDirect))

	var response map[string]any
	if err := callLocalAPIQuery(http.MethodGet, "/resolve", query, nil, &response); err != nil {
		die("resolve: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdMessages(args []string) {
	fs := flag.NewFlagSet("messages", flag.ExitOnError)
	chatRef := fs.String("chat", "", "chat name, phone, chat ID, or JID")
	senderRef := fs.String("sender", "", "sender name, phone, or JID")
	queryText := fs.String("query", "", "search within content, filenames, and message type")
	limit := fs.Int("limit", 100, "maximum messages to return")
	mediaOnly := fs.Bool("media-only", false, "only return media messages")
	fromMe := fs.String("from-me", "", "yes|no|true|false")
	before := fs.String("before", "", "latest timestamp, RFC3339")
	after := fs.String("after", "", "earliest timestamp, RFC3339")
	_ = fs.Parse(args)

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
	if err := callLocalAPIQuery(http.MethodGet, "/messages", query, nil, &response); err != nil {
		die("messages: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdEdit(args []string) {
	fs := flag.NewFlagSet("edit", flag.ExitOnError)
	chat := fs.String("chat", "", "chat name, phone, chat ID, or JID")
	id := fs.String("id", "", "message ID to edit")
	text := fs.String("text", "", "new message text")
	_ = fs.Parse(args)
	if *text == "" && fs.NArg() > 0 {
		*text = strings.Join(fs.Args(), " ")
	}
	if *chat == "" || *id == "" || *text == "" {
		die("usage: wacli edit --chat <ref> --id <message-id> --text <new text>")
	}
	var response map[string]any
	if err := callLocalAPI(http.MethodPost, "/messages/edit", map[string]any{
		"chat": *chat, "id": *id, "text": *text,
	}, &response); err != nil {
		die("edit: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	chat := fs.String("chat", "", "chat name, phone, chat ID, or JID")
	id := fs.String("id", "", "message ID to delete (revoke for everyone)")
	_ = fs.Parse(args)
	if *chat == "" || *id == "" {
		die("usage: wacli delete --chat <ref> --id <message-id>")
	}
	var response map[string]any
	if err := callLocalAPI(http.MethodPost, "/messages/delete", map[string]any{
		"chat": *chat, "id": *id,
	}, &response); err != nil {
		die("delete: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdReceipts(args []string) {
	fs := flag.NewFlagSet("receipts", flag.ExitOnError)
	id := fs.String("id", "", "message ID to inspect")
	_ = fs.Parse(args)
	if *id == "" && fs.NArg() > 0 {
		*id = strings.TrimSpace(fs.Args()[0])
	}
	if *id == "" {
		die("usage: wacli receipts --id <message-id>")
	}
	query := url.Values{}
	query.Set("id", *id)
	var response map[string]any
	if err := callLocalAPIQuery(http.MethodGet, "/messages/receipts", query, nil, &response); err != nil {
		die("receipts: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdContacts(args []string) {
	if len(args) == 0 {
		die("usage: wacli contacts <list|lookup|update>")
	}
	switch args[0] {
	case "list":
		cmdContactsList(args[1:])
	case "lookup", "get":
		cmdContactsLookup(args[1:])
	case "update":
		cmdContactsUpdate(args[1:])
	default:
		die("usage: wacli contacts <list|lookup|update>")
	}
}

func cmdContactsList(args []string) {
	fs := flag.NewFlagSet("contacts list", flag.ExitOnError)
	limit := fs.Int("limit", 200, "maximum contacts to return")
	queryText := fs.String("query", "", "search by name, phone, or JID")
	_ = fs.Parse(args)

	query := url.Values{}
	query.Set("limit", strconv.Itoa(*limit))
	if strings.TrimSpace(*queryText) != "" {
		query.Set("query", strings.TrimSpace(*queryText))
	}

	var response map[string]any
	if err := callLocalAPIQuery(http.MethodGet, "/contacts", query, nil, &response); err != nil {
		die("contacts list: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdContactsLookup(args []string) {
	fs := flag.NewFlagSet("contacts lookup", flag.ExitOnError)
	refFlag := fs.String("ref", "", "contact/chat reference")
	_ = fs.Parse(args)

	ref := strings.TrimSpace(*refFlag)
	if ref == "" {
		ref = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if ref == "" {
		die("usage: wacli contacts lookup [--ref <reference>] <reference>")
	}

	query := url.Values{}
	query.Set("ref", ref)
	var response map[string]any
	if err := callLocalAPIQuery(http.MethodGet, "/contacts/lookup", query, nil, &response); err != nil {
		die("contacts lookup: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdContactsUpdate(args []string) {
	fs := flag.NewFlagSet("contacts update", flag.ExitOnError)
	refFlag := fs.String("ref", "", "contact/chat reference")
	bio := fs.String("bio", "", "bio value")
	notes := fs.String("notes", "", "freeform notes")
	memory := fs.String("memory", "", "AI memory summary")
	metadataJSON := fs.String("metadata-json", "", "arbitrary JSON string")
	_ = fs.Parse(args)

	ref := strings.TrimSpace(*refFlag)
	if ref == "" {
		ref = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if ref == "" {
		die("usage: wacli contacts update [--ref <reference>] [--bio ...] [--notes ...] [--memory ...] [--metadata-json ...]")
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
		die("contacts update: at least one of --bio, --notes, --memory, or --metadata-json is required")
	}

	var response map[string]any
	if err := callLocalAPI(http.MethodPut, "/contacts/update", body, &response); err != nil {
		die("contacts update: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdBulkSend(args []string) {
	fs := flag.NewFlagSet("bulk-send", flag.ExitOnError)
	intervalMS := fs.Int("interval-ms", 0, "delay between sends in milliseconds")
	itemsFile := fs.String("items-file", "", "JSON array/object or newline-delimited items file")
	stdinJSON := fs.Bool("stdin-json", false, "read JSON array/object or newline-delimited items from stdin")
	var itemFlags stringListFlag
	fs.Var(&itemFlags, "item", "bulk item as JSON object or to=...,text=...,media=...")
	_ = fs.Parse(args)

	var items []BulkSendItem
	for _, raw := range itemFlags {
		item, err := parseBulkSendItemSpec(raw)
		if err != nil {
			die("bulk-send item: %v", err)
		}
		items = append(items, item)
	}
	if strings.TrimSpace(*itemsFile) != "" {
		data, err := os.ReadFile(strings.TrimSpace(*itemsFile))
		if err != nil {
			die("bulk-send read items-file: %v", err)
		}
		parsed, err := parseBulkSendInput(data)
		if err != nil {
			die("bulk-send parse items-file: %v", err)
		}
		items = append(items, parsed...)
	}
	if *stdinJSON {
		data, err := readAllStdin()
		if err != nil {
			die("bulk-send read stdin: %v", err)
		}
		parsed, err := parseBulkSendInput(data)
		if err != nil {
			die("bulk-send parse stdin: %v", err)
		}
		items = append(items, parsed...)
	}
	if len(items) == 0 {
		die("usage: wacli bulk-send [--item '{\"to\":\"...\",\"text\":\"...\"}'] [--items-file path] [--stdin-json]")
	}

	var response map[string]any
	if err := callLocalAPI(http.MethodPost, "/bulk_send", map[string]any{
		"items":       items,
		"interval_ms": *intervalMS,
	}, &response); err != nil {
		die("bulk-send: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdMedia(args []string) {
	if len(args) == 0 {
		die("usage: wacli media <download>")
	}
	switch args[0] {
	case "download":
		cmdMediaDownload(args[1:])
	default:
		die("usage: wacli media <download>")
	}
}

func cmdMediaDownload(args []string) {
	fs := flag.NewFlagSet("media download", flag.ExitOnError)
	chatRef := fs.String("chat", "", "chat name, phone, chat ID, or JID")
	messageID := fs.String("message-id", "", "message id")
	_ = fs.Parse(args)

	if strings.TrimSpace(*chatRef) == "" || strings.TrimSpace(*messageID) == "" {
		die("usage: wacli media download --chat <reference> --message-id <id>")
	}

	var response map[string]any
	if err := callLocalAPI(http.MethodPost, "/media/download", map[string]any{
		"chat_ref":   strings.TrimSpace(*chatRef),
		"message_id": strings.TrimSpace(*messageID),
	}, &response); err != nil {
		die("media download: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdWebhooks(args []string) {
	if len(args) == 0 {
		die("usage: wacli webhooks <list|add|remove>")
	}
	switch args[0] {
	case "list":
		var response map[string]any
		if err := callLocalAPI(http.MethodGet, "/webhooks", nil, &response); err != nil {
			die("webhooks list: %v", err)
		}
		prettyPrintJSON(response)
	case "logs", "deliveries":
		cmdWebhookLogs(args[1:])
	case "add":
		cmdWebhooksAdd(args[1:])
	case "remove", "rm", "delete":
		cmdWebhooksRemove(args[1:])
	default:
		die("usage: wacli webhooks <list|add|remove>")
	}
}

func cmdWebhooksAdd(args []string) {
	fs := flag.NewFlagSet("webhooks add", flag.ExitOnError)
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
	_ = fs.Parse(args)

	if strings.TrimSpace(*urlValue) == "" {
		die("usage: wacli webhooks add --url <url> [--chat <ref> ...|--scope all_unlocked] [--events ...] [--message-types ...]")
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
	if err := callLocalAPI(http.MethodPost, "/webhooks", map[string]any{
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
		die("webhooks add: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdWebhooksRemove(args []string) {
	if len(args) == 0 {
		die("usage: wacli webhooks remove <id>")
	}
	id := strings.TrimSpace(args[0])
	var response map[string]any
	if err := callLocalAPI(http.MethodDelete, "/webhooks/"+url.PathEscape(id), nil, &response); err != nil {
		die("webhooks remove: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdWebhookLogs(args []string) {
	fs := flag.NewFlagSet("webhooks logs", flag.ExitOnError)
	limit := fs.Int("limit", 100, "maximum delivery logs to return")
	status := fs.String("status", "", "pending|done|failed")
	event := fs.String("event", "", "incoming_message|outgoing_message|...")
	queryText := fs.String("query", "", "search by URL, chat jid, message id, or error text")
	_ = fs.Parse(args)

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
	if err := callLocalAPIQuery(http.MethodGet, "/webhook_deliveries", query, nil, &response); err != nil {
		die("webhooks logs: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdAutoReplies(args []string) {
	if len(args) == 0 {
		die("usage: wacli auto-replies <list|add|remove>")
	}
	switch args[0] {
	case "list":
		var response map[string]any
		if err := callLocalAPI(http.MethodGet, "/auto_replies", nil, &response); err != nil {
			die("auto-replies list: %v", err)
		}
		prettyPrintJSON(response)
	case "add":
		cmdAutoRepliesAdd(args[1:])
	case "remove", "rm", "delete":
		cmdAutoRepliesRemove(args[1:])
	default:
		die("usage: wacli auto-replies <list|add|remove>")
	}
}

func cmdAutoRepliesAdd(args []string) {
	fs := flag.NewFlagSet("auto-replies add", flag.ExitOnError)
	name := fs.String("name", "", "rule name")
	matchType := fs.String("match-type", "contains", "always|exact|contains|prefix|suffix|regex")
	pattern := fs.String("pattern", "", "match pattern")
	replyText := fs.String("reply-text", "", "reply text")
	mediaPath := fs.String("media", "", "optional media file")
	disabled := fs.Bool("disabled", false, "create disabled rule")
	dms := fs.Bool("dms", true, "apply to DMs")
	groups := fs.Bool("groups", false, "apply to groups")
	priority := fs.Int("priority", 100, "lower values run earlier")
	_ = fs.Parse(args)

	if strings.TrimSpace(*name) == "" {
		die("usage: wacli auto-replies add --name <name> [--match-type ...] [--pattern ...] [--reply-text ...]")
	}

	var response map[string]any
	if err := callLocalAPI(http.MethodPost, "/auto_replies", map[string]any{
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
		die("auto-replies add: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdAutoRepliesRemove(args []string) {
	if len(args) == 0 {
		die("usage: wacli auto-replies remove <id>")
	}
	id := strings.TrimSpace(args[0])
	var response map[string]any
	if err := callLocalAPI(http.MethodDelete, "/auto_replies/"+url.PathEscape(id), nil, &response); err != nil {
		die("auto-replies remove: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdAPI(args []string) {
	fs := flag.NewFlagSet("api", flag.ExitOnError)
	bodyFile := fs.String("body-file", "", "read JSON body from file")
	stdinJSON := fs.Bool("stdin-json", false, "read JSON body from stdin")
	_ = fs.Parse(args)

	if fs.NArg() < 2 {
		die("usage: wacli api [--body-file path|--stdin-json] <METHOD> </path> [json-body]")
	}
	method := strings.ToUpper(strings.TrimSpace(fs.Arg(0)))
	path := strings.TrimSpace(fs.Arg(1))
	if path == "" {
		die("api path is required")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	body, err := loadOptionalJSONBody(fs.Args()[2:], *bodyFile, *stdinJSON)
	if err != nil {
		die("api body: %v", err)
	}

	var response any
	if err := callLocalAPI(method, path, body, &response); err != nil {
		die("api %s %s: %v", method, path, err)
	}
	prettyPrintJSON(response)
}

func callLocalAPIQuery(method, path string, query url.Values, body any, out any) error {
	if len(query) > 0 {
		path = path + "?" + query.Encode()
	}
	return callLocalAPI(method, path, body, out)
}

func parseBulkSendInput(data []byte) ([]BulkSendItem, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("no bulk send items provided")
	}

	if strings.HasPrefix(trimmed, "[") {
		var items []BulkSendItem
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var wrapped struct {
			Items []BulkSendItem `json:"items"`
		}
		if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil && len(wrapped.Items) > 0 {
			return wrapped.Items, nil
		}
		var item BulkSendItem
		if err := json.Unmarshal([]byte(trimmed), &item); err != nil {
			return nil, err
		}
		return []BulkSendItem{item}, nil
	}

	lines := strings.Split(trimmed, "\n")
	items := make([]BulkSendItem, 0, len(lines))
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

func parseBulkSendItemSpec(input string) (BulkSendItem, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return BulkSendItem{}, fmt.Errorf("empty bulk item")
	}
	if strings.HasPrefix(trimmed, "{") {
		var item BulkSendItem
		if err := json.Unmarshal([]byte(trimmed), &item); err != nil {
			return BulkSendItem{}, err
		}
		if strings.TrimSpace(item.To) == "" {
			return BulkSendItem{}, fmt.Errorf("bulk item missing to")
		}
		return item, nil
	}

	item := BulkSendItem{}
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return BulkSendItem{}, fmt.Errorf("invalid bulk item fragment %q", part)
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
			return BulkSendItem{}, fmt.Errorf("unsupported bulk item field %q", key)
		}
	}
	if strings.TrimSpace(item.To) == "" {
		return BulkSendItem{}, fmt.Errorf("bulk item missing to")
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

func readAllStdin() ([]byte, error) {
	return os.ReadFile("/dev/stdin")
}

func loadOptionalJSONBody(args []string, bodyFile string, stdinJSON bool) (any, error) {
	if strings.TrimSpace(bodyFile) != "" {
		data, err := os.ReadFile(strings.TrimSpace(bodyFile))
		if err != nil {
			return nil, err
		}
		return decodeArbitraryJSON(data)
	}
	if stdinJSON {
		data, err := readAllStdin()
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
