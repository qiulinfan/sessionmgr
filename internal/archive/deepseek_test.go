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
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestResolveDeepSeekHomePrecedence(t *testing.T) {
	root := t.TempDir()
	environmentHome := filepath.Join(root, "environment")
	configuredHome := filepath.Join(root, "configured")
	t.Setenv("DSH_HOME", environmentHome)
	resolved, err := resolveDeepSeekHome("")
	if err != nil || resolved != environmentHome {
		t.Fatalf("DSH_HOME was not used: %q, %v", resolved, err)
	}
	resolved, err = resolveDeepSeekHome(configuredHome)
	if err != nil || resolved != configuredHome {
		t.Fatalf("configured DeepSeek home did not win: %q, %v", resolved, err)
	}
}

func TestDiscoverDeepSeekRejectsAmbiguousSessionEncoding(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "sessions", "--workspace--", "session-ambiguous")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{deepSeekPlainFilename, deepSeekCompressedFilename} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := discoverDeepSeekSessionFiles(home); err == nil || !strings.Contains(err.Error(), "both supported encodings") {
		t.Fatalf("ambiguous session encoding was not rejected: %v", err)
	}
}

func TestParseDeepSeekSessionDecodesConcatenatedFramesAndHumanMessages(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "dsh")
	image := []byte("fixture image bytes")
	imageHash := digestBytes(image)
	writeDeepSeekAttachment(t, home, imageHash, image)
	created := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	records := []map[string]any{
		deepSeekTestEvent(0, created.Add(time.Second), "user/message", map[string]any{
			"id": "plugin", "role": "user", "source": map[string]any{"kind": "plugin", "plugin": "instructions"},
			"content": []any{map[string]any{"type": "text", "text": "hidden injected context"}},
		}, "append"),
		deepSeekTestEvent(1, created.Add(2*time.Second), "user/message", map[string]any{
			"id": "human", "role": "user", "source": map[string]any{"kind": "user"},
			"content": []any{
				map[string]any{"type": "text", "text": "Please review this image"},
				map[string]any{"type": "image", "attachment": map[string]any{
					"attachmentId": imageHash, "mediaType": "image/png", "bytes": len(image),
					"width": 1, "height": 1, "name": "screen.png",
				}},
			},
		}, "append"),
		deepSeekTestEvent(2, created.Add(3*time.Second), "session/title", map[string]any{
			"title": "Image review", "messageSeqs": []any{1}, "source": map[string]any{"kind": "fallback"},
		}, nil),
		{
			"type": "reasoning-chunks", "seq0": 3, "time0": created.Add(4 * time.Second).UnixMilli(),
			"data": map[string]any{"turn": 0, "step": 0, "index": 0, "dt": []any{1, 1}, "texts": []any{"a", "b", "c"}},
		},
		deepSeekTestEvent(6, created.Add(5*time.Second), "assistant/message", map[string]any{
			"turn": 0, "step": 0,
			"message": map[string]any{
				"id": "assistant", "role": "assistant",
				"source": map[string]any{"kind": "model", "provider": "deepseek", "model": "fixture"},
				"content": []any{
					map[string]any{"type": "reasoning", "text": "private reasoning"},
					map[string]any{"type": "text", "text": "The image looks good."},
				},
			},
		}, "append"),
		deepSeekTestEvent(7, created.Add(6*time.Second), "tool/call", map[string]any{
			"turn": 0, "step": 0, "callId": "call-1", "name": "read", "arguments": "{\"secret\":true}",
		}, nil),
	}
	raw := deepSeekTestLog(t, "session-fixture", root, created, 0, records, true)
	session, err := parseDeepSeekSession(raw, true, home)
	if err != nil {
		t.Fatal(err)
	}
	if session.Harness != harnessDeepSeek || session.ID != "session-fixture" || session.Title != "Image review" {
		t.Fatalf("unexpected DeepSeek identity: %#v", session)
	}
	if session.RecordCount != 9 || session.OmittedCount != 7 || session.FilteredUserInput != 1 || session.ToolCallCount != 1 {
		t.Fatalf("unexpected DeepSeek counts: %#v", session)
	}
	if session.UserMessages != 1 || session.AssistantMessages != 1 || len(session.Messages) != 2 {
		t.Fatalf("unexpected DeepSeek conversation: %#v", session.Messages)
	}
	if strings.Contains(session.Messages[0].Text, "hidden") || strings.Contains(session.Messages[1].Text, "reasoning") {
		t.Fatalf("internal DeepSeek content leaked into conversation: %#v", session.Messages)
	}
	if len(session.Messages[0].Attachments) != 1 || session.Messages[0].Attachments[0].ExpectedHash != imageHash {
		t.Fatalf("DeepSeek image reference was not preserved: %#v", session.Messages[0].Attachments)
	}
}

func TestDeepSeekAttachmentRejectsNativeObjectIntegrityMismatch(t *testing.T) {
	home := t.TempDir()
	declaredHash := digestBytes([]byte("declared bytes"))
	actual := []byte("different bytes")
	writeDeepSeekAttachment(t, home, declaredHash, actual)
	hashMismatch, err := deepSeekAttachment(&deepSeekAttachmentRef{
		AttachmentID: declaredHash, MediaType: "image/png", Bytes: int64(len(actual)), Name: "image.png",
	}, home)
	if err != nil {
		t.Fatal(err)
	}
	resolveAttachment(context.Background(), Session{}, Repository{}, &hashMismatch)
	if hashMismatch.Status != attachmentStatusUnavailable || hashMismatch.ContentHash != "" || len(hashMismatch.Data) != 0 {
		t.Fatalf("hash-mismatched DeepSeek object was archived: %#v", hashMismatch)
	}

	actualHash := digestBytes(actual)
	writeDeepSeekAttachment(t, home, actualHash, actual)
	sizeMismatch, err := deepSeekAttachment(&deepSeekAttachmentRef{
		AttachmentID: actualHash, MediaType: "image/png", Bytes: int64(len(actual) + 1), Name: "image.png",
	}, home)
	if err != nil {
		t.Fatal(err)
	}
	resolveAttachment(context.Background(), Session{}, Repository{}, &sizeMismatch)
	if sizeMismatch.Status != attachmentStatusUnavailable || sizeMismatch.ContentHash != "" || len(sizeMismatch.Data) != 0 {
		t.Fatalf("size-mismatched DeepSeek object was archived: %#v", sizeMismatch)
	}
}

func TestDeepSeekExportIsOptInIdempotentAndListable(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "remote", "add", "origin", "https://github.com/example/deepseek-project.git")
	home := filepath.Join(root, "dsh")
	created := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	records := []map[string]any{
		deepSeekTestEvent(0, created.Add(time.Second), "user/message", map[string]any{
			"id": "human", "role": "user", "source": map[string]any{"kind": "user"},
			"content": []any{map[string]any{"type": "text", "text": "Add DeepSeek support"}},
		}, "append"),
		deepSeekTestEvent(1, created.Add(2*time.Second), "session/title", map[string]any{
			"title": "DeepSeek support", "messageSeqs": []any{0}, "source": map[string]any{"kind": "fallback"},
		}, nil),
		deepSeekTestEvent(2, created.Add(3*time.Second), "assistant/message", map[string]any{
			"turn": 0, "step": 0,
			"message": map[string]any{
				"id": "assistant", "role": "assistant",
				"source":  map[string]any{"kind": "model", "provider": "deepseek", "model": "fixture"},
				"content": []any{map[string]any{"type": "text", "text": "Implemented."}},
			},
		}, "append"),
	}
	raw := deepSeekTestLog(t, "session-export", repo, created, 0, records, true)
	writeDeepSeekLog(t, home, "session-export", raw, true)
	output := filepath.Join(root, "archive")
	opts := Options{
		CodexHome: filepath.Join(root, "empty-codex"), DeepSeekHome: home,
		Output: output, AllRepos: true, IncludeDeepSeek: true,
		DeviceID: "device:test", DeviceName: "test-device", StabilityWindow: -1,
	}

	withoutDeepSeek := opts
	withoutDeepSeek.IncludeDeepSeek = false
	withoutDeepSeek.Output = filepath.Join(root, "codex-only")
	result, err := Export(context.Background(), withoutDeepSeek)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources != 0 || result.Created != 0 {
		t.Fatalf("DeepSeek was not opt-in: %#v", result)
	}

	first, err := Export(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sources != 1 || first.Created != 1 || len(first.Changes) != 1 || first.Changes[0].Harness != harnessDeepSeek {
		t.Fatalf("unexpected first DeepSeek export: %#v", first)
	}
	document, err := os.ReadFile(first.Changes[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	markdown := string(document)
	for _, expected := range []string{`harness: "deepseek"`, "Exported from DeepSeek Harness", "Add DeepSeek support", "Implemented."} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("DeepSeek Markdown is missing %q:\n%s", expected, markdown)
		}
	}
	if strings.Contains(markdown, "session-export") {
		t.Fatalf("DeepSeek Markdown exposes native identity:\n%s", markdown)
	}
	if !strings.Contains(filepath.Base(filepath.Dir(first.Changes[0].Path)), "deepseek--") {
		t.Fatalf("DeepSeek semantic session path lacks its harness namespace: %s", first.Changes[0].Path)
	}
	var metadata sessionMetadata
	if err := readMetadata(filepath.Join(filepath.Dir(first.Changes[0].Path), sessionMetadataName), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.SchemaVersion != SessionMetadataSchema || metadata.Harness != harnessDeepSeek ||
		metadata.SessionKey != sessionKey("device:test", harnessDeepSeek, "session-export") {
		t.Fatalf("unexpected DeepSeek sidecar: %#v", metadata)
	}
	repeated, err := Export(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Created != 0 || repeated.Unchanged != 1 || len(repeated.Changes) != 0 {
		t.Fatalf("DeepSeek re-export was not idempotent: %#v", repeated)
	}
	entries, err := List(ListOptions{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Harness != harnessDeepSeek {
		t.Fatalf("DeepSeek list provenance was lost: %#v", entries)
	}
	stored, err := os.ReadFile(filepath.Join(home, "sessions", "--fixture--", "session-export", deepSeekCompressedFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, raw) {
		t.Fatal("export modified the raw DeepSeek source")
	}
}

func TestDeepSeekSubagentAndPluginOnlySessionsAreInternal(t *testing.T) {
	created := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	human := []map[string]any{deepSeekTestEvent(0, created, "user/message", map[string]any{
		"id": "human", "role": "user", "source": map[string]any{"kind": "user"},
		"content": []any{map[string]any{"type": "text", "text": "child task"}},
	}, "append")}
	subagent, err := parseDeepSeekSession(deepSeekTestLog(t, "child", t.TempDir(), created, 1, human, false), false, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if subagent.ExcludeReason != "subagent" {
		t.Fatalf("DeepSeek child was not excluded: %#v", subagent)
	}

	plugin := []map[string]any{deepSeekTestEvent(0, created, "user/message", map[string]any{
		"id": "plugin", "role": "user", "source": map[string]any{"kind": "plugin", "plugin": "instructions"},
		"content": []any{map[string]any{"type": "text", "text": "runtime context"}},
	}, "append")}
	pluginOnly, err := parseDeepSeekSession(deepSeekTestLog(t, "plugin", t.TempDir(), created, 0, plugin, false), false, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if pluginOnly.ExcludeReason != "runtime_context" || pluginOnly.UserMessages != 0 {
		t.Fatalf("DeepSeek plugin-only session was not excluded: %#v", pluginOnly)
	}
}

func TestDeepSeekRejectsTornCorruptAndDiscontinuousLogs(t *testing.T) {
	created := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	records := []map[string]any{deepSeekTestEvent(0, created, "user/message", map[string]any{
		"id": "human", "role": "user", "source": map[string]any{"kind": "user"},
		"content": []any{map[string]any{"type": "text", "text": "hello"}},
	}, "append")}
	raw := deepSeekTestLog(t, "session-errors", t.TempDir(), created, 0, records, true)
	if _, err := parseDeepSeekSession(raw[:len(raw)-1], true, t.TempDir()); !errors.Is(err, errSourceBusy) {
		t.Fatalf("torn final frame was not busy: %v", err)
	}
	corrupt := append([]byte(nil), raw...)
	corrupt[len(corrupt)-5] ^= 0xff
	if _, err := parseDeepSeekSession(corrupt, true, t.TempDir()); err == nil || errors.Is(err, errSourceBusy) {
		t.Fatalf("checksum corruption was not rejected as corruption: %v", err)
	}

	plain := deepSeekTestLog(t, "session-no-checksum", t.TempDir(), created, 0, records, false)
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(false), zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	withoutChecksum := encoder.EncodeAll(plain, nil)
	encoder.Close()
	if _, err := parseDeepSeekSession(withoutChecksum, true, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no checksum") {
		t.Fatalf("checksum-free frame was not rejected: %v", err)
	}

	partialPlain := append(append([]byte(nil), plain...), []byte(`{"type":"user/message"`)...)
	encoder, err = zstd.NewWriter(nil, zstd.WithEncoderCRC(true), zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	partialJSON := encoder.EncodeAll(partialPlain, nil)
	encoder.Close()
	if _, err := parseDeepSeekSession(partialJSON, true, t.TempDir()); !errors.Is(err, errSourceBusy) {
		t.Fatalf("complete frame with a partial JSONL tail was not busy: %v", err)
	}

	discontinuous := []map[string]any{deepSeekTestEvent(1, created, "user/message", map[string]any{
		"id": "human", "role": "user", "source": map[string]any{"kind": "user"},
		"content": []any{map[string]any{"type": "text", "text": "hello"}},
	}, "append")}
	if _, err := parseDeepSeekSession(deepSeekTestLog(t, "session-gap", t.TempDir(), created, 0, discontinuous, false), false, t.TempDir()); err == nil || !strings.Contains(err.Error(), "discontinuity") {
		t.Fatalf("sequence discontinuity was not rejected: %v", err)
	}
}

func TestDeepSeekRejectsMissingHeaderFieldsAndDirectoryMismatch(t *testing.T) {
	created := time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)
	records := []map[string]any{deepSeekTestEvent(0, created, "user/message", map[string]any{
		"id": "human", "role": "user", "source": map[string]any{"kind": "user"},
		"content": []any{map[string]any{"type": "text", "text": "hello"}},
	}, "append")}
	plain := deepSeekTestLog(t, "missing-created", t.TempDir(), created, 0, records, false)
	first, remaining, _ := bytes.Cut(plain, []byte{'\n'})
	var header map[string]any
	if err := json.Unmarshal(first, &header); err != nil {
		t.Fatal(err)
	}
	delete(header, "createdAt")
	missingCreated := append(marshalDeepSeekLine(t, header), '\n')
	missingCreated = append(missingCreated, remaining...)
	if _, err := parseDeepSeekSession(missingCreated, false, t.TempDir()); err == nil || !strings.Contains(err.Error(), "missing createdAt") {
		t.Fatalf("missing createdAt was not rejected: %v", err)
	}

	root := t.TempDir()
	home := filepath.Join(root, "dsh")
	writeDeepSeekLog(t, home, "directory-id", deepSeekTestLog(t, "header-id", root, created, 0, records, false), false)
	result, err := Export(context.Background(), Options{
		CodexHome: filepath.Join(root, "empty-codex"), DeepSeekHome: home,
		Output: filepath.Join(root, "archive"), AllRepos: true, IncludeDeepSeek: true,
		DeviceID: "device:test", DeviceName: "test-device", StabilityWindow: -1,
	})
	if err == nil || result.Skipped != 1 || len(result.Changes) != 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "does not match header ID") {
		t.Fatalf("directory/header mismatch was not rejected: result=%#v err=%v", result, err)
	}
}

func deepSeekTestEvent(seq int, at time.Time, eventType string, data map[string]any, surface any) map[string]any {
	result := map[string]any{"type": eventType, "seq": seq, "time": at.UnixMilli(), "data": data}
	if surface != nil {
		result["surfaceOp"] = surface
	}
	return result
}

func deepSeekTestLog(t *testing.T, id, cwd string, created time.Time, depth int, records []map[string]any, compressed bool) []byte {
	t.Helper()
	header := map[string]any{
		"type": "session", "version": deepSeekSessionFormat, "id": id,
		"createdAt": created.UnixMilli(), "cwd": cwd, "delegationDepth": depth, "agentPreset": "standard",
	}
	if depth > 0 {
		header["origin"] = "subagent"
		header["parentSession"] = "parent"
	}
	frames := make([][]byte, 0, 2)
	frames = append(frames, append(marshalDeepSeekLine(t, header), '\n'))
	var events []byte
	for _, record := range records {
		events = append(events, marshalDeepSeekLine(t, record)...)
		events = append(events, '\n')
	}
	frames = append(frames, events)
	if !compressed {
		return bytes.Join(frames, nil)
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true), zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	var output []byte
	for _, frame := range frames {
		output = append(output, encoder.EncodeAll(frame, nil)...)
	}
	return output
}

func marshalDeepSeekLine(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeDeepSeekLog(t *testing.T, home, id string, raw []byte, compressed bool) string {
	t.Helper()
	name := deepSeekPlainFilename
	if compressed {
		name = deepSeekCompressedFilename
	}
	path := filepath.Join(home, "sessions", "--fixture--", id, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeDeepSeekAttachment(t *testing.T, home, hash string, data []byte) string {
	t.Helper()
	digestHex := strings.TrimPrefix(hash, "sha256:")
	path := filepath.Join(home, "attachments", "v1", "objects", digestHex[:2], digestHex)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
