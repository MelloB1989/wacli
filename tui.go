package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	wacliclient "github.com/MelloB1989/wacli/client"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tuiSection int

const (
	tuiSectionDashboard tuiSection = iota
	tuiSectionChats
	tuiSectionLogs
	tuiSectionWebhooks
)

var (
	tuiTitleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	tuiTabStyle       = lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("245"))
	tuiActiveTabStyle = lipgloss.NewStyle().Padding(0, 1).Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("62"))
	tuiMutedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	tuiWarningStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	tuiErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	tuiOkayStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	tuiBoxStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
	tuiCursorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
)

type tuiSnapshot struct {
	Status     wacliclient.StatusSnapshot
	Chats      []wacliclient.ChatRecord
	Logs       []wacliclient.AppLogRecord
	Webhooks   []wacliclient.WebhookRecord
	ChatFilter string
	FetchedAt  time.Time
}

type tuiSnapshotMsg struct {
	Snapshot tuiSnapshot
}

type tuiErrMsg struct {
	Err error
}

type tuiOpDoneMsg struct {
	Message string
}

type tuiTickMsg time.Time

type tuiModel struct {
	client *wacliclient.Client

	width  int
	height int

	active      tuiSection
	status      wacliclient.StatusSnapshot
	chats       []wacliclient.ChatRecord
	logs        []wacliclient.AppLogRecord
	webhooks    []wacliclient.WebhookRecord
	chatFilter  string
	lastRefresh time.Time

	chatIndex int
	logIndex  int

	statusMessage string
	lastError     string

	inputMode  bool
	inputField string
	input      textinput.Model
}

func newTUIModel() tuiModel {
	input := textinput.New()
	input.Cursor.Style = tuiCursorStyle
	input.Width = 64
	return tuiModel{
		client:     wacliclient.New(""),
		active:     tuiSectionDashboard,
		chatFilter: "all",
		input:      input,
	}
}

func cmdTUI() {
	model := newTUIModel()
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		die("tui: %v", err)
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), tuiTickCmd())
}

func tuiTickCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
		return tuiTickMsg(t)
	})
}

func (m tuiModel) refreshCmd() tea.Cmd {
	filter := m.chatFilter
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		status, err := client.Status(ctx)
		if err != nil {
			return tuiErrMsg{Err: err}
		}
		chats, err := client.ListChats(ctx, wacliclient.ChatListOptions{Filter: filter, Limit: 200})
		if err != nil {
			return tuiErrMsg{Err: err}
		}
		logs, err := client.ListLogs(ctx, wacliclient.LogQuery{Limit: 200})
		if err != nil {
			return tuiErrMsg{Err: err}
		}
		webhooks, err := client.ListWebhooks(ctx)
		if err != nil {
			return tuiErrMsg{Err: err}
		}
		return tuiSnapshotMsg{
			Snapshot: tuiSnapshot{
				Status:     status,
				Chats:      chats,
				Logs:       logs,
				Webhooks:   webhooks,
				ChatFilter: filter,
				FetchedAt:  time.Now(),
			},
		}
	}
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.inputMode {
		return m.updateInputMode(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tuiSnapshotMsg:
		m.status = msg.Snapshot.Status
		m.chats = msg.Snapshot.Chats
		m.logs = msg.Snapshot.Logs
		m.webhooks = msg.Snapshot.Webhooks
		m.chatFilter = msg.Snapshot.ChatFilter
		m.lastRefresh = msg.Snapshot.FetchedAt
		m.statusMessage = fmt.Sprintf("Refreshed %s", m.lastRefresh.Format("15:04:05"))
		m.lastError = ""
		m.chatIndex = clampIndex(m.chatIndex, len(m.chats))
		m.logIndex = clampIndex(m.logIndex, len(m.logs))
		return m, nil
	case tuiErrMsg:
		m.lastError = msg.Err.Error()
		m.statusMessage = ""
		return m, tuiTickCmd()
	case tuiOpDoneMsg:
		m.statusMessage = msg.Message
		m.lastError = ""
		return m, m.refreshCmd()
	case tuiTickMsg:
		return m, tea.Batch(m.refreshCmd(), tuiTickCmd())
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "r":
			return m, m.refreshCmd()
		case "1":
			m.active = tuiSectionDashboard
			return m, nil
		case "2":
			m.active = tuiSectionChats
			return m, nil
		case "3":
			m.active = tuiSectionLogs
			return m, nil
		case "4":
			m.active = tuiSectionWebhooks
			return m, nil
		case "d":
			return m, m.toggleDNDCmd()
		case "j", "down":
			switch m.active {
			case tuiSectionChats:
				m.chatIndex = clampIndex(m.chatIndex+1, len(m.chats))
			case tuiSectionLogs:
				m.logIndex = clampIndex(m.logIndex+1, len(m.logs))
			}
			return m, nil
		case "k", "up":
			switch m.active {
			case tuiSectionChats:
				m.chatIndex = clampIndex(m.chatIndex-1, len(m.chats))
			case tuiSectionLogs:
				m.logIndex = clampIndex(m.logIndex-1, len(m.logs))
			}
			return m, nil
		case "f":
			if m.active == tuiSectionChats {
				m.chatFilter = nextChatFilter(m.chatFilter)
				return m, m.refreshCmd()
			}
		case "l":
			if m.active == tuiSectionChats && len(m.chats) > 0 {
				chat := m.chats[m.chatIndex]
				return m, m.setChatLockedCmd(chat.JID, !chat.Locked)
			}
		}
	}

	return m, nil
}

func (m tuiModel) updateInputMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			m.inputMode = false
			m.inputField = ""
			m.input.Blur()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m tuiModel) toggleDNDCmd() tea.Cmd {
	client := m.client
	next := !m.status.DNDMode
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		enabled, err := client.SetDND(ctx, next)
		if err != nil {
			return tuiErrMsg{Err: err}
		}
		return tuiOpDoneMsg{Message: fmt.Sprintf("DND set to %t", enabled)}
	}
}

func (m tuiModel) setChatLockedCmd(jid string, locked bool) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := client.SetChatLocked(ctx, jid, locked); err != nil {
			return tuiErrMsg{Err: err}
		}
		state := "unlocked"
		if locked {
			state = "locked"
		}
		return tuiOpDoneMsg{Message: fmt.Sprintf("%s %s", jid, state)}
	}
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "Loading WACLI TUI..."
	}
	var body string
	switch m.active {
	case tuiSectionChats:
		body = m.viewChats()
	case tuiSectionLogs:
		body = m.viewLogs()
	case tuiSectionWebhooks:
		body = m.viewWebhooks()
	default:
		body = m.viewDashboard()
	}

	statusLine := tuiMutedStyle.Render("1 Dashboard  2 Chats  3 Logs  4 Webhooks  |  r refresh  d toggle DND  q quit")
	if m.inputMode {
		statusLine = tuiTitleStyle.Render("Editing "+m.inputField+":") + "\n" + m.input.View()
	}
	footer := strings.TrimSpace(strings.Join([]string{
		renderStatusMessage(m.statusMessage, false),
		renderStatusMessage(m.lastError, true),
	}, "  "))
	if footer == "" {
		footer = tuiMutedStyle.Render("Ready")
	}

	return strings.Join([]string{
		m.viewTabs(),
		body,
		statusLine,
		footer,
	}, "\n\n")
}

func (m tuiModel) viewTabs() string {
	labels := []struct {
		Section tuiSection
		Label   string
	}{
		{tuiSectionDashboard, "Dashboard"},
		{tuiSectionChats, "Chats"},
		{tuiSectionLogs, "Logs"},
		{tuiSectionWebhooks, "Webhooks"},
	}
	parts := make([]string, 0, len(labels))
	for _, item := range labels {
		style := tuiTabStyle
		if item.Section == m.active {
			style = tuiActiveTabStyle
		}
		parts = append(parts, style.Render(item.Label))
	}
	return strings.Join(parts, " ")
}

func (m tuiModel) viewDashboard() string {
	var lastSync string
	if m.status.LastHistorySync != nil {
		lastSync = m.status.LastHistorySync.Local().Format(time.RFC822)
	} else {
		lastSync = "never"
	}
	lines := []string{
		tuiTitleStyle.Render("WACLI"),
		fmt.Sprintf("Connected: %s", boolBadge(m.status.Connected)),
		fmt.Sprintf("DND mode: %s", boolBadge(m.status.DNDMode)),
		fmt.Sprintf("Chats: %d", m.status.ChatCount),
		fmt.Sprintf("Messages: %d", m.status.MessageCount),
		fmt.Sprintf("Last sync: %s", lastSync),
		fmt.Sprintf("Webhooks: %d", len(m.webhooks)),
		fmt.Sprintf("Last refresh: %s", m.lastRefresh.Format("15:04:05")),
	}
	return tuiBoxStyle.Width(max(40, m.width-4)).Render(strings.Join(lines, "\n"))
}

func (m tuiModel) viewChats() string {
	lines := []string{
		tuiTitleStyle.Render("Chats"),
		fmt.Sprintf("Filter: %s", m.chatFilter),
	}
	if len(m.chats) == 0 {
		lines = append(lines, tuiMutedStyle.Render("No chats"))
		return tuiBoxStyle.Width(max(40, m.width-4)).Render(strings.Join(lines, "\n"))
	}
	for idx, chat := range m.chats {
		cursor := " "
		if idx == m.chatIndex {
			cursor = ">"
		}
		lockState := "UNLOCKED"
		if chat.Locked {
			lockState = "LOCKED"
		}
		kind := "DM"
		if chat.IsGroup {
			kind = "GROUP"
		}
		lines = append(lines, fmt.Sprintf("%s [%s] %s (%s)", cursor, lockState, chat.Name, kind))
		if idx == m.chatIndex {
			lines = append(lines, tuiMutedStyle.Render("  "+chat.JID))
		}
		if idx >= 24 {
			lines = append(lines, tuiMutedStyle.Render(fmt.Sprintf("...and %d more", len(m.chats)-idx-1)))
			break
		}
	}
	lines = append(lines, "", tuiMutedStyle.Render("Use j/k to move, l to toggle lock, f to cycle filters"))
	return tuiBoxStyle.Width(max(40, m.width-4)).Render(strings.Join(lines, "\n"))
}

func (m tuiModel) viewLogs() string {
	lines := []string{tuiTitleStyle.Render("Logs")}
	if len(m.logs) == 0 {
		lines = append(lines, tuiMutedStyle.Render("No logs"))
		return tuiBoxStyle.Width(max(40, m.width-4)).Render(strings.Join(lines, "\n"))
	}
	for idx, record := range m.logs {
		cursor := " "
		if idx == m.logIndex {
			cursor = ">"
		}
		lines = append(lines, fmt.Sprintf("%s %s [%s] %s", cursor, record.CreatedAt.Format("15:04:05"), record.Category, record.Message))
		if idx == m.logIndex && strings.TrimSpace(record.DetailsJSON) != "" {
			lines = append(lines, tuiMutedStyle.Render("  "+truncateLogLine(record.DetailsJSON, max(32, m.width-12))))
		}
		if idx >= 24 {
			lines = append(lines, tuiMutedStyle.Render(fmt.Sprintf("...and %d more", len(m.logs)-idx-1)))
			break
		}
	}
	return tuiBoxStyle.Width(max(40, m.width-4)).Render(strings.Join(lines, "\n"))
}

func (m tuiModel) viewWebhooks() string {
	lines := []string{tuiTitleStyle.Render("Webhooks")}
	lines = append(lines, fmt.Sprintf("HTTP webhooks: %d", len(m.webhooks)))
	for idx, webhook := range m.webhooks {
		if idx >= 8 {
			lines = append(lines, tuiMutedStyle.Render(fmt.Sprintf("...and %d more webhooks", len(m.webhooks)-idx)))
			break
		}
		lines = append(lines, fmt.Sprintf("- #%d %s [%s]", webhook.ID, webhook.URL, boolLabel(webhook.Enabled)))
	}
	return tuiBoxStyle.Width(max(40, m.width-4)).Render(strings.Join(lines, "\n"))
}

func nextChatFilter(current string) string {
	order := []string{"all", "locked", "unlocked", "groups", "dms"}
	for idx, candidate := range order {
		if candidate == current {
			return order[(idx+1)%len(order)]
		}
	}
	return order[0]
}

func clampIndex(idx, length int) int {
	if length <= 0 {
		return 0
	}
	if idx < 0 {
		return 0
	}
	if idx >= length {
		return length - 1
	}
	return idx
}

func boolBadge(v bool) string {
	if v {
		return tuiOkayStyle.Render("yes")
	}
	return tuiWarningStyle.Render("no")
}

func boolLabel(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func renderStatusMessage(value string, isError bool) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if isError {
		return tuiErrorStyle.Render(value)
	}
	return tuiOkayStyle.Render(value)
}

func truncateLogLine(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
