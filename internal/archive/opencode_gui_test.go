package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCodeGUIListScopeAndBroadExport(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "opencode.db")
	project := filepath.Join(root, "project")
	makeOpenCodeFixture(t, database, project)
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`ALTER TABLE session ADD COLUMN time_archived INTEGER`,
		`INSERT INTO session SELECT 'ses-automatic', '/other-cli-project', title,version,NULL,time_created,time_updated,NULL FROM session WHERE id='ses-primary'`,
		`INSERT INTO session SELECT 'ses-archived', directory,title,version,NULL,time_created,time_updated,1 FROM session WHERE id='ses-primary'`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	home := filepath.Join(root, "gui")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"server": map[string]any{"projects": map[string]any{"local": []any{map[string]string{"worktree": project}}, "remote": []any{map[string]string{"worktree": "/other-cli-project"}}}}}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(home, "opencode.global.dat"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ids, err := ReadOpenCodeGUIIDs(context.Background(), database, home)
	if err != nil || len(ids) != 1 || !ids["ses-primary"] {
		t.Fatalf("wrong GUI membership: %v %v", ids, err)
	}
	opts := Options{OpenCodeDB: database, OpenCodeGUIHome: home, GUIOnly: true, Output: filepath.Join(root, "out"), Sources: &SourceSelection{OpenCode: true}, AllRepos: true, IncludeNonGit: true, DeviceID: "device:test", DeviceName: "fixture"}
	scoped := mustExport(t, opts)
	if scoped.Scope != "gui-conversations" || scoped.Created != 1 || scoped.FilteredInternal != 3 {
		t.Fatalf("scope leaked: %+v", scoped)
	}
	opts.GUIOnly = false
	broad := mustExport(t, opts)
	if broad.Scope != "" || broad.Sources != 4 {
		t.Fatalf("broad interface lost: %+v", broad)
	}
	// Membership is checked even when incremental timestamps would be old.
	state["server"] = map[string]any{"projects": map[string]any{"local": []any{}}}
	raw, _ = json.Marshal(state)
	os.WriteFile(filepath.Join(home, "opencode.global.dat"), raw, 0600)
	ids, err = ReadOpenCodeGUIIDs(context.Background(), database, home)
	if err != nil || len(ids) != 0 {
		t.Fatalf("removed projects retained: %v %v", ids, err)
	}
}

func TestOpenCodeGUIUnknownInventoryFailsBeforeArchiveWrites(t *testing.T) {
	root := t.TempDir()
	opts := Options{GUIOnly: true, Sources: &SourceSelection{OpenCode: true}, OpenCodeDB: filepath.Join(root, "missing.db"), OpenCodeGUIHome: root, Output: filepath.Join(root, "out")}
	if _, err := Export(context.Background(), opts); err == nil {
		t.Fatal("missing GUI inventory accepted")
	}
	if _, err := os.Stat(opts.Output); !os.IsNotExist(err) {
		t.Fatal("archive created without membership")
	}
}
