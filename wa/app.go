package wa

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Paths and settings for this wacli instance, resolved once from the environment.
//
// WACLI_HOME lets several independent instances share one OS user, each with its own WhatsApp
// session, database and media; pair it with WACLI_HTTP_ADDR so each daemon binds its own port.
var (
	DataDir       string
	MediaDir      string
	CapturePath   string
	SessionDBPath string
	AppDBPath     string
	QRPath        string
	HTTPAddr      string

	// InitErr reports a failure to prepare the data directory. A library must not exit the process,
	// so init records the problem and OpenStore/NewService refuse to start with it.
	InitErr error
)

const defaultHTTPAddr = "127.0.0.1:8765"

func init() {
	// WACLI_HOME lets several independent wacli instances share one OS user,
	// each with its own WhatsApp session, database and media. Pair it with
	// WACLI_HTTP_ADDR so each daemon binds its own port. Defaults to ~/.wacli.
	InitErr = Configure(strings.TrimSpace(os.Getenv("WACLI_HOME")))
}

// Configure points this process at a state directory, deriving every path under it and creating the
// directory tree. An empty home means "resolve it the usual way": WACLI_HOME, else ~/.wacli.
//
// init calls this, which covers the CLI and the daemon. It is exported for the mobile bindings:
// an app only learns its sandbox path at runtime, well after package init has run, and neither
// Android nor iOS has a home directory to fall back on — so the Expo module calls Configure with
// its container path before opening a store.
//
// Call it before OpenStore or NewService, not after: both capture the paths as they stand when they
// open, so moving the state directory under a live service does nothing useful.
func Configure(home string) error {
	InitErr = nil
	DataDir = strings.TrimSpace(home)
	if DataDir == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine home directory: %w", err)
		}
		DataDir = filepath.Join(userHome, ".wacli")
	}
	MediaDir = filepath.Join(DataDir, "media")
	// CapturePath holds raw call signaling stanzas when call capture is enabled. See callcapture.go.
	CapturePath = filepath.Join(DataDir, captureFileName)
	SessionDBPath = filepath.Join(DataDir, "session.db")
	AppDBPath = filepath.Join(DataDir, "state.db")
	QRPath = filepath.Join(DataDir, "qr.png")
	HTTPAddr = strings.TrimSpace(os.Getenv("WACLI_HTTP_ADDR"))
	if HTTPAddr == "" {
		HTTPAddr = defaultHTTPAddr
	}
	if err := os.MkdirAll(DataDir, 0o700); err != nil {
		return fmt.Errorf("cannot create data dir: %w", err)
	}
	if err := os.MkdirAll(MediaDir, 0o700); err != nil {
		return fmt.Errorf("cannot create media dir: %w", err)
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func intToBool(v int) bool {
	return v != 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func NormalizePhone(input string) string {
	clean := strings.TrimSpace(input)
	if at := strings.Index(clean, "@"); at >= 0 {
		clean = clean[:at]
	}
	if colon := strings.Index(clean, ":"); colon >= 0 {
		clean = clean[:colon]
	}
	clean = strings.TrimPrefix(clean, "+")
	re := regexp.MustCompile(`\D+`)
	clean = re.ReplaceAllString(clean, "")
	return clean
}

func sanitizeFileName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "file"
	}
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	clean := re.ReplaceAllString(name, "_")
	clean = strings.Trim(clean, "._")
	if clean == "" {
		return "file"
	}
	return clean
}

func sanitizePathPart(value string) string {
	replacer := strings.NewReplacer("@", "_", "/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(value)
}
