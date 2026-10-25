package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// claudeDesktopDataFixture writes a Claude Desktop data folder whose session
// list holds records, laid out as the packaged Windows app keeps it.
func claudeDesktopDataFixture(t *testing.T, records ...map[string]any) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "claude_pkg", claudeDesktopSessionsDir, "org", "account")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i, record := range records {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "local_"+string(rune('a'+i))+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDesktopScopeIncludesClaudeAndExcludesAutomatedSources(t *testing.T) {
	opts := checkpointOptions(t)
	sidebarFixture(t, opts.CodexHome)
	opts.ClaudeHome = t.TempDir()
	opts.DesktopOnly = true
	opts.ClaudeDesktopData = claudeDesktopDataFixture(t, map[string]any{"cliSessionId": "claude"})
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
	// "child" is a CLI run started inside a Desktop session: it inherits the
	// Desktop entrypoint, but the app does not list it.
	for id, entrypoint := range map[string]string{"claude": "claude-desktop", "child": "claude-desktop", "vscode": "claude-vscode", "sdk": "sdk-cli", "cli": "cli", "unknown": ""} {
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
	// The legacy Desktop-only scope keeps VS Code side-panel sessions out.
	if first.Scope != "codex-claude-desktop" || first.Created != 2 || first.FilteredInternal != 6 {
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
		"vscode": `{"sessionId":"vscode","entrypoint":"claude-vscode","isSidechain":true}`,
		"agent":  `{"sessionId":"agent","entrypoint":"claude-desktop","agentId":"agent-1"}`,
		"broken": `{"sessionId":"broken","entrypoint":"claude-desktop"}` + "\n{partial",
	} {
		path := filepath.Join(t.TempDir(), name+".jsonl")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		listed := map[string]bool{"fork": true, "child": true, "agent": true, "broken": true, "vscode": true}
		id, err := claudeDesktopSessionID(path, listed, listed, true, nil)
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
	for id, entrypoint := range map[string]string{"desktop": "claude-desktop", "vscode": "claude-vscode", "zed": "sdk-ts", "script": "sdk-ts", "archived": "sdk-ts", "terminal": "cli", "vscode-archived": "claude-vscode"} {
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
	desktopData := claudeDesktopDataFixture(t, map[string]any{"cliSessionId": "desktop"})
	// The extension keeps the side-panel sessions archived in VS Code's state.
	vscodeState := filepath.Join(root, "vscode", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(vscodeState), 0700); err != nil {
		t.Fatal(err)
	}
	state, err := sql.Open("sqlite", vscodeState)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err = state.Exec("CREATE TABLE ItemTable(key TEXT UNIQUE, value BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err = state.Exec("INSERT INTO ItemTable VALUES(?, ?)", vscodeClaudeStateKey, `{"hiddenSessionIds":["vscode-archived"]}`); err != nil {
		t.Fatal(err)
	}
	export := func(name string) Result {
		t.Helper()
		result, err := Export(context.Background(), Options{
			ClaudeHome: claudeHome, ZedDB: zedDB, ClaudeDesktopData: desktopData, VSCodeState: vscodeState, Output: filepath.Join(root, name), AllRepos: true,
			Sources: &SourceSelection{ClaudeCode: true}, DeviceID: "device:test", DeviceName: "test",
			StabilityWindow: -time.Second, GUIOnly: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := export("without-zed"); result.Created != 2 || result.FilteredInternal != 5 {
		t.Fatalf("a missing Zed database should leave only Desktop and VS Code sessions: %+v", result)
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
	if result := export("with-zed"); result.Created != 3 || result.FilteredInternal != 4 {
		t.Fatalf("only listed Claude Agent SDK sessions should join Desktop and VS Code sessions: %+v", result)
	}
}

func TestClaudeDesktopListDecidesDesktopMembership(t *testing.T) {
	root := claudeDesktopDataFixture(t,
		map[string]any{"cliSessionId": "current", "priorCliSessionIds": []string{"before-rewind"}},
		map[string]any{"cliSessionId": "archived", "isArchived": true},
		map[string]any{"cliSessionId": ""},
	)
	if err := os.WriteFile(filepath.Join(root, "claude_pkg", claudeDesktopSessionsDir, "org", "account", "archived-sessions.idx"), []byte(`{"v":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	ids, dirs, err := readClaudeDesktopIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || len(ids) != 2 || !ids["current"] || !ids["before-rewind"] {
		t.Fatalf("listed sessions: %v from %v", ids, dirs)
	}
	// Claude Desktop keeps its sessions at the top of an unpackaged data folder.
	flat := t.TempDir()
	if err := os.MkdirAll(filepath.Join(flat, claudeDesktopSessionsDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flat, claudeDesktopSessionsDir, "local_x.json"), []byte(`{"cliSessionId":"flat"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if ids, _, err := readClaudeDesktopIDs(flat); err != nil || !ids["flat"] {
		t.Fatalf("flat layout: %v %v", ids, err)
	}
	if _, _, err := readClaudeDesktopIDs(t.TempDir()); err == nil {
		t.Fatal("a named data folder without sessions was accepted")
	}
	broken := claudeDesktopDataFixture(t, map[string]any{"cliSessionId": "x"})
	if err := os.WriteFile(filepath.Join(broken, "claude_pkg", claudeDesktopSessionsDir, "org", "account", "local_z.json"), []byte("{partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readClaudeDesktopIDs(broken); err == nil {
		t.Fatal("an unreadable session record was accepted")
	}
}
