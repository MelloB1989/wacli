package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	dataDir       string
	mediaDir      string
	capturePath   string
	sessionDBPath string
	appDBPath     string
	qrPath        string
	httpAddr      string
)

const defaultHTTPAddr = "127.0.0.1:8765"

func init() {
	// WACLI_HOME lets several independent wacli instances share one OS user,
	// each with its own WhatsApp session, database and media. Pair it with
	// WACLI_HTTP_ADDR so each daemon binds its own port. Defaults to ~/.wacli.
	dataDir = strings.TrimSpace(os.Getenv("WACLI_HOME"))
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			die("cannot determine home directory: %v", err)
		}
		dataDir = filepath.Join(home, ".wacli")
	}
	mediaDir = filepath.Join(dataDir, "media")
	// capturePath holds raw call signaling stanzas when call capture is enabled. See callcapture.go.
	capturePath = filepath.Join(dataDir, captureFileName)
	sessionDBPath = filepath.Join(dataDir, "session.db")
	appDBPath = filepath.Join(dataDir, "state.db")
	qrPath = filepath.Join(dataDir, "qr.png")
	httpAddr = strings.TrimSpace(os.Getenv("WACLI_HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = defaultHTTPAddr
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		die("cannot create data dir: %v", err)
	}
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		die("cannot create media dir: %v", err)
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
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

func normalizePhone(input string) string {
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
