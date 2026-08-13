// Package wasession moves a WhatsApp session between the phone and the server.
//
// What travels is whatsmeow's state and nothing else: the device identity, the Signal ratchet and
// prekeys, and the app-state sync keys. Message history stays where it is — a call does not need
// it, it is the bulk of the database, and it is the most sensitive thing we could carry.
//
// The blob is deliberately a SQLite database rather than a bespoke format. whatsmeow owns this
// schema and changes it between versions; anything that re-encodes it by hand becomes a migration
// liability the moment wacli's dependency moves.
package wa

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// sessionTablePrefix is what whatsmeow names its tables. Everything else in the file is wacli's own —
// messages, chats, triggers — and stays behind.
const sessionTablePrefix = "whatsmeow_"

// Export reads a wacli store and returns a compressed database holding only the session state.
func ExportSession(ctx context.Context, dbPath string) ([]byte, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("session store: %w", err)
	}

	tmp, err := os.MkdirTemp("", "wasession-export-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	outPath := filepath.Join(tmp, "session.db")

	dst, err := openSessionDB(outPath)
	if err != nil {
		return nil, err
	}
	defer dst.Close()

	// Read-only, and immutable so a store still open elsewhere cannot be disturbed by the copy.
	src := fmt.Sprintf("file:%s?mode=ro", dbPath)
	if _, err := dst.ExecContext(ctx, `ATTACH DATABASE ? AS src`, src); err != nil {
		return nil, fmt.Errorf("attach source: %w", err)
	}
	defer dst.ExecContext(ctx, `DETACH DATABASE src`)

	objects, err := sessionSchemaOf(ctx, dst)
	if err != nil {
		return nil, err
	}
	if len(objects.tables) == 0 {
		return nil, fmt.Errorf("no %s tables found — is this a paired wacli store?", sessionTablePrefix)
	}

	for _, t := range objects.tables {
		if _, err := dst.ExecContext(ctx, t.create); err != nil {
			return nil, fmt.Errorf("create %s: %w", t.name, err)
		}
		stmt := fmt.Sprintf("INSERT INTO main.%q SELECT * FROM src.%q", t.name, t.name)
		if _, err := dst.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("copy %s: %w", t.name, err)
		}
	}
	// Indexes after the rows: building them once at the end is cheaper than maintaining them
	// through every insert, and on a session store the difference is not academic.
	for _, idx := range objects.indexes {
		if _, err := dst.ExecContext(ctx, idx); err != nil {
			return nil, fmt.Errorf("create index: %w", err)
		}
	}

	if _, err := dst.ExecContext(ctx, `DETACH DATABASE src`); err != nil {
		return nil, fmt.Errorf("detach: %w", err)
	}
	if err := dst.Close(); err != nil {
		return nil, fmt.Errorf("close export: %w", err)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		return nil, err
	}
	return compressSession(raw)
}

// Import writes an exported session to dbPath, replacing whatever is there.
//
// Replacing rather than merging is the only safe choice: two versions of a ratchet cannot be
// reconciled, and the whole point of the lease is that the incoming copy is the authoritative one.
func ImportSession(ctx context.Context, blob []byte, dbPath string) error {
	raw, err := decompressSession(blob)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return err
	}

	// Write beside the target and rename: a half-written session store is worse than none, because
	// it looks paired and is not.
	tmpPath := dbPath + ".incoming"
	if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
		return err
	}
	if err := verifySession(ctx, tmpPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// SQLite's sidecar files belong to the database being replaced, not the new one.
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(dbPath + suffix)
	}
	return os.Rename(tmpPath, dbPath)
}

// verify opens an imported file and checks it holds a device row.
//
// An empty but well-formed database would import cleanly and then fail at connect time as
// "not paired", which points at the wrong thing entirely.
func verifySession(ctx context.Context, path string) error {
	db, err := openSessionDB(path)
	if err != nil {
		return fmt.Errorf("imported session is unreadable: %w", err)
	}
	defer db.Close()

	var n int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type='table' AND name LIKE ?`, sessionTablePrefix+"%").Scan(&n)
	if err != nil {
		return fmt.Errorf("imported session is unreadable: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("imported session holds no %s tables", sessionTablePrefix)
	}

	var devices int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM whatsmeow_device`).Scan(&devices); err != nil {
		return fmt.Errorf("imported session has no device table: %w", err)
	}
	if devices == 0 {
		return fmt.Errorf("imported session contains no paired device")
	}
	return nil
}

// Tables reports which session tables a blob carries, for diagnostics.
func SessionTables(ctx context.Context, blob []byte) ([]string, error) {
	raw, err := decompressSession(blob)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "wasession-peek-*.db")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		return nil, err
	}
	tmp.Close()

	db, err := openSessionDB(tmp.Name())
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE type='table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

type sessionSchema struct {
	tables  []sessionTable
	indexes []string
}

type sessionTable struct {
	name   string
	create string
}

// schemaOf lists the session tables and indexes in the attached source.
func sessionSchemaOf(ctx context.Context, db *sql.DB) (sessionSchema, error) {
	var s sessionSchema

	rows, err := db.QueryContext(ctx, `SELECT type, name, sql FROM src.sqlite_master
		WHERE name LIKE ? AND sql IS NOT NULL ORDER BY type DESC, name`, sessionTablePrefix+"%")
	if err != nil {
		return s, fmt.Errorf("read source schema: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var kind, name, create string
		if err := rows.Scan(&kind, &name, &create); err != nil {
			return s, err
		}
		switch kind {
		case "table":
			s.tables = append(s.tables, sessionTable{name: name, create: create})
		case "index":
			s.indexes = append(s.indexes, create)
		}
	}
	return s, rows.Err()
}

func openSessionDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite is single-writer, and a pool here buys contention rather than speed.
	db.SetMaxOpenConns(1)
	return db, nil
}

func compressSession(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decompressSession(blob []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, fmt.Errorf("session blob is not gzip: %w", err)
	}
	defer zr.Close()

	// Bounded: an attacker-supplied blob should not be able to exhaust a Lambda's memory by
	// decompressing to something enormous.
	const maxSize = 256 << 20
	raw, err := io.ReadAll(io.LimitReader(zr, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("decompress session: %w", err)
	}
	if len(raw) > maxSize {
		return nil, fmt.Errorf("session blob decompresses beyond %d bytes", maxSize)
	}
	if !bytes.HasPrefix(raw, []byte("SQLite format 3\x00")) {
		return nil, fmt.Errorf("session blob is not a SQLite database")
	}
	return raw, nil
}

// SanityCheck reports whether a path looks like a wacli store worth exporting.
func SessionSanityCheck(ctx context.Context, dbPath string) error {
	db, err := openSessionDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type='table' AND name LIKE ?`, sessionTablePrefix+"%").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s holds no %s tables", filepath.Base(dbPath), sessionTablePrefix)
	}
	return nil
}

// RemoveSessionStore erases a session and its SQLite sidecars.
//
// Used after a session has been handed to another machine. Leaving the copy behind is what makes a
// later accidental reconnect possible, and that reconnect is the divergence the handover exists to
// avoid.
func RemoveSessionStore(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm", path + ".incoming"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", filepath.Base(p), err)
		}
	}
	return nil
}
