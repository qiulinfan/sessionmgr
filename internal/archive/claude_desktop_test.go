package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
		id, err := claudeDesktopSessionID(path)
		if err != nil || id != "" {
			t.Fatalf("%s accepted: %q %v", name, id, err)
		}
	}
}
