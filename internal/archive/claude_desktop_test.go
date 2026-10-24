package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDesktopScopeIncludesClaudeAndExcludesAutomatedSources(t *testing.T) {
	opts := checkpointOptions(t)
	sidebarFixture(t, opts.CodexHome)
	opts.ClaudeHome = t.TempDir()
	opts.DesktopOnly = true
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "remote", "add", "origin", "https://github.com/example/desktop.git")
	for _, id := range []string{"desktop", "exec"} {
		writeSessionFixture(t, opts.CodexHome, id, "https://github.com/example/desktop.git", id+" answer")
	}
	project := filepath.Join(opts.ClaudeHome, "projects", "fixture")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	for id, entrypoint := range map[string]string{"claude": "claude-desktop", "sdk": "sdk-cli", "cli": "cli", "unknown": ""} {
		u := claudeUserRecord(id, "u", "", "2026-08-20T01:00:00Z", workspace, []any{map[string]any{"type": "text", "text": "Archive this"}})
		u["entrypoint"] = entrypoint
		a := claudeAssistantRecord(id, "a", "u", "m", "2026-08-20T01:00:01Z", workspace, []any{map[string]any{"type": "text", "text": "Done"}})
		a["entrypoint"] = entrypoint
		a["message"].(map[string]any)["stop_reason"] = "end_turn"
		if err := os.WriteFile(filepath.Join(project, id+".jsonl"), marshalClaudeRecords(t, []map[string]any{u, a}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first := mustExport(t, opts)
	if first.Scope != "codex-claude-desktop" || first.Created != 2 || first.FilteredInternal != 4 {
		t.Fatalf("scope leaked or omitted sources: %+v", first)
	}
	second := mustExport(t, opts)
	if second.Created != 0 || second.Busy != 0 {
		t.Fatalf("repeat: %+v", second)
	}
	// Switching back to the existing Codex-only option preserves that interface.
	opts.DesktopOnly = false
	opts.SidebarOnly = true
	onlyCodex := mustExport(t, opts)
	if onlyCodex.Scope != "codex-sidebar" || onlyCodex.Matched != 1 {
		t.Fatalf("compatibility: %+v", onlyCodex)
	}
	opts.DesktopOnly = true
	if _, err := Export(context.Background(), opts); err == nil {
		t.Fatal("conflicting scopes accepted")
	}
}

func TestClaudeDesktopCannotBeEstablishedByAncestryOrSubagent(t *testing.T) {
	for name, raw := range map[string]string{
		"fork":   `{"sessionId":"ancestor","entrypoint":"claude-desktop"}` + "\n" + `{"sessionId":"fork","entrypoint":"sdk-cli"}`,
		"child":  `{"sessionId":"child","entrypoint":"claude-desktop","isSidechain":true}`,
		"agent":  `{"sessionId":"agent","entrypoint":"claude-desktop","agentId":"agent-1"}`,
		"broken": `{"sessionId":"broken","entrypoint":"claude-desktop"}` + "\n{partial",
	} {
		path := filepath.Join(t.TempDir(), name+".jsonl")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		id, err := claudeDesktopSessionID(path, map[string]bool{"fork": true, "child": true, "agent": true, "broken": true})
		if err != nil || id != "" {
			t.Fatalf("%s accepted: %q %v", name, id, err)
		}
	}
}

func TestGUIOnlyClaudeAddsSDKSessionsListedInTheZedSidebar(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "work")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "init")
	runGit(t, workspace, "remote", "add", "origin", "https://github.com/example/zed.git")
	claudeHome := filepath.Join(root, "claude")
	project := filepath.Join(claudeHome, "projects", "fixture")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	for id, entrypoint := range map[string]string{"desktop": "claude-desktop", "zed": "sdk-ts", "script": "sdk-ts", "archived": "sdk-ts", "terminal": "cli"} {
		u := claudeUserRecord(id, "u", "", "2026-10-24T01:00:00Z", workspace, []any{map[string]any{"type": "text", "text": "Hello"}})
		u["entrypoint"] = entrypoint
		a := claudeAssistantRecord(id, "a", "u", "m", "2026-10-24T01:00:01Z", workspace, []any{map[string]any{"type": "text", "text": "Hi"}})
		a["entrypoint"] = entrypoint
		a["message"].(map[string]any)["stop_reason"] = "end_turn"
		if err := os.WriteFile(filepath.Join(project, id+".jsonl"), marshalClaudeRecords(t, []map[string]any{u, a}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	zedDB := filepath.Join(root, "zed", "db.sqlite")
	export := func(name string) Result {
		t.Helper()
		result, err := Export(context.Background(), Options{
			ClaudeHome: claudeHome, ZedDB: zedDB, Output: filepath.Join(root, name), AllRepos: true,
			Sources: &SourceSelection{ClaudeCode: true}, DeviceID: "device:test", DeviceName: "test",
			StabilityWindow: -time.Second, GUIOnly: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := export("without-zed"); result.Created != 1 || result.FilteredInternal != 4 {
		t.Fatalf("a missing Zed database should leave only Desktop sessions: %+v", result)
	}
	if err := os.MkdirAll(filepath.Dir(zedDB), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", zedDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE sidebar_threads(session_id TEXT, agent_id TEXT, archived INTEGER)"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		id, agent string
		archived  int
	}{{"zed", "claude-acp", 0}, {"archived", "claude-acp", 1}, {"terminal", "claude-acp", 0}, {"script", "OMP", 0}} {
		if _, err = db.Exec("INSERT INTO sidebar_threads VALUES(?,?,?)", r.id, r.agent, r.archived); err != nil {
			t.Fatal(err)
		}
	}
	if result := export("with-zed"); result.Created != 2 || result.FilteredInternal != 3 {
		t.Fatalf("only listed Claude Agent SDK sessions should join Desktop sessions: %+v", result)
	}
}
