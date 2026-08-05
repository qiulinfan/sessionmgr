package archive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSessionExtractsOnlyStructuredUserAttachments(t *testing.T) {
	dataURL := "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString([]byte("image bytes"))
	raw := attachmentSessionJSONL(t, "structured", []map[string]any{
		{"timestamp": "2026-08-05T01:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "structured"}},
		{"timestamp": "2026-08-05T01:00:01Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": `<image name=[Image #1] path="/tmp/Design.PNG">`},
				map[string]any{"type": "input_image", "image_url": dataURL},
				map[string]any{"type": "input_text", "text": "</image>"},
				map[string]any{"type": "input_text", "text": "Please compare /tmp/not-an-attachment.txt"},
			},
		}},
		{"timestamp": "2026-08-05T01:00:01Z", "type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": "Please compare /tmp/not-an-attachment.txt",
			"local_images": []any{"/tmp/Design.PNG"},
		}},
		{"timestamp": "2026-08-05T01:00:02Z", "type": "response_item", "payload": map[string]any{
			"type": "function_call", "arguments": `{"path":"/tmp/tool-file.txt"}`,
		}},
		{"timestamp": "2026-08-05T01:00:03Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": "assistant output"},
				map[string]any{"type": "input_file", "filename": "agent-created.txt", "file_data": "YWdlbnQ="},
			},
		}},
	})
	session, err := parseSession(raw, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Messages) != 2 || len(session.Messages[0].Attachments) != 1 || len(session.Messages[1].Attachments) != 0 {
		t.Fatalf("unexpected structured parse: %+v", session.Messages)
	}
	attachment := session.Messages[0].Attachments[0]
	if attachment.SourceKind != "embedded_data" || attachment.LocalPath != "/tmp/Design.PNG" || attachment.Name != "Design.PNG" {
		t.Fatalf("unexpected attachment candidate: %+v", attachment)
	}
	if strings.Contains(session.Messages[0].Text, `<image name=`) || strings.Contains(session.Messages[0].Text, "tool-file") {
		t.Fatalf("wrapper or tool input leaked into conversation: %q", session.Messages[0].Text)
	}
	if !strings.Contains(session.Messages[0].Text, "/tmp/not-an-attachment.txt") {
		t.Fatalf("ordinary user text was unexpectedly rewritten: %q", session.Messages[0].Text)
	}
}

func TestExportArchivesEmbeddedAttachmentAndProtectsItFromManualEdits(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	attachmentData := []byte("meeting notes\n")
	writeAttachmentSession(t, codexHome, "with-file", []any{
		map[string]any{"type": "input_text", "text": "Read this file"},
		map[string]any{
			"type": "input_file", "filename": "Project Notes.txt",
			"file_data": "data:text/plain;base64," + base64.StdEncoding.EncodeToString(attachmentData),
		},
	})

	result, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Attachments != 1 || result.ArchivedAttachments != 1 || len(result.Warnings) != 0 {
		t.Fatalf("unexpected attachment export: %+v", result)
	}
	sessionDir := filepath.Dir(result.Changes[0].Path)
	attachmentPath := filepath.Join(sessionDir, "attachments", "001-project-notes.txt")
	data, err := os.ReadFile(attachmentPath)
	if err != nil || string(data) != string(attachmentData) {
		t.Fatalf("archived attachment mismatch: %q, %v", data, err)
	}
	markdown, err := os.ReadFile(result.Changes[0].Path)
	if err != nil || !strings.Contains(string(markdown), "[Project Notes.txt](attachments/001-project-notes.txt)") {
		t.Fatalf("Markdown attachment link missing: %v\n%s", err, markdown)
	}
	var metadata sessionMetadata
	if err := readMetadata(filepath.Join(sessionDir, sessionMetadataName), &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.Attachments) != 1 || metadata.Attachments[0].Status != attachmentStatusArchived ||
		metadata.Attachments[0].ContentHash != digestBytes(attachmentData) || metadata.Attachments[0].Size != int64(len(attachmentData)) {
		t.Fatalf("attachment manifest mismatch: %+v", metadata.Attachments)
	}
	metadataBytes, err := os.ReadFile(filepath.Join(sessionDir, sessionMetadataName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadataBytes), "data:text") || strings.Contains(string(metadataBytes), root) {
		t.Fatalf("sidecar persisted a source value: %s", metadataBytes)
	}

	repeated, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || repeated.Created != 0 || repeated.Unchanged != 1 {
		t.Fatalf("attachment export was not idempotent: %+v, %v", repeated, err)
	}
	manual := []byte("manual replacement\n")
	if err := os.WriteFile(attachmentPath, manual, 0o644); err != nil {
		t.Fatal(err)
	}
	appendSessionRecord(t, codexHome, "with-file", map[string]any{
		"timestamp": "2026-08-05T02:00:00Z", "type": "response_item",
		"payload": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "updated"}}},
	})
	blocked, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err == nil || blocked.Skipped != 1 || len(blocked.Changes) != 0 {
		t.Fatalf("manual attachment edit was not protected: %+v, %v", blocked, err)
	}
	after, readErr := os.ReadFile(attachmentPath)
	if readErr != nil || string(after) != string(manual) {
		t.Fatalf("manual attachment was overwritten: %q, %v", after, readErr)
	}
}

func TestAttachmentLimitIsInclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exact.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxAttachmentBytes); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, size, err := readStableAttachment(context.Background(), path)
	if err != nil || size != MaxAttachmentBytes || int64(len(data)) != MaxAttachmentBytes {
		t.Fatalf("exact-limit attachment was rejected: size=%d len=%d err=%v", size, len(data), err)
	}
	if err := os.Truncate(path, MaxAttachmentBytes+1); err != nil {
		t.Fatal(err)
	}
	data, size, err = readStableAttachment(context.Background(), path)
	if !errors.Is(err, errAttachmentTooLarge) || data != nil || size != MaxAttachmentBytes+1 {
		t.Fatalf("over-limit attachment was not rejected: size=%d len=%d err=%v", size, len(data), err)
	}
}

func TestUnavailableOversizeAndRemoteAttachmentsDoNotFailConversation(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	largePath := filepath.Join(root, "large.pdf")
	file, err := os.Create(largePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxAttachmentBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	writeEventAttachmentSession(t, codexHome, "attachment-statuses", []any{
		map[string]any{"path": largePath, "filename": "large.pdf", "mime_type": "application/pdf"},
		map[string]any{"path": filepath.Join(root, "missing.txt"), "filename": "missing.txt"},
		map[string]any{"url": "https://example.com/private/report.pdf?token=secret", "filename": "remote.pdf"},
	})

	result, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil {
		t.Fatalf("attachment-level failures failed the conversation: %+v, %v", result, err)
	}
	if result.Created != 1 || result.Attachments != 3 || result.ArchivedAttachments != 0 || len(result.Warnings) != 3 {
		t.Fatalf("unexpected attachment statuses: %+v", result)
	}
	sessionDir := filepath.Dir(result.Changes[0].Path)
	if _, err := os.Lstat(filepath.Join(sessionDir, "attachments")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unarchived inputs created an attachments directory: %v", err)
	}
	var metadata sessionMetadata
	if err := readMetadata(filepath.Join(sessionDir, sessionMetadataName), &metadata); err != nil {
		t.Fatal(err)
	}
	statuses := []string{metadata.Attachments[0].Status, metadata.Attachments[1].Status, metadata.Attachments[2].Status}
	if strings.Join(statuses, ",") != "too_large,busy,remote_reference" {
		t.Fatalf("unexpected manifest statuses: %v", statuses)
	}
	metadataBytes, _ := os.ReadFile(filepath.Join(sessionDir, sessionMetadataName))
	if strings.Contains(string(metadataBytes), root) || strings.Contains(string(metadataBytes), "example.com") || strings.Contains(string(metadataBytes), "token=secret") {
		t.Fatalf("sidecar leaked attachment sources: %s", metadataBytes)
	}
	repeated, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || repeated.Created != 0 || len(repeated.Warnings) != 0 {
		t.Fatalf("unchanged unresolved attachments repeated warnings: %+v, %v", repeated, err)
	}
}

func TestLocalAttachmentRetriesThenKeepsArchivedBytes(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	localPath := filepath.Join(root, "later.txt")
	writeEventAttachmentSession(t, codexHome, "retry-local", []any{
		map[string]any{"path": localPath, "filename": "later.txt", "mime_type": "text/plain"},
	})
	first, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || first.Created != 1 || first.ArchivedAttachments != 0 || len(first.Warnings) != 1 {
		t.Fatalf("missing local attachment did not remain retryable: %+v, %v", first, err)
	}
	content := []byte("captured once\n")
	if err := os.WriteFile(localPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || second.Created != 1 || second.ArchivedAttachments != 1 || len(second.Warnings) != 0 {
		t.Fatalf("available local attachment was not retried: %+v, %v", second, err)
	}
	attachmentPath := filepath.Join(filepath.Dir(second.Changes[0].Path), "attachments", "001-later.txt")
	if data, readErr := os.ReadFile(attachmentPath); readErr != nil || string(data) != string(content) {
		t.Fatalf("retried attachment mismatch: %q, %v", data, readErr)
	}
	if err := os.Remove(localPath); err != nil {
		t.Fatal(err)
	}
	third, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil || third.Created != 0 || third.Unchanged != 1 || third.ArchivedAttachments != 1 || len(third.Warnings) != 0 {
		t.Fatalf("archived local bytes regressed after source disappeared: %+v, %v", third, err)
	}
}

func TestLocalAttachmentSymlinkIsNotFollowed(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("do not follow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	session := prepareSessionAttachments(context.Background(), Session{
		Messages: []Message{{Role: "user", Attachments: []Attachment{{
			Name: "link.txt", SourceKind: "local_path", SourceValue: link, LocalPath: link,
		}}}},
	}, repositoryFromRemote("github.com/example/project"))
	attachment := session.Messages[0].Attachments[0]
	if attachment.Status != attachmentStatusUnavailable || len(attachment.Data) != 0 || attachment.ArchivePath != "" {
		t.Fatalf("local symlink was followed: %+v", attachment)
	}
}

func TestExportRefusesArchivedAttachmentSymlink(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "archive")
	writeAttachmentSession(t, codexHome, "archive-symlink", []any{
		map[string]any{"type": "input_file", "filename": "safe.txt", "file_data": base64.StdEncoding.EncodeToString([]byte("safe\n")), "mime_type": "text/plain"},
	})
	first, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err != nil {
		t.Fatal(err)
	}
	attachmentPath := filepath.Join(filepath.Dir(first.Changes[0].Path), "attachments", "001-safe.txt")
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(attachmentPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, attachmentPath); err != nil {
		t.Fatal(err)
	}
	appendSessionRecord(t, codexHome, "archive-symlink", map[string]any{
		"timestamp": "2026-08-05T03:00:00Z", "type": "event_msg",
		"payload": map[string]any{"type": "agent_message", "message": "later"},
	})
	result, err := Export(context.Background(), testExportOptions(codexHome, output))
	if err == nil || result.Skipped != 1 || len(result.Changes) != 0 {
		t.Fatalf("archived attachment symlink was not refused: %+v, %v", result, err)
	}
	if data, readErr := os.ReadFile(target); readErr != nil || string(data) != "target\n" {
		t.Fatalf("symlink target changed: %q, %v", data, readErr)
	}
}

func TestGitTrackedAttachmentIsProvenWithoutCopy(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repository, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-q")
	runGit(t, repository, "config", "user.name", "Session Manager Test")
	runGit(t, repository, "config", "user.email", "sessionmgr@example.invalid")
	runGit(t, repository, "remote", "add", "origin", "https://github.com/example/attachments.git")
	content := []byte("tracked content\n")
	path := filepath.Join(repository, "docs", "brief.txt")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "docs/brief.txt")
	runGit(t, repository, "commit", "-q", "-m", "fixture")
	commit := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	repo, err := RepositoryFromPath(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	session := prepareSessionAttachments(context.Background(), Session{
		CWD: repository, Commit: commit,
		Messages: []Message{{Role: "user", Attachments: []Attachment{{
			Name: "brief.txt", SourceKind: "embedded_data", LocalPath: path,
			SourceValue: "data:text/plain;base64," + base64.StdEncoding.EncodeToString(content),
		}}}},
	}, repo)
	attachment := session.Messages[0].Attachments[0]
	if attachment.Status != attachmentStatusGitTracked || attachment.RepositoryPath != "docs/brief.txt" || attachment.ArchivePath != "" || len(attachment.Data) != 0 {
		t.Fatalf("tracked attachment was not classified by blob proof: %+v", attachment)
	}

	changed := append([]byte(nil), content...)
	changed[0] = 'T'
	session.Messages[0].Attachments[0] = Attachment{
		Name: "brief.txt", SourceKind: "embedded_data", LocalPath: path,
		SourceValue: "data:text/plain;base64," + base64.StdEncoding.EncodeToString(changed),
	}
	session = prepareSessionAttachments(context.Background(), session, repo)
	if session.Messages[0].Attachments[0].Status != attachmentStatusArchived {
		t.Fatalf("different bytes were incorrectly treated as Git-covered: %+v", session.Messages[0].Attachments[0])
	}
}

func TestSensitiveAttachmentIsNotArchived(t *testing.T) {
	repo := repositoryFromRemote("github.com/example/project")
	session := prepareSessionAttachments(context.Background(), Session{
		Messages: []Message{{Role: "user", Attachments: []Attachment{{
			Name: "auth.json", SourceKind: "embedded_data",
			SourceValue: "data:application/json;base64," + base64.StdEncoding.EncodeToString([]byte(`{"token":"complete-secret"}`)),
		}}}},
	}, repo)
	attachment := session.Messages[0].Attachments[0]
	if attachment.Status != attachmentStatusSensitive || attachment.ArchivePath != "" || len(attachment.Data) != 0 {
		t.Fatalf("sensitive attachment was not blocked: %+v", attachment)
	}
}

func writeAttachmentSession(t *testing.T, home, id string, content []any) {
	t.Helper()
	records := []map[string]any{
		{"timestamp": "2026-08-05T01:00:00Z", "type": "session_meta", "payload": map[string]any{
			"id": id, "cwd": "/missing/on/this/machine",
			"git": map[string]any{"repository_url": "https://github.com/example/project.git", "commit_hash": "abc123", "branch": "main"},
		}},
		{"timestamp": "2026-08-05T01:00:01Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "user", "content": content,
		}},
		{"timestamp": "2026-08-05T01:00:02Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}},
		}},
	}
	writeSessionRecords(t, home, id, records)
}

func writeEventAttachmentSession(t *testing.T, home, id string, attachments []any) {
	t.Helper()
	records := []map[string]any{
		{"timestamp": "2026-08-05T01:00:00Z", "type": "session_meta", "payload": map[string]any{
			"id": id, "cwd": "/missing/on/this/machine",
			"git": map[string]any{"repository_url": "https://github.com/example/project.git", "commit_hash": "abc123", "branch": "main"},
		}},
		{"timestamp": "2026-08-05T01:00:01Z", "type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": "files", "attachments": attachments,
		}},
		{"timestamp": "2026-08-05T01:00:02Z", "type": "event_msg", "payload": map[string]any{
			"type": "agent_message", "message": "done",
		}},
	}
	writeSessionRecords(t, home, id, records)
}

func writeSessionRecords(t *testing.T, home, id string, records []map[string]any) {
	t.Helper()
	directory := filepath.Join(home, "sessions", "2026", "08", "05")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "rollout-"+id+".jsonl"), attachmentSessionJSONL(t, id, records), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendSessionRecord(t *testing.T, home, id string, record map[string]any) {
	t.Helper()
	path := filepath.Join(home, "sessions", "2026", "08", "05", "rollout-"+id+".jsonl")
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append session record: %v / %v", writeErr, closeErr)
	}
}

func attachmentSessionJSONL(t *testing.T, _ string, records []map[string]any) []byte {
	t.Helper()
	lines := make([]string, 0, len(records))
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", "-C", directory)
	command.Args = append(command.Args, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
