package archive

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportCoalescesCodexFragmentsForOneNativeSession(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	const (
		id     = "fragmented-session"
		remote = "https://github.com/example/fragment-contract.git"
	)

	firstFragment := writeCodexFragmentFixture(t, codexHome, "rollout-"+id+".jsonl", id, remote,
		"2026-08-05T01:00:00Z", "fragment-one-user", "fragment-one-assistant")
	secondFragment := writeCodexFragmentFixture(t, codexHome, "rollout-"+id+"_continuation-a.jsonl", id, remote,
		"2026-08-05T01:02:00Z", "fragment-two-user", "fragment-two-assistant")
	thirdFragment := writeCodexFragmentFixture(t, codexHome, "rollout-"+id+"_continuation-b.jsonl", id, remote,
		"2026-08-05T01:04:00Z", "fragment-three-user", "fragment-three-assistant")
	writeTitles(t, codexHome, titleLine(id, "Fragment contract", "2026-08-05T01:05:00Z"))

	first, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil {
		t.Fatalf("export fragmented Codex session: %v", err)
	}
	if first.Sources != 3 || first.Matched != 1 || first.Created != 1 || first.Unchanged != 0 || first.Skipped != 0 || len(first.Changes) != 1 {
		t.Fatalf("unexpected first fragmented export: %+v", first)
	}
	change := first.Changes[0]
	if change.SessionID != id || change.Kind != "new" {
		t.Fatalf("unexpected fragment changeset: %+v", change)
	}
	if got, want := filepath.Base(filepath.Dir(change.Path)), "2026-08-05T01-00-00Z--fragment-contract"; got != want {
		t.Fatalf("fragmented session directory = %q, want earliest semantic directory %q", got, want)
	}

	document, err := os.ReadFile(change.Path)
	if err != nil {
		t.Fatalf("read coalesced document: %v", err)
	}
	assertFragmentMarkersOnce(t, string(document),
		"fragment-one-user", "fragment-one-assistant",
		"fragment-two-user", "fragment-two-assistant",
		"fragment-three-user", "fragment-three-assistant",
	)
	if !strings.Contains(string(document), "source_records: 9\n") || !strings.Contains(string(document), "messages: 6\n") {
		t.Fatalf("coalesced document did not aggregate fragment counts:\n%s", document)
	}

	var metadata sessionMetadata
	if err := readMetadata(filepath.Join(filepath.Dir(change.Path), sessionMetadataName), &metadata); err != nil {
		t.Fatalf("read coalesced session metadata: %v", err)
	}
	if metadata.SessionID != id || metadata.CreatedAt != "2026-08-05T01:00:00Z" || !validSHA256(metadata.SourceHash) {
		t.Fatalf("coalesced session metadata is incomplete: %+v", metadata)
	}
	for _, fragment := range []string{firstFragment, secondFragment, thirdFragment} {
		raw, readErr := os.ReadFile(fragment)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if metadata.SourceHash == digestBytes(raw) {
			t.Fatalf("coalesced source hash incorrectly represents only %s", filepath.Base(fragment))
		}
	}

	entries, err := List(ListOptions{Output: output})
	if err != nil || len(entries) != 1 || entries[0].SessionID != id || entries[0].Path != change.Path {
		t.Fatalf("coalesced archive list = %+v, %v", entries, err)
	}

	repeated, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil {
		t.Fatalf("repeat fragmented export: %v", err)
	}
	if repeated.Matched != 1 || repeated.Created != 0 || repeated.Unchanged != 1 || repeated.Skipped != 0 || len(repeated.Changes) != 0 {
		t.Fatalf("fragmented export was not a no-op: %+v", repeated)
	}
	repeatedDocument, err := os.ReadFile(change.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(document, repeatedDocument) {
		t.Fatal("unchanged fragment bundle rewrote the conversation document")
	}

	appendCodexFragmentMessages(t, thirdFragment, "2026-08-05T01:06:00Z", "fragment-three-update-user", "fragment-three-update-assistant")
	updated, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil {
		t.Fatalf("update one Codex fragment: %v", err)
	}
	if updated.Matched != 1 || updated.Created != 1 || updated.Unchanged != 0 || updated.Skipped != 0 || len(updated.Changes) != 1 || updated.Changes[0].Kind != "updated" {
		t.Fatalf("fragment update was not one logical session update: %+v", updated)
	}
	if updated.Changes[0].Path != change.Path {
		t.Fatalf("fragment update changed the semantic path: got %s, want %s", updated.Changes[0].Path, change.Path)
	}
	updatedDocument, err := os.ReadFile(change.Path)
	if err != nil {
		t.Fatal(err)
	}
	assertFragmentMarkersOnce(t, string(updatedDocument),
		"fragment-one-user", "fragment-one-assistant",
		"fragment-two-user", "fragment-two-assistant",
		"fragment-three-user", "fragment-three-assistant",
		"fragment-three-update-user", "fragment-three-update-assistant",
	)
	var updatedMetadata sessionMetadata
	if err := readMetadata(filepath.Join(filepath.Dir(change.Path), sessionMetadataName), &updatedMetadata); err != nil {
		t.Fatal(err)
	}
	if updatedMetadata.SourceHash == metadata.SourceHash {
		t.Fatal("changing one physical fragment did not change the logical source hash")
	}
}

func TestExportRejectsCodexFragmentsWithConflictingRepositoryIdentity(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	const id = "fragment-conflict"
	writeCodexFragmentFixture(t, codexHome, "rollout-"+id+".jsonl", id,
		"https://github.com/example/fragment-contract-one.git", "2026-08-05T01:00:00Z", "first-user", "first-assistant")
	writeCodexFragmentFixture(t, codexHome, "rollout-"+id+"_continuation.jsonl", id,
		"https://github.com/example/fragment-contract-two.git", "2026-08-05T01:02:00Z", "second-user", "second-assistant")

	result, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err == nil {
		t.Fatal("conflicting repository identities were exported as one Codex session")
	}
	if result.Sources != 2 || result.Created != 0 || result.Unchanged != 0 || result.Skipped == 0 || len(result.Changes) != 0 || len(result.Warnings) == 0 {
		t.Fatalf("conflicting fragment result was not fail-closed: %+v", result)
	}
	entries, listErr := List(ListOptions{Output: output})
	if listErr != nil {
		t.Fatalf("list after conflicting fragments: %v", listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("conflicting fragments partially published archive entries: %+v", entries)
	}
}

func writeCodexFragmentFixture(t *testing.T, home, filename, id, remote, createdAt, userText, assistantText string) string {
	t.Helper()
	started, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	records := []map[string]any{
		{"timestamp": createdAt, "type": "session_meta", "payload": map[string]any{
			"id": id, "timestamp": createdAt, "originator": "Codex Desktop", "source": "vscode", "thread_source": "user",
			"cwd": "/missing/on/this/machine",
			"git": map[string]any{"repository_url": remote, "commit_hash": "abc123", "branch": "main"},
		}},
		{"timestamp": started.Add(time.Second).Format(time.RFC3339), "type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": userText,
		}},
		{"timestamp": started.Add(2 * time.Second).Format(time.RFC3339), "type": "event_msg", "payload": map[string]any{
			"type": "agent_message", "message": assistantText,
		}},
	}
	directory := filepath.Join(home, "sessions", "2026", "08", "05")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, filename)
	if err := os.WriteFile(path, attachmentSessionJSONL(t, id, records), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendCodexFragmentMessages(t *testing.T, path, timestamp, userText, assistantText string) {
	t.Helper()
	started, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	records := []map[string]any{
		{"timestamp": started.Format(time.RFC3339), "type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": userText,
		}},
		{"timestamp": started.Add(time.Second).Format(time.RFC3339), "type": "event_msg", "payload": map[string]any{
			"type": "agent_message", "message": assistantText,
		}},
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(attachmentSessionJSONL(t, "", records)); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertFragmentMarkersOnce(t *testing.T, document string, markers ...string) {
	t.Helper()
	for _, marker := range markers {
		if count := strings.Count(document, marker); count != 1 {
			t.Fatalf("conversation contains %q %d times, want exactly once", marker, count)
		}
	}
}
