package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sidebarFixture(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE threads(id TEXT, source TEXT, archived INTEGER)"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		id, source string
		archived   int
	}{{"desktop", "vscode", 0}, {"older", "vscode", 0}, {"cli", "cli", 0}, {"exec", "exec", 0}, {"child", `{"subagent":{}}`, 0}, {"archived", "vscode", 1}} {
		if _, err = db.Exec("INSERT INTO threads VALUES(?,?,?)", r.id, r.source, r.archived); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSidebarExportFiltersBeforeParsingAndUsesSeparateCheckpoint(t *testing.T) {
	opts := checkpointOptions(t)
	sidebarFixture(t, opts.CodexHome)
	var titles []string
	for _, id := range []string{"desktop", "older", "cli", "exec", "child", "archived"} {
		writeSessionFixture(t, opts.CodexHome, id, "https://github.com/example/sidebar.git", id+" answer")
		ageSource(t, incrementalFixturePath(opts, id), 3*time.Hour)
		titles = append(titles, titleLine(id, id+" conversation", "2026-08-05T02:00:00Z"))
	}
	writeTitles(t, opts.CodexHome, titles...)
	first := mustExport(t, opts)
	if first.Created != 6 {
		t.Fatalf("baseline: %+v", first)
	}
	opts.SidebarOnly = true
	scoped := mustExport(t, opts)
	if scoped.Scope != "codex-sidebar" || scoped.Matched != 2 || scoped.ScannedSources != 2 || scoped.FilteredInternal != 4 {
		t.Fatalf("sidebar scope: %+v", scoped)
	}
	// An incomplete CLI log must not enter the parser or the pending queue.
	if err := os.WriteFile(incrementalFixturePath(opts, "exec"), []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"exec\"}}\n{partial"), 0600); err != nil {
		t.Fatal(err)
	}
	next := mustExport(t, opts)
	if next.Busy != 0 || next.Incomplete != 0 || next.ScannedSources != 0 || next.IgnoredUnchanged != 2 {
		t.Fatalf("automatic source leaked: %+v", next)
	}
	opts.IncludeArchived = true
	if _, err := Export(context.Background(), opts); err == nil {
		t.Fatal("conflicting archived scope accepted")
	}
}

func TestSidebarMissingDatabaseFailsBeforeWritingArchive(t *testing.T) {
	root := t.TempDir()
	opts := testExportOptions(filepath.Join(root, "codex"), filepath.Join(root, "export"))
	opts.SidebarOnly = true
	if _, err := Export(context.Background(), opts); err == nil {
		t.Fatal("missing database did not fail closed")
	}
	if _, err := os.Stat(opts.Output); !os.IsNotExist(err) {
		t.Fatal("archive was created without a verified scope")
	}
}

func TestSidebarLatestUnsupportedDatabaseDoesNotFallBack(t *testing.T) {
	home := t.TempDir()
	sidebarFixture(t, home)
	db, err := sql.Open("sqlite", filepath.Join(home, "state_6.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE unsupported(id TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := readCodexSidebarIDs(context.Background(), home); err == nil {
		t.Fatal("unsupported latest inventory silently fell back")
	}
}
