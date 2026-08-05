package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestArchiveMaintainsOneSemanticDocumentPerDeviceSession(t *testing.T) {
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

	result, err := Export(context.Background(), testExportOptions(codexHome, output))
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
	repositoryDir := filepath.Join(output, "github.com-example", "project")
	if _, err := os.Stat(filepath.Join(repositoryDir, repositoryMetadataName)); err != nil {
		t.Fatalf("semantic repository metadata is missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(repositoryDir, "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new export created a sessions wrapper: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(output, "repositories")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new export created a repositories wrapper: %v", err)
	}

	repeated, err := Export(context.Background(), testExportOptions(codexHome, output))
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
	renamed, err := Export(context.Background(), testExportOptions(codexHome, output))
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
	updated, err := Export(context.Background(), testExportOptions(codexHome, output))
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
			if entry.Title != "Renamed title" || entry.Versions != 1 {
				t.Fatalf("latest rename was not selected: %+v", entry)
			}
			if filepath.Base(entry.Path) != conversationName || !strings.Contains(filepath.Base(filepath.Dir(entry.Path)), "renamed-title") {
				t.Fatalf("session document path is not semantic: %s", entry.Path)
			}
			if strings.Contains(entry.Path, "sha256") || strings.Contains(entry.Path, entry.SessionID) {
				t.Fatalf("visible path exposes identity hash: %s", entry.Path)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(entry.Path), sessionMetadataName)); err != nil {
				t.Fatalf("hidden session metadata is missing: %v", err)
			}
			var metadata sessionMetadata
			if err := readMetadata(filepath.Join(filepath.Dir(entry.Path), sessionMetadataName), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.SessionKey != digest("device-session-v1\x00device:test\x00session-a") ||
				metadata.SourceHash == "" || metadata.DocumentHash == "" {
				t.Fatalf("hidden session identity is incomplete: %+v", metadata)
			}
		}
	}
	history, err := List(ListOptions{Output: output, History: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history returned %d current documents, want 2", len(history))
	}
}

func TestExportRetainsDocumentWhenSourceIsArchivedOrMissing(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeSessionFixture(t, codexHome, "finished-session", "https://github.com/example/project.git", "mission complete")

	first, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || first.Created != 1 || len(first.Changes) != 1 {
		t.Fatalf("initial export failed: %+v, %v", first, err)
	}
	documentPath := first.Changes[0].Path
	sidecarPath := filepath.Join(filepath.Dir(documentPath), sessionMetadataName)
	documentBefore, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	sidecarBefore, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}

	activePath := filepath.Join(codexHome, "sessions", "2026", "08", "05", "rollout-finished-session.jsonl")
	archivedPath := filepath.Join(codexHome, "archived_sessions", "rollout-finished-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(activePath, archivedPath); err != nil {
		t.Fatal(err)
	}
	archived, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || archived.Sources != 1 || archived.Unchanged != 1 || len(archived.Changes) != 0 {
		t.Fatalf("archived source was not retained as unchanged: %+v, %v", archived, err)
	}

	if err := os.Remove(archivedPath); err != nil {
		t.Fatal(err)
	}
	missing, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || missing.Sources != 0 || missing.Created != 0 || len(missing.Changes) != 0 {
		t.Fatalf("missing source changed the archive result: %+v, %v", missing, err)
	}
	documentAfter, documentErr := os.ReadFile(documentPath)
	sidecarAfter, sidecarErr := os.ReadFile(sidecarPath)
	if documentErr != nil || sidecarErr != nil || !bytes.Equal(documentBefore, documentAfter) || !bytes.Equal(sidecarBefore, sidecarAfter) {
		t.Fatalf("missing source deleted or changed its archive: document=%v sidecar=%v", documentErr, sidecarErr)
	}
	entries, err := List(ListOptions{Output: output})
	if err != nil || len(entries) != 1 || entries[0].SessionID != "finished-session" {
		t.Fatalf("missing source disappeared from the derived catalog: %+v, %v", entries, err)
	}
}

func TestExportRefusesToOverwriteManuallyEditedDocument(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeSessionFixture(t, codexHome, "session-a", "https://github.com/example/project.git", "first answer")
	first, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || len(first.Changes) != 1 {
		t.Fatalf("initial export failed: %+v, %v", first, err)
	}
	documentPath := first.Changes[0].Path
	manual := []byte("manually edited\n")
	if err := os.WriteFile(documentPath, manual, 0o644); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(codexHome, "sessions", "2026", "08", "05", "rollout-session-a.jsonl")
	file, err := os.OpenFile(sourcePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString("{\"timestamp\":\"2026-08-05T05:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"new answer\"}}\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("update fixture: %v / %v", writeErr, closeErr)
	}
	result, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err == nil || result.Skipped != 1 || len(result.Changes) != 0 {
		t.Fatalf("manual edit was not protected: %+v, %v", result, err)
	}
	after, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, manual) {
		t.Fatalf("manual document was overwritten: %q", after)
	}
}

func TestSemanticSessionCollisionIsRefused(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeSessionFixture(t, codexHome, "session-a", "https://github.com/example/project.git", "first")
	writeSessionFixture(t, codexHome, "session-b", "https://github.com/example/project.git", "second")
	writeTitles(t, codexHome,
		titleLine("session-a", "Same title", "2026-08-05T02:00:00Z"),
		titleLine("session-b", "Same title", "2026-08-05T02:00:00Z"),
	)
	result, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err == nil || result.Created != 1 || result.Skipped != 1 {
		t.Fatalf("semantic collision was not isolated: %+v, %v", result, err)
	}
	entries, listErr := List(ListOptions{Output: output})
	if listErr != nil || len(entries) != 1 {
		t.Fatalf("collision damaged the first identity: %+v, %v", entries, listErr)
	}
}

func TestExistingSemanticRepositoryDirectoryIsNotClaimed(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeSessionFixture(t, codexHome, "session-a", "https://github.com/example/project.git", "answer")
	repositoryDir := filepath.Join(output, "github.com-example", "project")
	if err := os.MkdirAll(repositoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(repositoryDir, "keep.txt")
	if err := os.WriteFile(userPath, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err == nil || result.Skipped != 1 || len(result.Changes) != 0 {
		t.Fatalf("existing repository directory was claimed: %+v, %v", result, err)
	}
	data, readErr := os.ReadFile(userPath)
	if readErr != nil || string(data) != "keep\n" {
		t.Fatalf("existing repository content changed: %q, %v", data, readErr)
	}
	if _, metadataErr := os.Stat(filepath.Join(repositoryDir, repositoryMetadataName)); !errors.Is(metadataErr, os.ErrNotExist) {
		t.Fatalf("repository sidecar was unexpectedly created: %v", metadataErr)
	}
}

func TestListKeepsLegacyHashLayoutInspectable(t *testing.T) {
	output := t.TempDir()
	path := filepath.Join(output, "repositories", "project--oldhash", "sessions", "legacy-id", "title--snapshot.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
repository_key: "sha256:repo"
repository_name: "project"
session_id: "legacy-id"
session_title: "Legacy title"
snapshot_hash: "sha256:snapshot"
source_hash: "sha256:source"
updated_at: "2026-08-05T01:00:00Z"
---

# Legacy title
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := List(ListOptions{Output: output, History: true})
	if err != nil || len(entries) != 1 || !entries[0].Legacy || entries[0].Path != path {
		t.Fatalf("legacy session was not inspectable: %+v, %v", entries, err)
	}
}

func TestLayoutV3SessionMigratesToCurrentLayoutOnVerifiedExport(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeSessionFixture(t, codexHome, "session-a", "https://github.com/example/project.git", "answer")
	first, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || len(first.Changes) != 1 {
		t.Fatalf("initial layout-v5 export failed: %+v, %v", first, err)
	}
	newRepositoryDir := filepath.Join(output, "github.com-example", "project")
	oldRepositoryDir := filepath.Join(output, "repositories", "github.com", "example", "project")
	if err := os.MkdirAll(filepath.Dir(oldRepositoryDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newRepositoryDir, oldRepositoryDir); err != nil {
		t.Fatal(err)
	}
	relativeDocument, err := filepath.Rel(newRepositoryDir, first.Changes[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	deviceName := strings.Split(relativeDocument, string(filepath.Separator))[0]
	if err := os.MkdirAll(filepath.Join(oldRepositoryDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(oldRepositoryDir, deviceName), filepath.Join(oldRepositoryDir, "sessions", deviceName)); err != nil {
		t.Fatal(err)
	}
	var repository repositoryMetadata
	repositoryPath := filepath.Join(oldRepositoryDir, repositoryMetadataName)
	if err := readMetadata(repositoryPath, &repository); err != nil {
		t.Fatal(err)
	}
	repository.LayoutVersion = 3
	repositoryBytes, err := marshalMetadata(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repositoryPath, repositoryBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	oldDocument := filepath.Join(oldRepositoryDir, "sessions", relativeDocument)
	sessionPath := filepath.Join(filepath.Dir(oldDocument), sessionMetadataName)
	var session sessionMetadata
	if err := readMetadata(sessionPath, &session); err != nil {
		t.Fatal(err)
	}
	session.LayoutVersion = 3
	sessionBytes, err := marshalMetadata(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, sessionBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	legacyCurrent, err := List(ListOptions{Output: output})
	if err != nil || len(legacyCurrent) != 1 || legacyCurrent[0].Path != oldDocument {
		t.Fatalf("layout-v3 current session was not readable: %+v, %v", legacyCurrent, err)
	}
	migrated, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || migrated.Created != 1 || len(migrated.Changes) != 1 {
		t.Fatalf("layout-v3 session did not migrate: %+v, %v", migrated, err)
	}
	if strings.Contains(migrated.Changes[0].Path, string(filepath.Separator)+"repositories"+string(filepath.Separator)) ||
		strings.Contains(migrated.Changes[0].Path, string(filepath.Separator)+"sessions"+string(filepath.Separator)) ||
		!strings.Contains(migrated.Changes[0].Path, filepath.Join("github.com-example", "project", "test-device")) {
		t.Fatalf("migrated path was not flattened: %s", migrated.Changes[0].Path)
	}
	var migratedMetadata sessionMetadata
	if err := readMetadata(filepath.Join(filepath.Dir(migrated.Changes[0].Path), sessionMetadataName), &migratedMetadata); err != nil {
		t.Fatal(err)
	}
	if migratedMetadata.LayoutVersion != LayoutVersion {
		t.Fatalf("migrated sidecar layout = %d, want %d", migratedMetadata.LayoutVersion, LayoutVersion)
	}
	if _, err := os.Stat(repositoryPath); err != nil {
		t.Fatalf("old repository identity sidecar was unexpectedly removed: %v", err)
	}
}

func TestLayoutV4SessionMigratesWithoutSessionsWrapper(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeSessionFixture(t, codexHome, "session-a", "https://github.com/example/project.git", "answer")
	first, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || len(first.Changes) != 1 {
		t.Fatalf("initial export failed: %+v, %v", first, err)
	}
	repositoryDir := filepath.Join(output, "github.com-example", "project")
	repositoryPath := filepath.Join(repositoryDir, repositoryMetadataName)
	var repository repositoryMetadata
	if err := readMetadata(repositoryPath, &repository); err != nil {
		t.Fatal(err)
	}
	repository.LayoutVersion = 4
	repositoryBytes, err := marshalMetadata(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repositoryPath, repositoryBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	relativeDocument, err := filepath.Rel(repositoryDir, first.Changes[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	deviceName := strings.Split(relativeDocument, string(filepath.Separator))[0]
	if err := os.MkdirAll(filepath.Join(repositoryDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repositoryDir, deviceName), filepath.Join(repositoryDir, "sessions", deviceName)); err != nil {
		t.Fatal(err)
	}
	oldDocument := filepath.Join(repositoryDir, "sessions", relativeDocument)
	metadataPath := filepath.Join(filepath.Dir(oldDocument), sessionMetadataName)
	var metadata sessionMetadata
	if err := readMetadata(metadataPath, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata.LayoutVersion = 4
	metadataBytes, err := marshalMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, metadataBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	legacyCurrent, err := List(ListOptions{Output: output})
	if err != nil || len(legacyCurrent) != 1 || legacyCurrent[0].Path != oldDocument {
		t.Fatalf("layout-v4 current session was not readable: %+v, %v", legacyCurrent, err)
	}
	migrated, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || migrated.Created != 1 || len(migrated.Changes) != 1 {
		t.Fatalf("layout-v4 session did not migrate: %+v, %v", migrated, err)
	}
	if strings.Contains(migrated.Changes[0].Path, string(filepath.Separator)+"sessions"+string(filepath.Separator)) {
		t.Fatalf("migrated path retained the sessions wrapper: %s", migrated.Changes[0].Path)
	}
	if _, err := os.Lstat(filepath.Join(repositoryDir, "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty layout-v4 sessions wrapper remained: %v", err)
	}
	if err := readMetadata(filepath.Join(filepath.Dir(migrated.Changes[0].Path), sessionMetadataName), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.LayoutVersion != LayoutVersion {
		t.Fatalf("migrated sidecar layout = %d, want %d", metadata.LayoutVersion, LayoutVersion)
	}
	if err := readMetadata(repositoryPath, &repository); err != nil {
		t.Fatal(err)
	}
	if repository.LayoutVersion != LayoutVersion {
		t.Fatalf("repository sidecar layout = %d, want %d", repository.LayoutVersion, LayoutVersion)
	}
}

func TestListRecoversV3SessionSidecarAlreadyMovedUnderV5Repository(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeSessionFixture(t, codexHome, "session-a", "https://github.com/example/project.git", "answer")
	first, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || len(first.Changes) != 1 {
		t.Fatalf("initial export failed: %+v, %v", first, err)
	}
	metadataPath := filepath.Join(filepath.Dir(first.Changes[0].Path), sessionMetadataName)
	var metadata sessionMetadata
	if err := readMetadata(metadataPath, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata.LayoutVersion = 3
	data, err := marshalMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := List(ListOptions{Output: output})
	if err != nil || len(entries) != 1 || entries[0].Path != first.Changes[0].Path {
		t.Fatalf("interrupted migration state was not readable: %+v, %v", entries, err)
	}
	recovered, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || recovered.Created != 1 || len(recovered.Changes) != 1 {
		t.Fatalf("interrupted migration state was not repaired: %+v, %v", recovered, err)
	}
	if err := readMetadata(metadataPath, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.LayoutVersion != LayoutVersion {
		t.Fatalf("recovered layout = %d, want %d", metadata.LayoutVersion, LayoutVersion)
	}
}

func TestExportIgnoresIncompleteBusySessionWithoutError(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "active.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"timestamp":"2026-08-05T01:00:00Z"`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Export(context.Background(), Options{
		CodexHome: codexHome, Output: filepath.Join(root, "archive"),
		AllRepos: true, SessionID: "possibly-active", StabilityWindow: -1,
		DeviceID: "device:test", DeviceName: "test-device",
	})
	if err != nil {
		t.Fatalf("busy-only export returned an error: %v", err)
	}
	if result.Busy != 1 || result.Skipped != 0 || len(result.Warnings) != 0 || len(result.Changes) != 0 {
		t.Fatalf("busy source was not silently classified: %+v", result)
	}
}

func TestObservationWindowClassifiesMutationAsBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err == nil {
			_, err = file.WriteString("{}\n")
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
		mutated <- err
	}()
	stable, busy, issues, err := observeStableSources(context.Background(), []string{path}, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if mutationErr := <-mutated; mutationErr != nil {
		t.Fatal(mutationErr)
	}
	if busy != 1 || len(stable) != 0 || len(issues) != 0 {
		t.Fatalf("mutation was not classified as busy: stable=%v busy=%d issues=%v", stable, busy, issues)
	}
}

func TestParseSessionDerivesConversationTimeline(t *testing.T) {
	raw := []byte(`{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"timeline"}}
{"timestamp":"2026-08-05T01:01:00Z","type":"event_msg","payload":{"type":"user_message","message":"question"}}
{"timestamp":"2026-08-05T01:02:00Z","type":"event_msg","payload":{"type":"agent_message","message":"answer"}}
{"timestamp":"2026-08-05T01:03:00Z","type":"response_item","payload":{"type":"function_call"}}
`)
	session, err := parseSession(raw, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := func(hour, minute int) time.Time {
		return time.Date(2026, 8, 5, hour, minute, 0, 0, time.UTC)
	}
	if !session.CreatedAt.Equal(want(1, 0)) ||
		!session.FirstMessageAt.Equal(want(1, 1)) ||
		!session.LastMessageAt.Equal(want(1, 2)) ||
		!session.LastEventAt.Equal(want(1, 3)) {
		t.Fatalf("unexpected timeline: %+v", session)
	}
	if session.UserMessages != 1 || session.AssistantMessages != 1 || len(session.Messages) != 2 {
		t.Fatalf("unexpected message counts: %+v", session)
	}
}

func TestParseSessionUsesUserEventsAndOmitsInjectedContext(t *testing.T) {
	raw := injectedContextFixture(t, "context-filter", true)
	session, err := parseSession(raw, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if session.Title != "Please fix the exporter" {
		t.Fatalf("title came from injected context: %q", session.Title)
	}
	if session.UserMessages != 1 || session.AssistantMessages != 1 || len(session.Messages) != 2 {
		t.Fatalf("unexpected visible conversation: %+v", session)
	}
	if session.Messages[0].Role != "user" || session.Messages[0].Text != "Please fix the exporter" ||
		session.Messages[1].Role != "assistant" || session.Messages[1].Text != "Done" {
		t.Fatalf("wrong messages survived reconciliation: %+v", session.Messages)
	}
	visible := session.Title + "\n" + session.Messages[0].Text + "\n" + session.Messages[1].Text
	for _, leaked := range []string{"recommended_plugins", "AGENTS.md instructions", "environment_context"} {
		if strings.Contains(visible, leaked) {
			t.Fatalf("injected %s leaked into the conversation: %s", leaked, visible)
		}
	}
	wantFirst := time.Date(2026, 8, 5, 1, 0, 2, 0, time.UTC)
	if !session.FirstMessageAt.Equal(wantFirst) {
		t.Fatalf("timeline used the injected response timestamp: %s", session.FirstMessageAt)
	}
}

func TestContextOnlySessionIsNotExported(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeSessionRecords(t, codexHome, "context-only", injectedContextRecords("context-only", false))

	result, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources != 1 || result.Matched != 0 || result.Created != 0 || result.Skipped != 0 || len(result.Changes) != 0 {
		t.Fatalf("context-only source was treated as a conversation: %+v", result)
	}
	if entries, err := List(ListOptions{Output: output}); err != nil || len(entries) != 0 {
		t.Fatalf("context-only source created archive entries: %+v, %v", entries, err)
	}
}

func TestResponseOnlyUserTextThatMentionsContextTagsIsPreserved(t *testing.T) {
	raw := attachmentSessionJSONL(t, "literal-context-text", []map[string]any{
		{"timestamp": "2026-08-05T01:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "literal-context-text"}},
		{"timestamp": "2026-08-05T01:00:01Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "user", "content": []any{map[string]any{
				"type": "input_text", "text": "Why does <recommended_plugins> appear in my export?",
			}},
		}},
	})
	session, err := parseSession(raw, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if session.UserMessages != 1 || len(session.Messages) != 1 ||
		session.Messages[0].Text != "Why does <recommended_plugins> appear in my export?" {
		t.Fatalf("ordinary legacy user text was mistaken for injected context: %+v", session)
	}
}

func TestVerifiedReexportRepairsInjectedContextDocument(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	raw := injectedContextFixture(t, "repair-context", true)
	writeSessionRecords(t, codexHome, "repair-context", injectedContextRecords("repair-context", true))

	parsed, err := parseSession(raw, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	repository := repositoryFromRemote("github.com/example/project")
	pollutedTitle := "<recommended_plugins> generated title"
	polluted := parsed
	polluted.Title = pollutedTitle
	polluted.Messages = append([]Message{{
		Role: "user", Text: "<recommended_plugins>\nplugin list\n</recommended_plugins>",
		Timestamp: time.Date(2026, 8, 5, 1, 0, 1, 0, time.UTC),
	}}, parsed.Messages...)
	polluted.UserMessages++
	polluted.FirstMessageAt = polluted.Messages[0].Timestamp
	polluted.OmittedCount--
	snapshot := makeSnapshot(repository, polluted, "device:test", "test-device")
	repositoryDir := filepath.Join(output, semanticRepositoryDirectory(repository))
	if err := publishRepositoryMetadata(repositoryDir, repository); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(repositoryDir, "test-device", semanticSessionDirectory(snapshot))
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDocument := bytes.Replace(renderSnapshot(snapshot), []byte("renderer_version: 5"), []byte("renderer_version: 4"), 1)
	oldRecord := sessionRecord(snapshot, digestBytes(oldDocument))
	oldRecord.RendererVersion = 4
	metadata, err := marshalMetadata(oldRecord)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldDir, conversationName)
	if err := os.WriteFile(oldPath, oldDocument, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, sessionMetadataName), metadata, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || len(result.Changes) != 1 || result.Changes[0].Kind != "renamed" {
		t.Fatalf("polluted document was not repaired as a rename: %+v", result)
	}
	if _, err := os.Lstat(oldDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("polluted semantic directory remained: %v", err)
	}
	newPath := result.Changes[0].Path
	document, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"recommended_plugins", "AGENTS.md instructions", "environment_context"} {
		if strings.Contains(string(document), leaked) {
			t.Fatalf("repaired document still contains %s:\n%s", leaked, document)
		}
	}
	if !strings.Contains(string(document), "# Please fix the exporter") || !strings.Contains(string(document), "Done") {
		t.Fatalf("repaired document lost the real conversation:\n%s", document)
	}
	var current sessionMetadata
	if err := readMetadata(filepath.Join(filepath.Dir(newPath), sessionMetadataName), &current); err != nil {
		t.Fatal(err)
	}
	if current.RendererVersion != RendererVersion {
		t.Fatalf("renderer metadata was not upgraded: %+v", current)
	}
	repeated, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || repeated.Created != 0 || repeated.Unchanged != 1 {
		t.Fatalf("repaired document was not idempotent: %+v, %v", repeated, err)
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

func testExportOptions(codexHome, output string) Options {
	return Options{
		CodexHome: codexHome, Output: output, AllRepos: true, StabilityWindow: -1,
		DeviceID: "device:test", DeviceName: "test-device",
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

func injectedContextFixture(t *testing.T, id string, includeConversation bool) []byte {
	t.Helper()
	return attachmentSessionJSONL(t, id, injectedContextRecords(id, includeConversation))
}

func injectedContextRecords(id string, includeConversation bool) []map[string]any {
	records := []map[string]any{
		{"timestamp": "2026-08-05T01:00:00Z", "type": "session_meta", "payload": map[string]any{
			"id": id, "cwd": "/private/worktree",
			"git": map[string]any{"repository_url": "https://github.com/example/project.git", "commit_hash": "abc123", "branch": "main"},
		}},
		{"timestamp": "2026-08-05T01:00:01Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "<recommended_plugins>\nplugin list\n</recommended_plugins>"},
				map[string]any{"type": "input_text", "text": "# AGENTS.md instructions for /private/worktree\n\n<INSTRUCTIONS>\nrules\n</INSTRUCTIONS>"},
				map[string]any{"type": "input_text", "text": "<environment_context>\n<cwds>/private/worktree</cwds>\n</environment_context>"},
			},
		}},
	}
	if !includeConversation {
		return records
	}
	return append(records,
		map[string]any{"timestamp": "2026-08-05T01:00:01.500Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Please fix the exporter"}},
		}},
		map[string]any{"timestamp": "2026-08-05T01:00:02Z", "type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": "Please fix the exporter",
		}},
		map[string]any{"timestamp": "2026-08-05T01:00:03Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Done"}},
		}},
		map[string]any{"timestamp": "2026-08-05T01:00:03.100Z", "type": "event_msg", "payload": map[string]any{
			"type": "agent_message", "message": "Done",
		}},
	)
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
