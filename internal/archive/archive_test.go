package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestArchiveCreatesSetOfImmutableSessionSnapshots(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeSessionFixture(t, codexHome, "session-a", "https://github.com/example/project.git", "first answer")
	writeSessionFixture(t, codexHome, "session-b", "git@github.com:example/project.git", "second answer")
	activeB := filepath.Join(codexHome, "sessions", "2026", "08", "05", "rollout-session-b.jsonl")
	archivedB := filepath.Join(codexHome, "archived_sessions", "rollout-session-b.jsonl")
	if err := os.MkdirAll(filepath.Dir(archivedB), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(activeB, archivedB); err != nil {
		t.Fatal(err)
	}
	writeTitles(t, codexHome,
		titleLine("session-a", "Semantic title", "2026-08-05T02:00:00Z"),
		titleLine("session-b", "Other machine", "2026-08-05T02:01:00Z"),
	)
	sourcePath := filepath.Join(codexHome, "sessions", "2026", "08", "05", "rollout-session-a.jsonl")
	sourceBefore, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Export(context.Background(), Options{CodexHome: codexHome, Output: output, AllRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 || result.Matched != 2 {
		t.Fatalf("unexpected first result: %+v", result)
	}
	if len(result.Changes) != 2 || result.Changes[0].Kind != "new" || result.Changes[1].Kind != "new" {
		t.Fatalf("first export did not report new sessions: %+v", result.Changes)
	}
	sourceAfter, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceBefore) != string(sourceAfter) {
		t.Fatal("archive modified the raw Codex source")
	}
	repositories, err := filepath.Glob(filepath.Join(output, "repositories", "*"))
	if err != nil || len(repositories) != 1 {
		t.Fatalf("expected one repository set, got %v, %v", repositories, err)
	}

	repeated, err := Export(context.Background(), Options{CodexHome: codexHome, Output: output, AllRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Created != 0 || repeated.Unchanged != 2 {
		t.Fatalf("repeat was not idempotent: %+v", repeated)
	}
	if len(repeated.Changes) != 0 {
		t.Fatalf("unchanged export exposed old records: %+v", repeated.Changes)
	}

	writeTitles(t, codexHome,
		titleLine("session-a", "Semantic title", "2026-08-05T02:00:00Z"),
		titleLine("session-b", "Other machine", "2026-08-05T02:01:00Z"),
		titleLine("session-a", "Renamed title", "2026-08-05T03:00:00Z"),
	)
	renamed, err := Export(context.Background(), Options{CodexHome: codexHome, Output: output, AllRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Created != 1 || renamed.Unchanged != 1 {
		t.Fatalf("rename did not create one new snapshot: %+v", renamed)
	}
	if len(renamed.Changes) != 1 || renamed.Changes[0].Kind != "renamed" {
		t.Fatalf("rename change was not classified: %+v", renamed.Changes)
	}
	updatedSource, err := os.OpenFile(sourcePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updatedSource.WriteString("{\"timestamp\":\"2026-08-05T04:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"updated\"}}\n"); err != nil {
		updatedSource.Close()
		t.Fatal(err)
	}
	if err := updatedSource.Close(); err != nil {
		t.Fatal(err)
	}
	updated, err := Export(context.Background(), Options{CodexHome: codexHome, Output: output, AllRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Created != 1 || updated.Unchanged != 1 {
		t.Fatalf("source update did not create one new snapshot: %+v", updated)
	}
	if len(updated.Changes) != 1 || updated.Changes[0].Kind != "updated" {
		t.Fatalf("content change was not classified: %+v", updated.Changes)
	}

	entries, err := List(ListOptions{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("list returned %d sessions", len(entries))
	}
	for _, entry := range entries {
		if entry.SessionID == "session-a" {
			if entry.Title != "Renamed title" || entry.Versions != 3 {
				t.Fatalf("latest rename was not selected: %+v", entry)
			}
			if !strings.Contains(filepath.Base(entry.Path), "renamed-title--") {
				t.Fatalf("snapshot filename is not semantic: %s", entry.Path)
			}
		}
	}
	history, err := List(ListOptions{Output: output, History: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("history returned %d snapshots, want 4", len(history))
	}
}

func TestPublishImmutableIsSafeUnderConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.md")
	data := []byte("immutable\n")
	var created atomic.Int32
	var wait sync.WaitGroup
	errors := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			wasCreated, err := publishImmutable(path, data)
			if err != nil {
				errors <- err
				return
			}
			if wasCreated {
				created.Add(1)
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	if created.Load() != 1 {
		t.Fatalf("created %d files, want exactly one publisher", created.Load())
	}
	if _, err := publishImmutable(path, []byte("conflict\n")); err == nil {
		t.Fatal("conflicting publication was allowed")
	}
}

func writeSessionFixture(t *testing.T, home, id, remote, answer string) {
	t.Helper()
	directory := filepath.Join(home, "sessions", "2026", "08", "05")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	records := []map[string]interface{}{
		{"timestamp": "2026-08-05T01:00:00Z", "type": "session_meta", "payload": map[string]interface{}{
			"id": id, "cwd": "/missing/on/this/machine", "cli_version": "1.2.3",
			"git": map[string]interface{}{"repository_url": remote, "commit_hash": "abc123", "branch": "main"},
		}},
		{"timestamp": "2026-08-05T01:00:01Z", "type": "response_item", "payload": map[string]interface{}{
			"type": "message", "role": "user", "content": []interface{}{map[string]interface{}{"type": "input_text", "text": "do the work"}},
		}},
		{"timestamp": "2026-08-05T01:00:02Z", "type": "response_item", "payload": map[string]interface{}{
			"type": "message", "role": "assistant", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": answer}},
		}},
	}
	var lines []string
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	path := filepath.Join(directory, fmt.Sprintf("rollout-%s.jsonl", id))
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func titleLine(id, title, updated string) string {
	data, _ := json.Marshal(map[string]string{"id": id, "thread_name": title, "updated_at": updated})
	return string(data)
}

func writeTitles(t *testing.T, home string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTitleTimestampSelectsNewestRecord(t *testing.T) {
	home := t.TempDir()
	writeTitles(t, home,
		titleLine("id", "new", "2026-08-05T03:00:00Z"),
		titleLine("id", "old", "2026-08-05T02:00:00Z"),
	)
	titles, err := loadTitles(home)
	if err != nil {
		t.Fatal(err)
	}
	if titles["id"].Title != "new" || !titles["id"].UpdatedAt.Equal(time.Date(2026, 8, 5, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("wrong title selected: %+v", titles["id"])
	}
}
