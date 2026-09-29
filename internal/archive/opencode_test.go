package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeOpenCodeFixture(t *testing.T, path, cwd string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL, parent_id TEXT, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).UnixMilli()
	for _, row := range []struct{ id, parent string }{{"ses-primary", ""}, {"ses-child", "ses-primary"}} {
		var parent any
		if row.parent != "" {
			parent = row.parent
		}
		if _, err := db.Exec(`INSERT INTO session VALUES (?,?,?,?,?,?,?)`, row.id, cwd, "Review work", "1.18.2", parent, stamp, stamp+3000); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id, session, role string
		offset            int64
	}{
		{"msg-user", "ses-primary", "user", 1000}, {"msg-assistant", "ses-primary", "assistant", 2000},
		{"msg-child", "ses-child", "user", 1000},
	} {
		data := fmt.Sprintf(`{"role":%q,"time":{"created":%d}}`, row.role, stamp+row.offset)
		if row.role == "assistant" {
			data = fmt.Sprintf(`{"role":%q,"time":{"created":%d,"completed":%d}}`, row.role, stamp+row.offset, stamp+row.offset+500)
		}
		if _, err := db.Exec(`INSERT INTO message VALUES (?,?,?,?,?)`, row.id, row.session, stamp+row.offset, stamp+row.offset, data); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id, message, session, data string
		offset                     int64
	}{
		{"part-user", "msg-user", "ses-primary", `{"type":"text","text":"Can you review this?"}`, 1000},
		{"part-tool", "msg-assistant", "ses-primary", `{"type":"tool","tool":"read","state":{"status":"completed","output":"not rendered"}}`, 2000},
		{"part-reason", "msg-assistant", "ses-primary", `{"type":"reasoning","text":"private reasoning"}`, 2001},
		{"part-answer", "msg-assistant", "ses-primary", `{"type":"text","text":"Review complete."}`, 2002},
		{"part-child", "msg-child", "ses-child", `{"type":"text","text":"delegated work"}`, 1000},
	} {
		if _, err := db.Exec(`INSERT INTO part VALUES (?,?,?,?,?,?)`, row.id, row.message, row.session, stamp+row.offset, stamp+row.offset, row.data); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenCodeReadOnlySnapshotAndExport(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "opencode", "opencode.db")
	makeOpenCodeFixture(t, path, cwd)
	read, err := readOpenCodeSessions(context.Background(), path, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if read.sources != 2 || read.busy != 0 || read.skipped != 0 || len(read.sessions) != 2 {
		t.Fatalf("unexpected OpenCode snapshot: %+v", read)
	}
	byID := map[string]Session{}
	for _, session := range read.sessions {
		byID[session.ID] = session
	}
	primary, child := byID["ses-primary"], byID["ses-child"]
	if primary.UserMessages != 1 || primary.AssistantMessages != 1 || primary.ToolCallCount != 1 ||
		len(primary.Messages) != 2 || primary.Messages[1].Text != "Review complete." || child.ExcludeReason != "subagent" {
		t.Fatalf("wrong OpenCode conversation extraction: %+v", read.sessions)
	}
	result, err := Export(context.Background(), Options{
		OpenCodeDB: path, Output: filepath.Join(root, "archive"), AllRepos: true, IncludeNonGit: true,
		Sources: &SourceSelection{OpenCode: true}, DeviceID: "device:test", DeviceName: "test",
		StabilityWindow: -time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources != 2 || result.Created != 1 || result.FilteredInternal != 1 || len(result.Changes) != 1 ||
		result.Changes[0].Harness != harnessOpenCode {
		t.Fatalf("unexpected OpenCode export: %+v", result)
	}
	document, err := os.ReadFile(result.Changes[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(document), "Review complete.") || strings.Contains(string(document), "private reasoning") ||
		strings.Contains(string(document), "not rendered") {
		t.Fatalf("OpenCode document included internal content: %s", document)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("native OpenCode database was changed or removed: %v", err)
	}
}

func TestOpenCodeRejectsUnsafeDatabasePath(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "opencode.db")
	if err := os.WriteFile(file, []byte("not SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.db")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readOpenCodeSessions(context.Background(), link, 0); err == nil {
		t.Fatal("accepted symlinked database")
	}
}

func TestOpenCodeLeavesIncompleteAssistantForNextExport(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "opencode.db")
	makeOpenCodeFixture(t, path, cwd)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE message SET data = '{"role":"assistant","time":{"created":1789905602000}}' WHERE id = 'msg-assistant'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	result, err := readOpenCodeSessions(context.Background(), path, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.busy != 1 || result.skipped != 0 || len(result.warnings) != 0 || len(result.sessions) != 1 {
		t.Fatalf("incomplete assistant was published or treated as corruption: %+v", result)
	}
}

func TestOpenCodeRunningTurnExportsStablePreviousConversation(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, cwd, "init")
	runGit(t, cwd, "remote", "add", "origin", "https://github.com/example/opencode-running.git")
	path := filepath.Join(root, "opencode.db")
	makeOpenCodeFixture(t, path, cwd)
	opts := Options{
		OpenCodeDB: path, Output: filepath.Join(root, "archive"), AllRepos: true, IncludeNonGit: true,
		Sources: &SourceSelection{OpenCode: true}, DeviceID: "device:test", DeviceName: "test", StabilityWindow: -time.Second,
	}
	previous, err := Export(context.Background(), opts)
	if err != nil || previous.Created != 1 {
		t.Fatalf("completed OpenCode baseline did not export: %+v / %v", previous, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stamp := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).UnixMilli()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO message VALUES (?,?,?,?,?)`, []any{"msg-user2", "ses-primary", stamp + 4000, stamp + 4000, fmt.Sprintf(`{"role":"user","time":{"created":%d}}`, stamp+4000)}},
		{`INSERT INTO part VALUES (?,?,?,?,?,?)`, []any{"part-user2", "msg-user2", "ses-primary", stamp + 4000, stamp + 4000, `{"type":"text","text":"current question"}`}},
		{`INSERT INTO message VALUES (?,?,?,?,?)`, []any{"msg-assistant2", "ses-primary", stamp + 5000, stamp + 5000, fmt.Sprintf(`{"role":"assistant","time":{"created":%d}}`, stamp+5000)}},
		{`INSERT INTO part VALUES (?,?,?,?,?,?)`, []any{"part-assistant2", "msg-assistant2", "ses-primary", stamp + 5000, stamp + 5000, `{"type":"text","text":"partial answer"}`}},
		{`UPDATE session SET time_updated = ? WHERE id = 'ses-primary'`, []any{stamp + 6000}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	active, err := Export(context.Background(), opts)
	if err != nil || active.Created != 0 || active.Unchanged != 1 || active.Busy != 1 {
		t.Fatalf("OpenCode active tail changed the completed archive: before=%+v after=%+v / %v", previous, active, err)
	}
	if _, err := db.Exec(`UPDATE part SET data = '{"type":"text","text":"more partial output"}' WHERE id = 'part-assistant2'`); err != nil {
		t.Fatal(err)
	}
	again, err := Export(context.Background(), opts)
	if err != nil || again.Created != 0 || again.Unchanged != 1 {
		t.Fatalf("OpenCode in-progress text caused a new version: %+v / %v", again, err)
	}
	if _, err := db.Exec(`UPDATE message SET data = ? WHERE id = 'msg-assistant2'`, fmt.Sprintf(`{"role":"assistant","finish":"tool-calls","time":{"created":%d,"completed":%d}}`, stamp+5000, stamp+6000)); err != nil {
		t.Fatal(err)
	}
	toolsDone, err := Export(context.Background(), opts)
	if err != nil || toolsDone.Created != 0 || toolsDone.Unchanged != 1 || toolsDone.Busy != 1 {
		t.Fatalf("OpenCode tool-call settlement was treated as a final answer: %+v / %v", toolsDone, err)
	}
	if _, err := db.Exec(`UPDATE message SET data = ? WHERE id = 'msg-assistant2'`, fmt.Sprintf(`{"role":"assistant","finish":"stop","time":{"created":%d,"completed":%d}}`, stamp+5000, stamp+6000)); err != nil {
		t.Fatal(err)
	}
	finished, err := Export(context.Background(), opts)
	if err != nil || finished.Created != 1 || len(finished.Changes) != 1 {
		t.Fatalf("completed OpenCode turn did not export: %+v / %v", finished, err)
	}
	document, err := os.ReadFile(finished.Changes[0].Path)
	if err != nil || !strings.Contains(string(document), "more partial output") {
		t.Fatalf("completed OpenCode answer missing: %v", err)
	}
}
