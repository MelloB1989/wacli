package wa

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeStore builds something shaped like a wacli store: whatsmeow's session tables alongside
// wacli's own message history, which is the bulk of a real file and must not travel.
func fakeSessionStore(t *testing.T, messages int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wacli.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE whatsmeow_device (jid TEXT PRIMARY KEY, registration_id INTEGER, noise_key BLOB)`,
		`CREATE TABLE whatsmeow_sessions (our_jid TEXT, their_id TEXT, session BLOB, PRIMARY KEY (our_jid, their_id))`,
		`CREATE TABLE whatsmeow_pre_keys (jid TEXT, key_id INTEGER, key BLOB, PRIMARY KEY (jid, key_id))`,
		`CREATE TABLE whatsmeow_app_state_sync_keys (jid TEXT, key_id BLOB, key_data BLOB, PRIMARY KEY (jid, key_id))`,
		`CREATE INDEX whatsmeow_sessions_jid ON whatsmeow_sessions (our_jid)`,
		// wacli's own, and deliberately the biggest thing in the file.
		`CREATE TABLE messages (id TEXT PRIMARY KEY, chat TEXT, content TEXT)`,
		`CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT)`,
		`INSERT INTO whatsmeow_device VALUES ('919000977879:1@s.whatsapp.net', 42, x'0102030405')`,
		`INSERT INTO whatsmeow_sessions VALUES ('919000977879:1@s.whatsapp.net', '919812345678', x'aabbcc')`,
		`INSERT INTO whatsmeow_pre_keys VALUES ('919000977879:1@s.whatsapp.net', 7, x'ddee')`,
		`INSERT INTO whatsmeow_app_state_sync_keys VALUES ('919000977879:1@s.whatsapp.net', x'01', x'02')`,
		`INSERT INTO chats VALUES ('919812345678@s.whatsapp.net', 'Aaji')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	for i := 0; i < messages; i++ {
		if _, err := db.Exec(`INSERT INTO messages VALUES (?, 'chat', ?)`,
			"m"+strings.Repeat("x", 12)+string(rune('a'+i%26))+string(rune('0'+i%10))+sessionItoa(i),
			strings.Repeat("private message content ", 40),
		); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	return path
}

func sessionItoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestSessionExportCarriesSessionAndNotHistory(t *testing.T) {
	ctx := context.Background()
	path := fakeSessionStore(t, 500)

	blob, err := ExportSession(ctx, path)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	tables, err := SessionTables(ctx, blob)
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	for _, want := range []string{
		"whatsmeow_device", "whatsmeow_sessions",
		"whatsmeow_pre_keys", "whatsmeow_app_state_sync_keys",
	} {
		if !slices.Contains(tables, want) {
			t.Fatalf("export is missing %s; has %v", want, tables)
		}
	}
	// The whole point: a call does not need message history, and it is the most sensitive thing
	// we could carry across a network we did not have to.
	for _, unwanted := range []string{"messages", "chats"} {
		if slices.Contains(tables, unwanted) {
			t.Fatalf("export leaked %s", unwanted)
		}
	}
}

func TestSessionExportIsSubstantiallySmallerThanTheStore(t *testing.T) {
	ctx := context.Background()
	path := fakeSessionStore(t, 2000)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	blob, err := ExportSession(ctx, path)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	// Leaving history behind should be visible in the numbers, not just in the table list.
	if int64(len(blob)) > info.Size()/10 {
		t.Fatalf("export is %d bytes against a %d byte store — history is coming along",
			len(blob), info.Size())
	}
}

func TestSessionRoundTripPreservesSessionRows(t *testing.T) {
	ctx := context.Background()
	blob, err := ExportSession(ctx, fakeSessionStore(t, 20))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "nested", "wacli.db")
	if err := ImportSession(ctx, blob, dest); err != nil {
		t.Fatalf("Import: %v", err)
	}

	db, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatalf("open imported: %v", err)
	}
	defer db.Close()

	var jid string
	var regID int
	if err := db.QueryRow(`SELECT jid, registration_id FROM whatsmeow_device`).Scan(&jid, &regID); err != nil {
		t.Fatalf("read device: %v", err)
	}
	if jid != "919000977879:1@s.whatsapp.net" || regID != 42 {
		t.Fatalf("device row changed: %s / %d", jid, regID)
	}

	// The ratchet is the part that must survive byte for byte.
	var session []byte
	if err := db.QueryRow(`SELECT session FROM whatsmeow_sessions`).Scan(&session); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if !slices.Equal(session, []byte{0xaa, 0xbb, 0xcc}) {
		t.Fatalf("signal session did not survive: %x", session)
	}
}

func TestSessionImportReplacesRatherThanMerges(t *testing.T) {
	ctx := context.Background()
	blob, err := ExportSession(ctx, fakeSessionStore(t, 5))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// A stale store already sitting at the destination must be wholly replaced. Two versions of a
	// ratchet cannot be reconciled, so merging is never the right answer.
	dest := filepath.Join(t.TempDir(), "wacli.db")
	stale := fakeSessionStore(t, 3)
	raw, err := os.ReadFile(stale)
	if err != nil {
		t.Fatalf("read stale: %v", err)
	}
	if err := os.WriteFile(dest, raw, 0o600); err != nil {
		t.Fatalf("seed stale: %v", err)
	}

	if err := ImportSession(ctx, blob, dest); err != nil {
		t.Fatalf("Import: %v", err)
	}

	db, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='messages'`).Scan(&n); err != nil {
		t.Fatalf("check: %v", err)
	}
	if n != 0 {
		t.Fatal("the stale store's own tables survived the import")
	}
}

func TestSessionImportRejectsRubbish(t *testing.T) {
	ctx := context.Background()
	dest := filepath.Join(t.TempDir(), "wacli.db")

	if err := ImportSession(ctx, []byte("not gzip at all"), dest); err == nil {
		t.Fatal("non-gzip input was accepted")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("a rejected import still wrote the destination")
	}

	// Gzip of something that is not a database. A half-written store is worse than none: it looks
	// paired and is not.
	notADB, err := compressSession([]byte("hello, this is definitely not sqlite"))
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := ImportSession(ctx, notADB, dest); err == nil {
		t.Fatal("a non-SQLite payload was accepted")
	}
	if _, err := os.Stat(dest + ".incoming"); err == nil {
		t.Fatal("a rejected import left its temp file behind")
	}
}

func TestSessionImportRejectsUnpairedSession(t *testing.T) {
	ctx := context.Background()

	// Well-formed, right tables, no device — imports cleanly and then fails much later at connect
	// time as "not paired", pointing at entirely the wrong thing.
	empty := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite", empty)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE whatsmeow_device (jid TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()

	raw, err := os.ReadFile(empty)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	blob, err := compressSession(raw)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "wacli.db")
	err = ImportSession(ctx, blob, dest)
	if err == nil {
		t.Fatal("a session with no paired device was accepted")
	}
	if !strings.Contains(err.Error(), "no paired device") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestSessionExportRefusesAStoreWithNoSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "plain.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()

	if _, err := ExportSession(ctx, path); err == nil {
		t.Fatal("exporting an unpaired store should fail loudly")
	}
	if err := SessionSanityCheck(ctx, path); err == nil {
		t.Fatal("SanityCheck should reject an unpaired store")
	}
}

func TestSessionExportRefusesAMissingStore(t *testing.T) {
	if _, err := ExportSession(context.Background(), filepath.Join(t.TempDir(), "nope.db")); err == nil {
		t.Fatal("a missing store should fail")
	}
}
