package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveClaudeHomePrecedence(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "configured")
	environment := filepath.Join(t.TempDir(), "environment")
	t.Setenv("CLAUDE_CONFIG_DIR", environment)

	resolved, err := resolveClaudeHome("")
	if err != nil || resolved != environment {
		t.Fatalf("environment Claude home was not resolved: %q, %v", resolved, err)
	}
	resolved, err = resolveClaudeHome(configured)
	if err != nil || resolved != configured {
		t.Fatalf("explicit Claude home did not win: %q, %v", resolved, err)
	}
}

func TestDiscoverClaudeSessionsUsesOnlyDirectProjectJSONL(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "projects", "project")
	direct := filepath.Join(project, "11111111-1111-1111-1111-111111111111.jsonl")
	for path, data := range map[string]string{
		direct: "{}\n",
		filepath.Join(project, "11111111-1111-1111-1111-111111111111", "subagents", "agent-a.jsonl"):  "{}\n",
		filepath.Join(project, "11111111-1111-1111-1111-111111111111", "tool-results", "result.json"): "{}",
		filepath.Join(project, "memory", "notes.jsonl"):                                               "{}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := discoverClaudeSessionFiles(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != direct {
		t.Fatalf("Claude discovery escaped the direct project boundary: %v", files)
	}
}

func TestParseClaudeSessionSelectsLatestLeafAndVisibleConversation(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	cwd := t.TempDir()
	records := []map[string]any{
		claudeUserRecord(id, "u1", "", "2026-08-20T01:00:00Z", cwd, []any{
			map[string]any{"type": "text", "text": "Start the work"},
		}),
		{"type": "ai-title", "sessionId": id, "aiTitle": "Generated title"},
		claudeAssistantRecord(id, "a1", "u1", "m1", "2026-08-20T01:00:01Z", cwd, []any{
			map[string]any{"type": "thinking", "thinking": "private", "signature": "hidden"},
		}),
		claudeAssistantRecord(id, "a2", "a1", "m1", "2026-08-20T01:00:02Z", cwd, []any{
			map[string]any{"type": "text", "text": "Visible answer"},
		}),
		claudeUserRecord(id, "alternate", "u1", "2026-08-20T01:00:03Z", cwd, []any{
			map[string]any{"type": "text", "text": "Abandoned rewind branch"},
		}),
		claudeToolResultRecord(id, "tool-result", "a2", "2026-08-20T01:00:04Z", cwd),
		claudeAssistantRecord(id, "a3", "tool-result", "m2", "2026-08-20T01:00:05Z", cwd, []any{
			map[string]any{"type": "tool_use", "id": "tool-2", "name": "Read", "input": map[string]any{"file_path": "secret"}},
		}),
		{
			"type": "user", "uuid": "u2", "parentUuid": "a3", "sessionId": id,
			"timestamp": "2026-08-20T01:00:06Z", "cwd": cwd, "gitBranch": "feature", "version": "2.1.235",
			"userType": "external", "origin": map[string]any{"kind": "human"}, "promptSource": "sdk",
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "<ide_opened_file>internal editor context</ide_opened_file>"},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "aW1hZ2U="}},
				map[string]any{"type": "document", "title": "notes", "source": map[string]any{"type": "text", "media_type": "text/plain", "data": "document body"}},
				map[string]any{"type": "text", "text": "Continue with the files"},
			}},
		},
		{"type": "agent-name", "sessionId": id, "agentName": "Explicit session name"},
		claudeAssistantRecord(id, "a4", "u2", "m3", "2026-08-20T01:00:07Z", cwd, []any{
			map[string]any{"type": "text", "text": "Finished"},
		}),
	}
	raw := marshalClaudeRecords(t, records)
	session, err := parseClaudeSession(raw, id)
	if err != nil {
		t.Fatal(err)
	}
	if session.Harness != harnessClaudeCode || session.ID != id || session.Title != "Explicit session name" || session.ClaudeVersion != "2.1.235" {
		t.Fatalf("unexpected Claude identity/title/version: %+v", session)
	}
	if session.AlternateBranches != 1 || session.FilteredUserInput != 1 || session.ToolCallCount != 1 {
		t.Fatalf("unexpected graph/filter/tool counts: %+v", session)
	}
	if session.UserMessages != 2 || session.AssistantMessages != 2 || len(session.Messages) != 4 {
		t.Fatalf("unexpected visible conversation counts: %+v", session)
	}
	joined := ""
	for _, message := range session.Messages {
		joined += message.Text + "\n"
	}
	for _, forbidden := range []string{"private", "secret", "Abandoned rewind branch", "internal editor context"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Claude projection leaked %q: %s", forbidden, joined)
		}
	}
	for _, visible := range []string{"Start the work", "Visible answer", "Continue with the files", "Finished"} {
		if !strings.Contains(joined, visible) {
			t.Fatalf("Claude projection omitted %q: %s", visible, joined)
		}
	}
	attachments := session.Messages[2].Attachments
	if len(attachments) != 2 || attachments[0].SourceKind != "embedded_data" || attachments[1].SourceKind != "embedded_bytes" || string(attachments[1].Data) != "document body" {
		t.Fatalf("structured Claude attachments were not preserved: %+v", attachments)
	}
	if session.CWD != cwd || session.Branch != "main" || session.CreatedAt.IsZero() || session.LastEventAt.IsZero() {
		t.Fatalf("Claude timeline or workspace metadata missing: %+v", session)
	}
}

func TestParseClaudeSessionRejectsInvalidSelectedPathAndBusyTail(t *testing.T) {
	const id = "22222222-2222-2222-2222-222222222222"
	cwd := t.TempDir()
	validRoot := claudeUserRecord(id, "root", "", "2026-08-20T01:00:00Z", cwd, []any{map[string]any{"type": "text", "text": "hello"}})
	tests := []struct {
		name    string
		records []map[string]any
	}{
		{"missing parent", []map[string]any{validRoot, claudeAssistantRecord(id, "a", "missing", "m", "2026-08-20T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "answer"}})}},
		{"cycle", []map[string]any{
			claudeUserRecord(id, "root", "assistant", "2026-08-20T01:00:00Z", cwd, []any{map[string]any{"type": "text", "text": "hello"}}),
			claudeAssistantRecord(id, "assistant", "root", "m", "2026-08-20T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "answer"}}),
		}},
		{"semantic replay conflict", []map[string]any{
			validRoot,
			claudeAssistantRecord(id, "a", "root", "m", "2026-08-20T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "first"}}),
			claudeAssistantRecord(id, "a", "root", "m", "2026-08-20T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "conflicting"}}),
			claudeUserRecord(id, "last", "a", "2026-08-20T01:00:02Z", cwd, []any{map[string]any{"type": "text", "text": "continue"}}),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseClaudeSession(marshalClaudeRecords(t, test.records), id); err == nil {
				t.Fatalf("invalid Claude graph %q was accepted", test.name)
			}
		})
	}
	complete := marshalClaudeRecords(t, []map[string]any{validRoot})
	if _, err := parseClaudeSession(complete[:len(complete)-3], id); !errors.Is(err, errSourceBusy) {
		t.Fatalf("incomplete Claude JSONL tail was not busy: %v", err)
	}
}

func TestParseClaudeSessionAllowsCopiedAncestryDetachedRootsAndReplay(t *testing.T) {
	const id = "copied-session"
	const ancestorID = "ancestor-session"
	cwd := t.TempDir()
	foreignUser := claudeUserRecord(ancestorID, "foreign-user", "", "2026-08-20T01:00:00Z", cwd, []any{map[string]any{"type": "text", "text": "ancestor request"}})
	foreignAssistant := claudeAssistantRecord(ancestorID, "foreign-assistant", "foreign-user", "foreign-message", "2026-08-20T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "ancestor answer"}})
	replay := claudeAssistantRecord(ancestorID, "foreign-assistant", "foreign-user", "foreign-message", "2026-08-20T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "ancestor answer"}})
	replay["slug"] = "replayed-metadata"
	detachedSystem := map[string]any{"type": "system", "uuid": "detached-system", "parentUuid": nil, "sessionId": ancestorID, "timestamp": "2026-08-20T01:00:01Z"}
	ownedUser := claudeUserRecord(id, "owned-user", "foreign-assistant", "2026-08-20T01:00:02Z", cwd, []any{map[string]any{"type": "text", "text": "fork request"}})
	ownedAssistant := claudeAssistantRecord(id, "owned-assistant", "owned-user", "owned-message", "2026-08-20T01:00:03Z", cwd, []any{map[string]any{"type": "text", "text": "fork answer"}})
	attachmentAfterAnchor := map[string]any{"type": "attachment", "uuid": "after-anchor", "parentUuid": "owned-assistant", "sessionId": id, "timestamp": "2026-08-20T01:00:04Z"}
	staleLastPrompt := map[string]any{"type": "last-prompt", "sessionId": id, "leafUuid": "foreign-user"}

	session, err := parseClaudeSession(marshalClaudeRecords(t, []map[string]any{
		foreignUser, foreignAssistant, replay, detachedSystem, ownedUser, ownedAssistant, attachmentAfterAnchor, staleLastPrompt,
	}), id)
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != id || session.UserMessages != 2 || session.AssistantMessages != 2 || len(session.Messages) != 4 {
		t.Fatalf("copied Claude ancestry was not projected as one current conversation: %+v", session)
	}
	joined := ""
	for _, message := range session.Messages {
		joined += message.Text + "\n"
	}
	for _, expected := range []string{"ancestor request", "ancestor answer", "fork request", "fork answer"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("copied ancestry omitted %q: %s", expected, joined)
		}
	}
	if strings.Contains(joined, "detached") || session.AlternateBranches == 0 {
		t.Fatalf("detached system root was rendered or not counted as alternate: %+v", session)
	}
}

func TestParseClaudeSessionSilentlySkipsBridgeOnlyTranscript(t *testing.T) {
	foreign := claudeUserRecord("ancestor", "foreign-user", "", "2026-08-20T01:00:00Z", t.TempDir(), []any{map[string]any{"type": "text", "text": "ancestor"}})
	session, err := parseClaudeSession(marshalClaudeRecords(t, []map[string]any{foreign}), "current")
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "current" || session.UserMessages != 0 || len(session.Messages) != 0 {
		t.Fatalf("bridge-only Claude transcript was not silently ignored: %+v", session)
	}
}

func TestParseClaudeSessionFiltersSidechainsAndRuntimeInputs(t *testing.T) {
	const id = "33333333-3333-3333-3333-333333333333"
	cwd := t.TempDir()
	root := claudeUserRecord(id, "root", "", "2026-08-20T01:00:00Z", cwd, []any{
		map[string]any{"type": "text", "text": "<command-name>/clear</command-name><command-args></command-args>"},
	})
	root["isSidechain"] = true
	root["agentId"] = "agent-a"
	session, err := parseClaudeSession(marshalClaudeRecords(t, []map[string]any{root}), id)
	if err != nil {
		t.Fatal(err)
	}
	if session.ExcludeReason != "subagent" || session.UserMessages != 0 || session.FilteredUserInput != 1 {
		t.Fatalf("Claude sidechain/runtime input was not filtered: %+v", session)
	}

	legacy := claudeUserRecord(id, "legacy", "", "2026-08-20T01:00:00Z", cwd, []any{
		map[string]any{"type": "text", "text": "[User stopped generation]"},
	})
	delete(legacy, "origin")
	delete(legacy, "promptSource")
	legacySession, err := parseClaudeSession(marshalClaudeRecords(t, []map[string]any{legacy}), id)
	if err != nil {
		t.Fatal(err)
	}
	if legacySession.UserMessages != 0 || legacySession.ExcludeReason != "runtime_context" {
		t.Fatalf("legacy bracketed runtime marker was not filtered: %+v", legacySession)
	}
}

func TestExportClaudeSessionIsOptSelectableIncrementalAndImmutable(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "claude")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "init")
	runGit(t, workspace, "remote", "add", "origin", "https://github.com/example/claude-project.git")
	const id = "44444444-4444-4444-4444-444444444444"
	source := filepath.Join(claudeHome, "projects", "project", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := marshalClaudeRecords(t, []map[string]any{
		claudeUserRecord(id, "u", "", "2026-08-20T01:00:00Z", workspace, []any{map[string]any{"type": "text", "text": "Archive Claude"}}),
		claudeAssistantRecord(id, "a", "u", "m", "2026-08-20T01:00:01Z", workspace, []any{map[string]any{"type": "text", "text": "Archived"}}),
	})
	if err := os.WriteFile(source, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "archive")
	disabled, err := Export(context.Background(), Options{
		ClaudeHome: claudeHome, Output: output, AllRepos: true,
		Sources: &SourceSelection{}, DeviceID: "device:test", DeviceName: "test-device", StabilityWindow: -1,
	})
	if err != nil || disabled.Sources != 0 || disabled.Created != 0 {
		t.Fatalf("disabled Claude source was not optional: %+v, %v", disabled, err)
	}
	opts := Options{
		ClaudeHome: claudeHome, Output: output, AllRepos: true,
		Sources: &SourceSelection{ClaudeCode: true}, DeviceID: "device:test", DeviceName: "test-device", StabilityWindow: -1,
	}
	first, err := Export(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sources != 1 || first.Created != 1 || len(first.Changes) != 1 || first.Changes[0].Harness != harnessClaudeCode {
		t.Fatalf("Claude export did not create one harness change: %+v", first)
	}
	if !strings.Contains(filepath.Base(filepath.Dir(first.Changes[0].Path)), "claude-code--") {
		t.Fatalf("Claude semantic directory lacks its harness prefix: %s", first.Changes[0].Path)
	}
	document, err := os.ReadFile(first.Changes[0].Path)
	if err != nil || !bytes.Contains(document, []byte("Exported from Claude Code")) || !bytes.Contains(document, []byte("renderer_version: 9")) {
		t.Fatalf("Claude Markdown provenance missing: %s, %v", document, err)
	}
	var metadata sessionMetadata
	if err := readMetadata(filepath.Join(filepath.Dir(first.Changes[0].Path), sessionMetadataName), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Harness != harnessClaudeCode || metadata.SessionKey != sessionKey("device:test", harnessClaudeCode, id) {
		t.Fatalf("Claude sidecar identity is invalid: %+v", metadata)
	}
	repeated, err := Export(context.Background(), opts)
	if err != nil || repeated.Unchanged != 1 || repeated.Created != 0 || len(repeated.Changes) != 0 {
		t.Fatalf("repeat Claude export was not a no-op: %+v, %v", repeated, err)
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("Claude source changed during export: %v", err)
	}
	entries, err := List(ListOptions{Output: output})
	if err != nil || len(entries) != 1 || entries[0].Harness != harnessClaudeCode {
		t.Fatalf("Claude list provenance missing: %+v, %v", entries, err)
	}
}

func TestExportRejectsAmbiguousClaudeTranscriptCopies(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "claude")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "init")
	runGit(t, workspace, "remote", "add", "origin", "https://github.com/example/claude-copy.git")
	const id = "77777777-7777-7777-7777-777777777777"
	for project, text := range map[string]string{"project-a": "first copy", "project-b": "second copy"} {
		path := filepath.Join(claudeHome, "projects", project, id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, marshalClaudeRecords(t, []map[string]any{
			claudeUserRecord(id, "user", "", "2026-08-20T01:00:00Z", workspace, []any{map[string]any{"type": "text", "text": text}}),
		}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Export(context.Background(), Options{
		ClaudeHome: claudeHome, Output: filepath.Join(root, "archive"), AllRepos: true,
		Sources: &SourceSelection{ClaudeCode: true}, DeviceID: "device:test", DeviceName: "test-device", StabilityWindow: -1,
	})
	if err == nil || result.Sources != 2 || result.Created != 0 || result.Skipped != 2 || len(result.Changes) != 0 {
		t.Fatalf("ambiguous Claude copies were not fail-closed: %+v, %v", result, err)
	}
	if len(result.Warnings) != 1 || strings.Contains(result.Warnings[0], workspace) {
		t.Fatalf("ambiguous copy warning leaked a workspace or was missing: %+v", result.Warnings)
	}
}

func TestExportClaudeFallsBackToAccessibleAncestryWorkspace(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "claude")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "init")
	runGit(t, workspace, "remote", "add", "origin", "https://github.com/example/claude-workspace.git")
	const id = "88888888-8888-8888-8888-888888888888"
	missingWorkspace := filepath.Join(root, "removed-extension")
	path := filepath.Join(claudeHome, "projects", "project", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, marshalClaudeRecords(t, []map[string]any{
		claudeUserRecord("ancestor", "ancestor-user", "", "2026-08-20T01:00:00Z", workspace, []any{map[string]any{"type": "text", "text": "ancestor"}}),
		claudeUserRecord(id, "owned-user", "ancestor-user", "2026-08-20T01:00:01Z", missingWorkspace, []any{map[string]any{"type": "text", "text": "current"}}),
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Export(context.Background(), Options{
		ClaudeHome: claudeHome, Output: filepath.Join(root, "archive"), AllRepos: true,
		Sources: &SourceSelection{ClaudeCode: true}, DeviceID: "device:test", DeviceName: "test-device", StabilityWindow: -1,
	})
	if err != nil || result.Created != 1 || result.Skipped != 0 || len(result.Changes) != 1 {
		t.Fatalf("accessible Claude ancestry workspace was not used: %+v, %v", result, err)
	}
}

func TestExportSilentlySkipsClaudeBridgeOnlySource(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "claude")
	const id = "99999999-9999-9999-9999-999999999999"
	path := filepath.Join(claudeHome, "projects", "project", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, marshalClaudeRecords(t, []map[string]any{
		claudeUserRecord("ancestor", "foreign-user", "", "2026-08-20T01:00:00Z", t.TempDir(), []any{map[string]any{"type": "text", "text": "bridge only"}}),
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Export(context.Background(), Options{
		ClaudeHome: claudeHome, Output: filepath.Join(root, "archive"), AllRepos: true,
		Sources: &SourceSelection{ClaudeCode: true}, DeviceID: "device:test", DeviceName: "test-device", StabilityWindow: -1,
	})
	if err != nil || result.Sources != 1 || result.Created != 0 || result.Skipped != 0 || result.Matched != 0 || len(result.Warnings) != 0 {
		t.Fatalf("bridge-only Claude source was not silently skipped: %+v, %v", result, err)
	}
}

func TestExportClaudeUnavailableWorkspaceDoesNotExposeAbsolutePath(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "claude")
	missingWorkspace := filepath.Join(root, "deleted-workspace")
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	path := filepath.Join(claudeHome, "projects", "project", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, marshalClaudeRecords(t, []map[string]any{
		claudeUserRecord(id, "user", "", "2026-08-20T01:00:00Z", missingWorkspace, []any{map[string]any{"type": "text", "text": "cannot map workspace"}}),
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Export(context.Background(), Options{
		ClaudeHome: claudeHome, Output: filepath.Join(root, "archive"), AllRepos: true,
		Sources: &SourceSelection{ClaudeCode: true}, DeviceID: "device:test", DeviceName: "test-device", StabilityWindow: -1,
	})
	if err == nil || result.Skipped != 1 || len(result.Warnings) != 1 {
		t.Fatalf("unavailable workspace was not a safe partial export: %+v, %v", result, err)
	}
	if strings.Contains(result.Warnings[0], missingWorkspace) || !strings.Contains(result.Warnings[0], "workspace is unavailable") {
		t.Fatalf("workspace warning leaked an absolute path: %q", result.Warnings[0])
	}
}

func TestClaudeSemanticDirectoriesDisambiguateForkedSessions(t *testing.T) {
	firstSession := Session{ID: "fork-a", Harness: harnessClaudeCode, Title: "Shared title", CreatedAt: parseTimestamp("2026-08-20T01:00:00Z")}
	secondSession := firstSession
	secondSession.ID = "fork-b"
	first := makeSnapshot(Repository{}, firstSession, "device:test", "device")
	second := makeSnapshot(Repository{}, secondSession, "device:test", "device")
	firstDirectory := semanticSessionDirectory(first)
	secondDirectory := semanticSessionDirectory(second)
	if firstDirectory == secondDirectory || !strings.HasPrefix(firstDirectory, "claude-code--") || !strings.HasPrefix(secondDirectory, "claude-code--") {
		t.Fatalf("Claude fork directories are not stable and distinct: %q, %q", firstDirectory, secondDirectory)
	}
}

func TestClaudeCloudOriginIsExcludedEvenWhenTranscriptIsLocal(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "work")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "claude")
	const id = "cloud-collision"
	local := claudeUserRecord(id, "local-u", "", "2026-09-29T01:00:00Z", workspace, []any{map[string]any{"type": "text", "text": "local request"}})
	localAnswer := claudeAssistantRecord(id, "local-a", "local-u", "local-message", "2026-09-29T01:00:01Z", workspace, []any{map[string]any{"type": "text", "text": "local answer"}})
	localAnswer["message"].(map[string]any)["stop_reason"] = "end_turn"
	cloud := claudeUserRecord(id, "cloud-u", "", "2026-09-29T01:00:00Z", workspace, []any{map[string]any{"type": "text", "text": "cloud request"}})
	cloudAnswer := claudeAssistantRecord(id, "cloud-a", "cloud-u", "cloud-message", "2026-09-29T01:00:01Z", workspace, []any{map[string]any{"type": "text", "text": "cloud answer"}})
	cloudAnswer["message"].(map[string]any)["stop_reason"] = "end_turn"
	for project, records := range map[string][]map[string]any{
		"local": {local, localAnswer},
		"cloud": {{"type": "teleported-from", "remoteSessionId": "session_remote"}, cloud, cloudAnswer},
	} {
		path := filepath.Join(home, "projects", project, id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, marshalClaudeRecords(t, records), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Export(context.Background(), Options{
		ClaudeHome: home, Output: filepath.Join(root, "archive"), AllRepos: true, IncludeNonGit: true,
		Sources: &SourceSelection{ClaudeCode: true}, DeviceID: "device:test", DeviceName: "test", StabilityWindow: -1,
	})
	if err != nil || result.Created != 1 || result.FilteredInternal != 1 || len(result.Changes) != 1 {
		t.Fatalf("cloud transcript was exported or local transcript was lost: %+v / %v", result, err)
	}
	document, err := os.ReadFile(result.Changes[0].Path)
	if err != nil || !bytes.Contains(document, []byte("local answer")) || bytes.Contains(document, []byte("cloud answer")) {
		t.Fatalf("wrong Claude source was rendered: %v", err)
	}
}

func TestClaudeLocalRemoteControlAndCloudEntrypoint(t *testing.T) {
	const id = "remote-control-local"
	cwd := t.TempDir()
	user := claudeUserRecord(id, "u", "", "2026-09-29T01:00:00Z", cwd, []any{map[string]any{"type": "text", "text": "local request"}})
	user["entrypoint"] = "cli"
	user["remoteSessionId"] = "remote-control-id"
	answer := claudeAssistantRecord(id, "a", "u", "m", "2026-09-29T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "local answer"}})
	answer["message"].(map[string]any)["stop_reason"] = "end_turn"
	local, err := parseClaudeSession(marshalClaudeRecords(t, []map[string]any{user, answer}), id)
	if err != nil || local.ExcludeReason != "" || local.UserMessages != 1 {
		t.Fatalf("local Remote Control transcript was excluded: %+v / %v", local, err)
	}
	user["entrypoint"] = "cloud"
	cloud, err := parseClaudeSession(marshalClaudeRecords(t, []map[string]any{user, answer}), id)
	if err != nil || cloud.ExcludeReason != "cloud" {
		t.Fatalf("cloud entrypoint was not excluded: %+v / %v", cloud, err)
	}
}

func TestClaudeCloudMarkerSkipsUnsupportedCloudMessageShape(t *testing.T) {
	const id = "cloud-shape"
	session, err := parseClaudeSession(marshalClaudeRecords(t, []map[string]any{
		{"type": "teleported-from", "remoteSessionId": "session_cloud"},
		{"type": "assistant", "sessionId": id, "uuid": "a", "message": 42},
	}), id)
	if err != nil || session.ExcludeReason != "cloud" || len(session.Messages) != 0 {
		t.Fatalf("cloud transcript was parsed as a local conversation: %+v / %v", session, err)
	}
}

func TestClaudeRunningTurnExportsPreviousCompletedTurn(t *testing.T) {
	const id = "running-claude"
	cwd := t.TempDir()
	u1 := claudeUserRecord(id, "u1", "", "2026-09-29T01:00:00Z", cwd, []any{map[string]any{"type": "text", "text": "first question"}})
	a1 := claudeAssistantRecord(id, "a1", "u1", "m1", "2026-09-29T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "first answer"}})
	a1["message"].(map[string]any)["stop_reason"] = "end_turn"
	title := map[string]any{"type": "ai-title", "sessionId": id, "aiTitle": "Saved title"}
	u2 := claudeUserRecord(id, "u2", "a1", "2026-09-29T01:00:02Z", cwd, []any{map[string]any{"type": "text", "text": "current question"}})
	a2 := claudeAssistantRecord(id, "a2", "u2", "m2", "2026-09-29T01:00:03Z", cwd, []any{map[string]any{"type": "text", "text": "partial answer"}})
	a2["message"].(map[string]any)["stop_reason"] = "tool_use"
	previous := marshalClaudeRecords(t, []map[string]any{u1, a1, title})
	for _, raw := range [][]byte{
		marshalClaudeRecords(t, []map[string]any{u1, a1, title, u2}),
		marshalClaudeRecords(t, []map[string]any{u1, a1, title, u2, a2}),
		append(marshalClaudeRecords(t, []map[string]any{u1, a1, title, u2, a2}), []byte(`{"type":"assistant"`)...),
	} {
		session, err := parseClaudeSession(raw, id)
		if err != nil || session.UserMessages != 1 || session.AssistantMessages != 1 ||
			session.RawHash != digestBytes(previous) || len(session.Messages) != 2 || session.Messages[1].Text != "first answer" {
			t.Fatalf("running Claude turn was included: %+v / %v", session, err)
		}
	}
	a2["message"].(map[string]any)["stop_reason"] = "end_turn"
	finished, err := parseClaudeSession(marshalClaudeRecords(t, []map[string]any{u1, a1, title, u2, a2}), id)
	if err != nil || finished.UserMessages != 2 || finished.AssistantMessages != 2 {
		t.Fatalf("completed Claude turn did not appear: %+v / %v", finished, err)
	}
}

func claudeUserRecord(id, uuid, parent, timestamp, cwd string, content []any) map[string]any {
	return map[string]any{
		"type": "user", "uuid": uuid, "parentUuid": nullableParent(parent), "sessionId": id,
		"timestamp": timestamp, "cwd": cwd, "gitBranch": "main", "version": "2.1.235",
		"userType": "external", "origin": map[string]any{"kind": "human"}, "promptSource": "typed",
		"message": map[string]any{"role": "user", "content": content},
	}
}

func claudeAssistantRecord(id, uuid, parent, messageID, timestamp, cwd string, content []any) map[string]any {
	return map[string]any{
		"type": "assistant", "uuid": uuid, "parentUuid": parent, "sessionId": id,
		"timestamp": timestamp, "cwd": cwd, "gitBranch": "main", "version": "2.1.235", "userType": "external",
		"message": map[string]any{"id": messageID, "type": "message", "role": "assistant", "model": "claude-fixture", "content": content},
	}
}

func claudeToolResultRecord(id, uuid, parent, timestamp, cwd string) map[string]any {
	return map[string]any{
		"type": "user", "uuid": uuid, "parentUuid": parent, "sessionId": id,
		"timestamp": timestamp, "cwd": cwd, "version": "2.1.235", "userType": "external",
		"sourceToolAssistantUUID": "assistant-tool", "toolUseResult": map[string]any{"ok": true},
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool-1", "content": "secret tool output"}}},
	}
}

func nullableParent(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func marshalClaudeRecords(t *testing.T, records []map[string]any) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	return output.Bytes()
}
