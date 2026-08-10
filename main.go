package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
)

// version is set at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Println("wacli", version)
		return
	case "login":
		cmdLogin(os.Args[2:])
	case "daemon":
		cmdDaemon()
	case "sync":
		cmdSync()
	case "status":
		cmdStatus()
	case "tui":
		cmdTUI()
	case "dnd":
		cmdDND(os.Args[2:])
	case "chats":
		cmdChats(os.Args[2:])
	case "access":
		cmdAccess(os.Args[2:])
	case "send":
		cmdSend(os.Args[2:])
	case "edit":
		cmdEdit(os.Args[2:])
	case "delete":
		cmdDelete(os.Args[2:])
	case "receipts":
		cmdReceipts(os.Args[2:])
	case "bulk-send":
		cmdBulkSend(os.Args[2:])
	case "story":
		cmdStory(os.Args[2:])
	case "call":
		cmdCall(os.Args[2:])
	case "media":
		cmdMedia(os.Args[2:])
	case "resolve":
		cmdResolve(os.Args[2:])
	case "messages":
		cmdMessages(os.Args[2:])
	case "contacts":
		cmdContacts(os.Args[2:])
	case "webhooks":
		cmdWebhooks(os.Args[2:])
	case "openclaw":
		cmdOpenClaw(os.Args[2:])
	case "auto-replies", "autoreplies":
		cmdAutoReplies(os.Args[2:])
	case "api":
		cmdAPI(os.Args[2:])
	default:
		usage()
	}
}

// absPath resolves a user-supplied file path against the shell's working directory.
//
// Audio and recording paths are handed to the daemon as text over the local API, and the daemon
// resolves what it receives against its own working directory — which is wherever it was started,
// not where the command was typed. Making them absolute here keeps "--record peer.wav" meaning the
// directory the user is standing in.
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

func openStoreOrDie() *Store {
	store, err := OpenStore(appDBPath)
	if err != nil {
		die("open store: %v", err)
	}
	return store
}

func newServiceOrDie(store *Store) *Service {
	service, err := NewService(store)
	if err != nil {
		die("create service: %v", err)
	}
	return service
}

func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	usePairCode := fs.Bool("pair-code", false, "use phone-number pairing instead of QR")
	skipAccessConfig := fs.Bool("skip-access-config", false, "skip interactive access configuration after login")
	_ = fs.Parse(args)
	if len(fs.Args()) > 0 && strings.EqualFold(fs.Args()[0], "code") {
		*usePairCode = true
	}

	store := openStoreOrDie()
	defer store.Close()
	service := newServiceOrDie(store)
	defer service.Close()

	if service.client.Store.ID != nil {
		fmt.Println("Existing session found, verifying...")
		if err := service.Connect(); err == nil && waitUntilConnected(service, 15*time.Second) {
			fmt.Println("Session valid.")
			postLoginSync(service)
			if !*skipAccessConfig {
				maybeConfigureAccess(store)
			}
			fmt.Printf("Start the daemon with: wacli daemon\n")
			return
		}
		fmt.Println("Existing session is stale. Clearing it and starting fresh.")
		service.Close()
		clearSession()
		service = newServiceOrDie(store)
		defer service.Close()
	}

	qrChan, err := service.client.GetQRChannel(context.Background())
	if err != nil {
		die("open QR channel: %v", err)
	}
	if err := service.client.Connect(); err != nil {
		die("connect: %v", err)
	}

	pairRequested := false
	for evt := range qrChan {
		switch evt.Event {
		case "code":
			if *usePairCode {
				if pairRequested {
					continue
				}
				phone := normalizePhone(os.Getenv("WACLI_PHONE"))
				if phone == "" {
					die("WACLI_PHONE env var is required for pairing-code login")
				}
				code, err := service.client.PairPhone(context.Background(), phone, false, whatsmeow.PairClientChrome, "Chrome (Linux)")
				if err != nil {
					die("pair phone: %v", err)
				}
				fmt.Printf("Pairing code: %s\n", code)
				fmt.Println("Open WhatsApp on your phone -> Linked Devices -> Link with phone number.")
				pairRequested = true
				continue
			}
			showQRCode(evt.Code)
		case "success":
			fmt.Println("Login successful.")
			postLoginSync(service)
			if !*skipAccessConfig {
				maybeConfigureAccess(store)
			}
			fmt.Printf("Start the daemon with: wacli daemon\n")
			return
		case "timeout":
			die("login timed out; run `wacli login` again")
		default:
			fmt.Printf("login event: %s\n", evt.Event)
		}
	}
}

func postLoginSync(service *Service) {
	fmt.Println("Syncing chats, messages, and contacts...")
	statusBefore, _ := service.store.BuildStatus(service.IsConnected(), service.CurrentUserJID())
	prevSync, _ := service.store.LastHistorySync()
	seen := false
	if prevSync == nil {
		seen = service.waitForBootstrapSync(120*time.Second, 6*time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if !seen && statusBefore.MessageCount > 0 {
		marker := service.historyMarker()
		if err := service.RequestHistorySync(ctx, 100); err != nil {
			if errors.Is(err, ErrHistoryAnchorUnavailable) {
				fmt.Fprintln(os.Stderr, "No local history anchor is available for on-demand sync yet. Waiting only for WhatsApp's bootstrap sync.")
			} else {
				fmt.Fprintf(os.Stderr, "history sync request failed: %v\n", err)
			}
		} else {
			seen = service.waitForHistoryQuiet(marker, 35*time.Second, 4*time.Second)
		}
	}
	if err := service.SyncContacts(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "contact sync failed: %v\n", err)
	}
	if err := service.RefreshMissingChatNames(ctx, 100); err != nil {
		fmt.Fprintf(os.Stderr, "chat name refresh failed: %v\n", err)
	}
	status, err := service.store.BuildStatus(service.IsConnected(), service.CurrentUserJID())
	if err != nil {
		fmt.Fprintf(os.Stderr, "status snapshot failed: %v\n", err)
		return
	}
	fmt.Printf("Sync complete. Chats: %d, messages: %d", status.ChatCount, status.MessageCount)
	if seen {
		fmt.Print(" (history sync observed)")
	}
	fmt.Println()
}

func waitUntilConnected(service *Service, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if service.IsConnected() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return service.IsConnected()
}

func showQRCode(code string) {
	if path, err := exec.LookPath("qrencode"); err == nil {
		cmd := exec.Command(path, "-o", qrPath, "-t", "PNG", "-s", "10")
		cmd.Stdin = strings.NewReader(code)
		if err := cmd.Run(); err == nil {
			fmt.Printf("QR code written to %s\n", qrPath)
		}
	}
	qrterminal.GenerateHalfBlock(code, qrterminal.L, os.Stdout)
	fmt.Println("Scan this QR with WhatsApp -> Linked Devices.")
}

func maybeConfigureAccess(store *Store) {
	configured, err := store.InitialAccessConfigured()
	if err != nil {
		fmt.Fprintf(os.Stderr, "read access configuration: %v\n", err)
		return
	}
	if configured {
		return
	}
	if err := runAccessConfiguration(store); err != nil {
		fmt.Fprintf(os.Stderr, "configure access: %v\n", err)
	}
}

func runAccessConfiguration(store *Store) error {
	chats, err := store.ListChats("all", 0, "")
	if err != nil {
		return err
	}
	if len(chats) == 0 {
		fmt.Println("No chats are in the local sync yet.")
		fmt.Println("Wait for the bootstrap sync to populate chats, then run `wacli access configure` again.")
		return nil
	}

	session := newAccessSession(chats)
	reader := bufio.NewReader(os.Stdin)
	printAccessManagerHelp()
	for {
		renderAccessSession(session)
		fmt.Print("access> ")
		input, err := reader.ReadString('\n')
		if err != nil && !errorsIsEOF(err) {
			return err
		}
		shouldSave, shouldQuit, err := handleAccessCommand(session, input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "access command error: %v\n", err)
		}
		if shouldSave {
			unlocked := session.unlockedJIDs()
			if err := store.ConfigureInitialAccess(unlocked); err != nil {
				return err
			}
			fmt.Printf("Access configuration saved. Unlocked chats: %d\n", len(unlocked))
			return nil
		}
		if shouldQuit {
			fmt.Println("Access configuration exited without changes.")
			return nil
		}
	}
}

type accessSession struct {
	allChats []ChatRecord
	locked   map[string]bool
	filter   string
	query    string
	page     int
	pageSize int
}

func newAccessSession(chats []ChatRecord) *accessSession {
	locked := make(map[string]bool, len(chats))
	for _, chat := range chats {
		locked[chat.JID] = chat.Locked
	}
	return &accessSession{
		allChats: chats,
		locked:   locked,
		filter:   "all",
		page:     0,
		pageSize: 25,
	}
}

func (s *accessSession) unlockedJIDs() []string {
	out := make([]string, 0, len(s.locked))
	for _, chat := range s.allChats {
		if !s.locked[chat.JID] {
			out = append(out, chat.JID)
		}
	}
	return out
}

func (s *accessSession) filteredChats() []ChatRecord {
	query := strings.ToLower(strings.TrimSpace(s.query))
	filtered := make([]ChatRecord, 0, len(s.allChats))
	for _, chat := range s.allChats {
		chat.Locked = s.locked[chat.JID]
		switch s.filter {
		case "locked":
			if !chat.Locked {
				continue
			}
		case "unlocked":
			if chat.Locked {
				continue
			}
		case "groups":
			if !chat.IsGroup {
				continue
			}
		case "dms":
			if chat.IsGroup {
				continue
			}
		}
		if query != "" {
			name := strings.ToLower(chat.Name)
			jid := strings.ToLower(chat.JID)
			preview := strings.ToLower(chat.LastMessagePreview)
			if !strings.Contains(name, query) && !strings.Contains(jid, query) && !strings.Contains(preview, query) {
				continue
			}
		}
		filtered = append(filtered, chat)
	}
	return filtered
}

func (s *accessSession) currentPageItems() ([]ChatRecord, int) {
	filtered := s.filteredChats()
	if s.pageSize <= 0 {
		s.pageSize = 25
	}
	totalPages := 1
	if len(filtered) > 0 {
		totalPages = (len(filtered) + s.pageSize - 1) / s.pageSize
	}
	if s.page < 0 {
		s.page = 0
	}
	if s.page >= totalPages {
		s.page = totalPages - 1
	}
	start := s.page * s.pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + s.pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[start:end], totalPages
}

func renderAccessSession(session *accessSession) {
	visible, totalPages := session.currentPageItems()
	filtered := session.filteredChats()
	lockedCount := 0
	for _, chat := range session.allChats {
		if session.locked[chat.JID] {
			lockedCount++
		}
	}
	fmt.Printf("\nAccess Manager | total=%d filtered=%d locked=%d unlocked=%d | filter=%s | query=%q | page=%d/%d\n",
		len(session.allChats), len(filtered), lockedCount, len(session.allChats)-lockedCount, session.filter, session.query, session.page+1, totalPages)
	for idx, chat := range visible {
		state := "UNLOCKED"
		if session.locked[chat.JID] {
			state = "LOCKED"
		}
		kind := "DM"
		if chat.IsGroup {
			kind = "GROUP"
		}
		fmt.Printf("  [%d] [%s] %s (%s)\n", idx+1, state, chat.Name, kind)
		fmt.Printf("      %s\n", chat.JID)
		if chat.LastMessagePreview != "" {
			fmt.Printf("      preview: %s\n", chat.LastMessagePreview)
		}
	}
	if len(visible) == 0 {
		fmt.Println("  No chats match the current filter.")
	}
}

func printAccessManagerHelp() {
	fmt.Println("Access manager commands:")
	fmt.Println("  help                    show this help")
	fmt.Println("  show                    redraw current page")
	fmt.Println("  next | prev             move pages")
	fmt.Println("  page <n>                jump to a page number")
	fmt.Println("  size <n>                set page size")
	fmt.Println("  filter <all|locked|unlocked|groups|dms>")
	fmt.Println("  search <text>           filter by name, JID, or preview")
	fmt.Println("  clear                   clear search and reset filter to all")
	fmt.Println("  unlock <1,2,5-8>        unlock page items by current visible index")
	fmt.Println("  lock <1,2,5-8>          lock page items by current visible index")
	fmt.Println("  unlock-visible          unlock every item on the current page")
	fmt.Println("  lock-visible            lock every item on the current page")
	fmt.Println("  save                    save the current lock state")
	fmt.Println("  quit                    exit without saving")
}

func handleAccessCommand(session *accessSession, input string) (bool, bool, error) {
	line := strings.TrimSpace(input)
	if line == "" || line == "show" {
		return false, false, nil
	}
	parts := strings.Fields(line)
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(line[len(parts[0]):])
	}
	switch cmd {
	case "help":
		printAccessManagerHelp()
	case "next", "n":
		session.page++
	case "prev", "p":
		session.page--
	case "page":
		value, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil || value < 1 {
			return false, false, fmt.Errorf("invalid page %q", strings.TrimSpace(arg))
		}
		session.page = value - 1
	case "size":
		value, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil || value < 1 || value > 200 {
			return false, false, fmt.Errorf("size must be between 1 and 200")
		}
		session.pageSize = value
		session.page = 0
	case "filter":
		value := strings.ToLower(strings.TrimSpace(arg))
		switch value {
		case "all", "locked", "unlocked", "groups", "dms":
			session.filter = value
			session.page = 0
		default:
			return false, false, fmt.Errorf("unsupported filter %q", value)
		}
	case "search":
		session.query = strings.TrimSpace(arg)
		session.page = 0
	case "clear":
		session.query = ""
		session.filter = "all"
		session.page = 0
	case "unlock":
		return false, false, applyAccessSelection(session, strings.TrimSpace(arg), false)
	case "lock":
		return false, false, applyAccessSelection(session, strings.TrimSpace(arg), true)
	case "unlock-visible":
		for _, chat := range currentVisibleChats(session) {
			session.locked[chat.JID] = false
		}
	case "lock-visible":
		for _, chat := range currentVisibleChats(session) {
			session.locked[chat.JID] = true
		}
	case "save", "done":
		return true, false, nil
	case "quit", "q", "exit":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("unknown command %q", cmd)
	}
	return false, false, nil
}

func currentVisibleChats(session *accessSession) []ChatRecord {
	visible, _ := session.currentPageItems()
	return visible
}

func applyAccessSelection(session *accessSession, input string, locked bool) error {
	visible := currentVisibleChats(session)
	if len(visible) == 0 {
		return errors.New("no visible chats to update")
	}
	if input == "" {
		return errors.New("selection required")
	}
	indices, err := parseSelection(input, len(visible))
	if err != nil {
		return err
	}
	for _, index := range indices {
		session.locked[visible[index].JID] = locked
	}
	return nil
}

func parseSelection(input string, total int) ([]int, error) {
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return nil, nil
	}
	if input == "all" {
		out := make([]int, 0, total)
		for i := 0; i < total; i++ {
			out = append(out, i)
		}
		return out, nil
	}
	seen := map[int]struct{}{}
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid selection %q", part)
			}
			end, err := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid selection %q", part)
			}
			if start > end {
				start, end = end, start
			}
			for i := start; i <= end; i++ {
				if i < 1 || i > total {
					return nil, fmt.Errorf("selection %d out of range", i)
				}
				seen[i-1] = struct{}{}
			}
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid selection %q", part)
		}
		if value < 1 || value > total {
			return nil, fmt.Errorf("selection %d out of range", value)
		}
		seen[value-1] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for index := range seen {
		out = append(out, index)
	}
	sort.Ints(out)
	return out, nil
}

func cmdDaemon() {
	store := openStoreOrDie()
	defer store.Close()
	service := newServiceOrDie(store)
	defer service.Close()

	if err := service.Connect(); err != nil {
		die("%v", err)
	}
	// Guard the long-running connection: recover from a socket that dies
	// silently while still reporting "connected" (messages stop arriving).
	service.StartConnectionWatchdog()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{
		Addr:    httpAddr,
		Handler: newHTTPHandler(service),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		service.Disconnect()
	}()

	fmt.Printf("wacli daemon ready on http://%s\n", httpAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		die("http server: %v", err)
	}
}

func cmdSync() {
	store := openStoreOrDie()
	defer store.Close()
	service := newServiceOrDie(store)
	defer service.Close()

	if err := service.Connect(); err != nil {
		die("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	marker := service.historyMarker()
	if err := service.RequestHistorySync(ctx, 100); err != nil {
		if errors.Is(err, ErrHistoryAnchorUnavailable) {
			fmt.Println("No local history anchor is available yet, so on-demand sync cannot be requested. Bootstrap sync only happens after login from the primary device.")
		} else {
			die("request history sync: %v", err)
		}
	}
	seen := service.waitForHistoryQuiet(marker, 40*time.Second, 4*time.Second)
	if err := service.SyncContacts(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "contact sync failed: %v\n", err)
	}
	if err := service.RefreshMissingChatNames(ctx, 100); err != nil {
		fmt.Fprintf(os.Stderr, "chat name refresh failed: %v\n", err)
	}
	status, err := store.BuildStatus(service.IsConnected(), service.CurrentUserJID())
	if err != nil {
		die("build status: %v", err)
	}
	fmt.Printf("Manual sync complete. Chats: %d, messages: %d, history seen: %v\n", status.ChatCount, status.MessageCount, seen)
}

func cmdStatus() {
	var status StatusSnapshot
	if err := callLocalAPI(http.MethodGet, "/status", nil, &status); err == nil {
		fmt.Printf("connected: %v\n", status.Connected)
		fmt.Printf("user: %s\n", status.UserJID)
		fmt.Printf("dnd: %v\n", status.DNDMode)
		fmt.Printf("initial access configured: %v\n", status.InitialAccessConfigured)
		fmt.Printf("chats: %d\n", status.ChatCount)
		fmt.Printf("messages: %d\n", status.MessageCount)
		if status.LastHistorySync != nil {
			fmt.Printf("last history sync: %s\n", status.LastHistorySync.Format(time.RFC3339))
		}
		return
	}

	store := openStoreOrDie()
	defer store.Close()
	status, err := store.BuildStatus(false, "")
	if err != nil {
		die("status: %v", err)
	}
	fmt.Println("daemon: not reachable")
	fmt.Printf("dnd: %v\n", status.DNDMode)
	fmt.Printf("initial access configured: %v\n", status.InitialAccessConfigured)
	fmt.Printf("chats: %d\n", status.ChatCount)
	fmt.Printf("messages: %d\n", status.MessageCount)
}

func cmdDND(args []string) {
	store := openStoreOrDie()
	defer store.Close()

	if len(args) == 0 {
		enabled, err := store.GetDNDMode()
		if err != nil {
			die("dnd status: %v", err)
		}
		if enabled {
			fmt.Println("DND mode: ON")
		} else {
			fmt.Println("DND mode: OFF")
		}
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "on":
		if err := store.SetDNDMode(true); err != nil {
			die("set dnd: %v", err)
		}
		fmt.Println("DND mode enabled")
	case "off":
		if err := store.SetDNDMode(false); err != nil {
			die("set dnd: %v", err)
		}
		fmt.Println("DND mode disabled")
	default:
		die("usage: wacli dnd [on|off]")
	}
}

func cmdChats(args []string) {
	fs := flag.NewFlagSet("chats", flag.ExitOnError)
	filter := fs.String("filter", "all", "all|locked|unlocked|groups|dms")
	limit := fs.Int("limit", 200, "maximum number of chats to list")
	query := fs.String("query", "", "search by name or jid")
	asJSON := fs.Bool("json", false, "output chats as a JSON array (for tooling)")
	_ = fs.Parse(args)

	store := openStoreOrDie()
	defer store.Close()
	chats, err := store.ListChats(*filter, *limit, *query)
	if err != nil {
		die("list chats: %v", err)
	}
	if *asJSON {
		out, err := json.MarshalIndent(chats, "", "  ")
		if err != nil {
			die("marshal chats: %v", err)
		}
		fmt.Println(string(out))
		return
	}
	if len(chats) == 0 {
		fmt.Println("No chats found.")
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
		fmt.Printf("[%s] %s (%s)\n", state, chat.Name, kind)
		fmt.Printf("  JID: %s\n", chat.JID)
		fmt.Printf("  Last Activity: %s\n", chat.LastMessageAt.Format("2006-01-02 15:04:05"))
		if chat.LastMessagePreview != "" {
			fmt.Printf("  Preview: %s\n", chat.LastMessagePreview)
		}
	}
}

func cmdAccess(args []string) {
	if len(args) == 0 {
		die("usage: wacli access <configure|list|lock|unlock>")
	}
	store := openStoreOrDie()
	defer store.Close()
	switch args[0] {
	case "configure":
		if err := runAccessConfiguration(store); err != nil {
			die("configure access: %v", err)
		}
	case "list":
		cmdAccessList(store, args[1:])
	case "lock":
		cmdAccessSetLock(store, true, args[1:])
	case "unlock":
		cmdAccessSetLock(store, false, args[1:])
	default:
		die("usage: wacli access <configure|list|lock|unlock>")
	}
}

func cmdAccessList(store *Store, args []string) {
	fs := flag.NewFlagSet("access list", flag.ExitOnError)
	filter := fs.String("filter", "all", "all|locked|unlocked|groups|dms")
	query := fs.String("query", "", "search by name, jid, or preview")
	page := fs.Int("page", 1, "1-based page number")
	pageSize := fs.Int("page-size", 25, "number of chats per page")
	_ = fs.Parse(args)

	chats, err := store.ListChats("all", 0, "")
	if err != nil {
		die("list access chats: %v", err)
	}
	session := newAccessSession(chats)
	session.filter = *filter
	session.query = *query
	session.page = *page - 1
	session.pageSize = *pageSize
	renderAccessSession(session)
}

func cmdAccessSetLock(store *Store, locked bool, args []string) {
	if len(args) == 0 {
		action := "unlock"
		if locked {
			action = "lock"
		}
		die("usage: wacli access %s <chat-ref> [chat-ref...]", action)
	}
	updated := 0
	for _, ref := range args {
		resolved, err := ResolveBestTarget(store, ref, ResolveOptions{
			Kind:        "chat",
			Limit:       10,
			AllowDirect: true,
		})
		if err != nil {
			die("resolve %q: %v", ref, err)
		}
		if !resolved.ExistsInChats {
			die("cannot %s %q: %s is not in synced chats", map[bool]string{true: "lock", false: "unlock"}[locked], ref, resolved.JID)
		}
		if err := store.SetChatLocked(resolved.JID, locked); err != nil {
			die("update %s: %v", resolved.JID, err)
		}
		state := "unlocked"
		if locked {
			state = "locked"
		}
		fmt.Printf("%s %s <%s>\n", state, resolved.Name, resolved.JID)
		updated++
	}
	if err := store.SetInitialAccessConfigured(true); err != nil {
		die("mark access configured: %v", err)
	}
	if locked {
		fmt.Printf("Locked %d chat(s)\n", updated)
	} else {
		fmt.Printf("Unlocked %d chat(s)\n", updated)
	}
}

func cmdSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	to := fs.String("to", "", "recipient JID or phone number")
	text := fs.String("text", "", "message text")
	mediaPath := fs.String("media", "", "optional local media path")
	replyTo := fs.String("reply-to", "", "message ID in the same chat to reply to (quotes it)")
	_ = fs.Parse(args)
	if *to == "" {
		die("usage: wacli send --to <jid|phone> [--text <text>] [--media <path>] [--reply-to <message-id>]")
	}
	if *text == "" && fs.NArg() > 0 {
		*text = strings.Join(fs.Args(), " ")
	}

	var response map[string]any
	if err := callLocalAPI(http.MethodPost, "/send", map[string]any{
		"to":         *to,
		"text":       *text,
		"media_path": *mediaPath,
		"reply_to":   *replyTo,
	}, &response); err != nil {
		die("send: %v", err)
	}
	prettyPrintJSON(response)
}

func cmdCall(args []string) {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		fs := flag.NewFlagSet("call list", flag.ExitOnError)
		activeOnly := fs.Bool("active", false, "only show calls that are still ringing or connected")
		_ = fs.Parse(args)
		path := "/calls"
		if *activeOnly {
			path += "?active=true"
		}
		var response map[string]any
		if err := callLocalAPI(http.MethodGet, path, nil, &response); err != nil {
			die("call list: %v", err)
		}
		prettyPrintJSON(response)
	case "end", "hangup":
		fs := flag.NewFlagSet("call end", flag.ExitOnError)
		id := fs.String("id", "", "call ID to hang up")
		reason := fs.String("reason", "", "termination reason (default: hangup)")
		_ = fs.Parse(args)
		if *id == "" {
			die("usage: wacli call end --id <call-id> [--reason <reason>]")
		}
		var response map[string]any
		if err := callLocalAPI(http.MethodPost, "/calls/end", map[string]any{
			"call_id": *id,
			"reason":  *reason,
		}, &response); err != nil {
			die("call end: %v", err)
		}
		prettyPrintJSON(response)
	case "status":
		fs := flag.NewFlagSet("call status", flag.ExitOnError)
		_ = fs.Parse(args)
		ref := ""
		if fs.NArg() > 0 {
			ref = fs.Arg(0)
		}
		var response map[string]any
		if err := callLocalAPI(http.MethodGet,
			"/calls/status?ref="+url.QueryEscape(ref), nil, &response); err != nil {
			die("call status: %v", err)
		}
		prettyPrintJSON(response)
	case "queue":
		fs := flag.NewFlagSet("call queue", flag.ExitOnError)
		_ = fs.Parse(args)
		var response map[string]any
		if err := callLocalAPI(http.MethodGet, "/calls/queue", nil, &response); err != nil {
			die("call queue: %v", err)
		}
		prettyPrintJSON(response)
	case "capture":
		fs := flag.NewFlagSet("call capture", flag.ExitOnError)
		off := fs.Bool("off", false, "stop capturing")
		_ = fs.Parse(args)
		var response map[string]any
		if err := callLocalAPI(http.MethodPost, "/calls/capture", map[string]any{
			"enabled": !*off,
		}, &response); err != nil {
			die("call capture: %v", err)
		}
		prettyPrintJSON(response)
	case "dump":
		fs := flag.NewFlagSet("call dump", flag.ExitOnError)
		last := fs.Int("last", 0, "only show the last N stanzas")
		_ = fs.Parse(args)
		records, err := LoadCaptures(capturePath)
		if err != nil {
			die("call dump: %v", err)
		}
		if len(records) == 0 {
			fmt.Println("no captured call stanzas yet — run 'wacli call capture' first, then make a call")
			return
		}
		if *last > 0 && *last < len(records) {
			records = records[len(records)-*last:]
		}
		for _, rec := range records {
			fmt.Print(DescribeCapture(rec))
		}
		fmt.Fprintf(os.Stderr, "\n%d stanza(s) from %s\n", len(records), capturePath)
	case "answer":
		fs := flag.NewFlagSet("call answer", flag.ExitOnError)
		id := fs.String("id", "", "call ID to answer (default: the only ringing call)")
		say := fs.String("say", "", "speak this text into the call")
		voice := fs.String("voice", "", "voice for --say (see: say -v '?')")
		audio := fs.String("audio", "", ".wav/.mp3/.opus to play instead of --say")
		repeat := fs.Bool("repeat", false, "loop the audio instead of hanging up when it ends")
		record := fs.String("record", "", "write the other party's voice to this .wav")
		_ = fs.Parse(args)
		var response map[string]any
		if err := callLocalAPI(http.MethodPost, "/calls/answer", map[string]any{
			"call_id": *id,
			"say":     *say,
			"voice":   *voice,
			"audio":   absPath(*audio),
			"repeat":  *repeat,
			"record":  absPath(*record),
		}, &response); err != nil {
			die("call answer: %v", err)
		}
		prettyPrintJSON(response)
	case "reject", "decline":
		fs := flag.NewFlagSet("call reject", flag.ExitOnError)
		id := fs.String("id", "", "call ID to decline")
		_ = fs.Parse(args)
		if *id == "" {
			die("usage: wacli call reject --id <call-id>")
		}
		var response map[string]any
		if err := callLocalAPI(http.MethodPost, "/calls/reject", map[string]any{
			"call_id": *id,
		}, &response); err != nil {
			die("call reject: %v", err)
		}
		prettyPrintJSON(response)
	case "", "place", "dial":
		fs := flag.NewFlagSet("call", flag.ExitOnError)
		to := fs.String("to", "", "recipient JID or phone number")
		video := fs.Bool("video", false, "place a video call instead of a voice call")
		ringFor := fs.Int("ring-for", 0, "seconds to ring before hanging up automatically (default 45)")
		noExpire := fs.Bool("no-expire", false, "keep ringing until explicitly ended")
		say := fs.String("say", "", "speak this text once they answer")
		voice := fs.String("voice", "", "voice for --say (see: say -v '?')")
		audio := fs.String("audio", "", ".wav/.mp3/.opus to play instead of --say")
		repeat := fs.Bool("repeat", false, "loop the audio instead of hanging up when it ends")
		record := fs.String("record", "", "write the other party's voice to this .wav")
		_ = fs.Parse(args)
		if *to == "" && fs.NArg() > 0 {
			*to = fs.Arg(0)
		}
		if *to == "" {
			die("usage: wacli call --to <jid|phone> [--say <text> | --audio <file>] [--video]")
		}
		var response map[string]any
		if err := callLocalAPI(http.MethodPost, "/calls", map[string]any{
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
			die("call: %v", err)
		}
		prettyPrintJSON(response)
		if *say == "" && *audio == "" {
			fmt.Fprintln(os.Stderr, "note: no --say/--audio, so this call rings but carries no audio.")
		}
	default:
		die("unknown call subcommand %q (want: place, answer, status, queue, list, end, reject, capture, dump)", sub)
	}
}

func cmdStory(args []string) {
	fs := flag.NewFlagSet("story", flag.ExitOnError)
	text := fs.String("text", "", "story text")
	mediaPath := fs.String("media", "", "optional image/video path")
	_ = fs.Parse(args)
	var response map[string]any
	if err := callLocalAPI(http.MethodPost, "/stories", map[string]any{
		"text":       *text,
		"media_path": *mediaPath,
	}, &response); err != nil {
		die("story: %v", err)
	}
	prettyPrintJSON(response)
}

func callLocalAPI(method, path string, body any, out any) error {
	return callLocalAPIWithTimeout(method, path, body, out, 20*time.Second)
}

func callLocalAPIWithTimeout(method, path string, body any, out any, timeout time.Duration) error {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, "http://"+httpAddr+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var payload map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil {
			if msg, ok := payload["error"].(string); ok {
				return errors.New(msg)
			}
		}
		return fmt.Errorf("request failed with status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func prettyPrintJSON(value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Printf("%v\n", value)
		return
	}
	fmt.Println(string(data))
}

func errorsIsEOF(err error) bool {
	return err == io.EOF
}

func usage() {
	fmt.Printf(`wacli

usage:
  wacli login [code]            authenticate with QR or pairing code
  wacli daemon                  run the local automation daemon on http://%s
  wacli tui                     open the Bubble Tea operations console
  wacli sync                    request a fresh history sync
  wacli status                  show daemon and local sync status
  wacli dnd [on|off]            toggle DND mode
  wacli chats [--filter ...]    list synced chats with lock state
  wacli access configure        paged interactive lock/unlock manager
  wacli access list             inspect access state with paging/search flags
  wacli access lock <ref...>    lock one or more chats by name, phone, chat ID, or JID
  wacli access unlock <ref...>  unlock one or more chats by name, phone, chat ID, or JID
  wacli resolve <ref>           resolve chat/contact names, numbers, chat IDs, or JIDs
  wacli messages ...            search messages by chat, sender, text, media, and time
  wacli contacts ...            list, lookup, and update contact memory
  wacli send --to ...           send a message or media through the daemon
  wacli edit --chat --id ...    edit a previously sent message's text
  wacli delete --chat --id ...  delete/revoke a sent message for everyone
  wacli receipts --id ...       show who has delivered/read a message
  wacli bulk-send ...           send many messages from JSON items, file, or stdin
  wacli media download ...      download message media by chat reference + message ID
  wacli story [--text ...]      post a WhatsApp story/status through the daemon
  wacli call --to ...           ring a contact (signaling only — carries no audio)
  wacli call list               show active and recent calls
  wacli call end --id ...       hang up a ringing or ongoing call
  wacli call reject --id ...    decline an incoming call
  wacli call capture [--off]    record raw call signaling stanzas for analysis
  wacli call bind [--v6]        attempt a TURN allocation using a captured relay token
  wacli call hold [--for 30s]   allocate and keep it alive with relay-pings
  wacli call status [<c1|id>]  show one call: state, duration, media, and the queue
  wacli call queue             show the active call and everything waiting behind it
  wacli call answer [--say s]  accept a ringing call and speak into it
  wacli call dump [--last N]    print captured call stanzas as an annotated tree
  wacli webhooks ...            list, add, remove, and inspect webhook delivery logs
  wacli openclaw ...            list, add, update, remove, replay, and inspect OpenClaw bridges
  wacli auto-replies ...        list, add, and remove auto-reply rules
  wacli api ...                 generic local API client for uncovered cases

AI-facing HTTP API:
  GET    /status
  GET    /assistant/settings
  PUT    /assistant/settings
  GET    /logs
  GET    /dnd
  PUT    /dnd
  POST   /sync
  GET    /resolve
  GET    /chats
  PUT    /chats/access
  PUT    /chats/{jid}
  GET    /messages
  POST   /send
  POST   /messages/edit
  POST   /messages/delete
  GET    /messages/receipts
  POST   /bulk_send
  POST   /stories
  GET    /calls
  POST   /calls
  GET    /calls/status?ref=<c1|call-id>
  GET    /calls/queue
  POST   /calls/answer
  POST   /calls/accept
  POST   /calls/end
  POST   /calls/reject
  POST   /calls/capture
  POST   /media/download
  GET    /contacts
  GET    /contacts/lookup
  PUT    /contacts/update
  GET    /contacts/{jid}
  PUT    /contacts/{jid}
  GET    /webhooks
  POST   /webhooks
  DELETE /webhooks/{id}
  GET    /webhook_deliveries
  GET    /openclaw_bridges
  POST   /openclaw_bridges
  GET    /openclaw_bridges/{id}
  PUT    /openclaw_bridges/{id}
  DELETE /openclaw_bridges/{id}
  GET    /openclaw_sessions
  GET    /openclaw_deliveries
  POST   /openclaw_replay
  GET    /auto_replies
  POST   /auto_replies
  DELETE /auto_replies/{id}

notes:
  - The daemon only listens on localhost by default.
  - Existing chats start locked after first login. You choose which chats the AI may touch.
  - New chats discovered after that are automatically unlocked.
  - Webhooks and outbound automation are only active while DND mode is ON.
`, httpAddr)
	os.Exit(0)
}
