package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx    context.Context
	client *daemonClient

	loginMu       sync.Mutex
	loginCmd      *exec.Cmd
	loginRunning  bool
	loginPairCode bool
	loginStarted  time.Time
	loginOutput   []string
	loginError    string
}

func NewApp() *App {
	return &App{
		client: newDaemonClient(),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) GetStatus() (StatusSnapshot, error) {
	ctx, cancel := a.callContext(10 * time.Second)
	defer cancel()
	return a.client.status(ctx)
}

func (a *App) SetDND(enabled bool) error {
	ctx, cancel := a.callContext(10 * time.Second)
	defer cancel()
	return a.client.setDND(ctx, enabled)
}

func (a *App) Sync() (SyncResponse, error) {
	ctx, cancel := a.callContext(45 * time.Second)
	defer cancel()
	return a.client.sync(ctx)
}

func (a *App) ListChats(filter, query string, limit int) ([]ChatRecord, error) {
	ctx, cancel := a.callContext(15 * time.Second)
	defer cancel()
	return a.client.chats(ctx, filter, query, limit)
}

func (a *App) SetChatLocked(jid string, locked bool) error {
	ctx, cancel := a.callContext(10 * time.Second)
	defer cancel()
	return a.client.setChatLocked(ctx, jid, locked)
}

func (a *App) ConfigureAccess(unlockedJIDs []string) error {
	ctx, cancel := a.callContext(20 * time.Second)
	defer cancel()
	return a.client.configureAccess(ctx, unlockedJIDs)
}

func (a *App) ListLogs(limit int) ([]AppLogRecord, error) {
	ctx, cancel := a.callContext(10 * time.Second)
	defer cancel()
	return a.client.logs(ctx, limit)
}

func (a *App) ListWebhooks() ([]WebhookRecord, error) {
	ctx, cancel := a.callContext(10 * time.Second)
	defer cancel()
	return a.client.webhooks(ctx)
}

func (a *App) CreateWebhook(record WebhookRecord) (WebhookRecord, error) {
	ctx, cancel := a.callContext(15 * time.Second)
	defer cancel()
	return a.client.createWebhook(ctx, record)
}

func (a *App) DeleteWebhook(id int64) error {
	ctx, cancel := a.callContext(10 * time.Second)
	defer cancel()
	return a.client.deleteWebhook(ctx, id)
}

func (a *App) StartLogin(pairCode bool, phone string) error {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	if a.loginRunning {
		return errors.New("login already running")
	}
	binary, err := findWACLIBinary()
	if err != nil {
		return err
	}
	_ = os.Remove(qrFilePath())

	args := []string{"login", "--skip-access-config"}
	if pairCode {
		args = append(args, "--pair-code")
	}
	cmd := exec.Command(binary, args...)
	if pairCode && strings.TrimSpace(phone) != "" {
		cmd.Env = append(os.Environ(), "WACLI_PHONE="+strings.TrimSpace(phone))
	} else {
		cmd.Env = os.Environ()
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	a.loginCmd = cmd
	a.loginRunning = true
	a.loginPairCode = pairCode
	a.loginStarted = time.Now()
	a.loginOutput = nil
	a.loginError = ""
	a.emitLoginState()

	go a.consumeLoginPipe(stdout)
	go a.consumeLoginPipe(stderr)
	go a.waitLogin()
	return nil
}

func (a *App) GetLoginSession() LoginSessionState {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	return a.snapshotLoginState()
}

func (a *App) GetQRCodeData() (string, error) {
	return readQRDataURI()
}

// callContext derives a deadline for one daemon call. The caller must defer the returned cancel:
// dropping it leaks the context and its timer until the deadline fires, and these are per-UI-action
// calls against a long-lived parent, so they accumulate for as long as the app is open.
func (a *App) callContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx := context.Background()
	if a.ctx != nil {
		ctx = a.ctx
	}
	return context.WithTimeout(ctx, timeout)
}

func (a *App) consumeLoginPipe(reader io.ReadCloser) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		a.appendLoginOutput(scanner.Text())
	}
}

func (a *App) waitLogin() {
	a.loginMu.Lock()
	cmd := a.loginCmd
	a.loginMu.Unlock()
	if cmd == nil {
		return
	}
	err := cmd.Wait()
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	a.loginRunning = false
	a.loginCmd = nil
	if err != nil {
		a.loginError = err.Error()
	}
	a.emitLoginState()
}

func (a *App) appendLoginOutput(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	a.loginMu.Lock()
	a.loginOutput = append(a.loginOutput, line)
	if len(a.loginOutput) > 200 {
		a.loginOutput = a.loginOutput[len(a.loginOutput)-200:]
	}
	a.loginMu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "login:output", line)
		a.emitLoginState()
	}
}

func (a *App) emitLoginState() {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "login:state", a.snapshotLoginState())
}

func (a *App) snapshotLoginState() LoginSessionState {
	output := append([]string(nil), a.loginOutput...)
	startedAt := ""
	if !a.loginStarted.IsZero() {
		startedAt = a.loginStarted.Format(time.RFC3339)
	}
	return LoginSessionState{
		Running:     a.loginRunning,
		PairCode:    a.loginPairCode,
		StartedAt:   startedAt,
		QRPath:      qrFilePath(),
		QRAvailable: fileExists(qrFilePath()),
		OutputLines: output,
		LastError:   a.loginError,
	}
}

func findWACLIBinary() (string, error) {
	if envPath := strings.TrimSpace(os.Getenv("WACLI_BIN")); envPath != "" {
		return envPath, nil
	}
	if resolved, err := exec.LookPath("wacli"); err == nil {
		return resolved, nil
	}
	if wd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(filepath.Dir(wd), "wacli")
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "..", "wacli")
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("could not find wacli binary; set WACLI_BIN or add wacli to PATH")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
