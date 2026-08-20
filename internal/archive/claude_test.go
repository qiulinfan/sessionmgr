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

func TestParseClaudeSessionRejectsInvalidGraphsAndBusyTail(t *testing.T) {
	const id = "22222222-2222-2222-2222-222222222222"
	cwd := t.TempDir()
	validRoot := claudeUserRecord(id, "root", "", "2026-08-20T01:00:00Z", cwd, []any{map[string]any{"type": "text", "text": "hello"}})
	tests := []struct {
		name    string
		records []map[string]any
	}{
		{"mismatched ID", []map[string]any{{"type": "user", "uuid": "root", "sessionId": "other", "timestamp": "2026-08-20T01:00:00Z", "message": map[string]any{"role": "user", "content": "hello"}}}},
		{"duplicate UUID", []map[string]any{validRoot, validRoot}},
		{"missing parent", []map[string]any{validRoot, claudeAssistantRecord(id, "a", "missing", "m", "2026-08-20T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "answer"}})}},
		{"multiple roots", []map[string]any{validRoot, claudeUserRecord(id, "root-2", "", "2026-08-20T01:00:01Z", cwd, []any{map[string]any{"type": "text", "text": "other"}})}},
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
	if err != nil || !bytes.Contains(document, []byte("Exported from Claude Code")) || !bytes.Contains(document, []byte("renderer_version: 8")) {
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
