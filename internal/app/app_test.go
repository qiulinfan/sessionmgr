package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sessionmgr/sessionmgr/internal/archive"
)

func TestConfiguredExportShowsOnlyCurrentChanges(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "sessions")
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"cli-session","cwd":"/missing","git":{"repository_url":"git@github.com:example/cli.git"}}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"archive me","attachments":[{"filename":"cli-note.txt","mime_type":"text/plain","file_data":"Y2xpIG5vdGUK"}]}}
{"timestamp":"2026-08-05T01:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"archived"}}
`
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONMGR_CONFIG", filepath.Join(root, "config.json"))

	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{"config", "set-directory", output}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("config failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code, err = Run(context.Background(), []string{"export", "--all", "--codex-home", codexHome, "--json"}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var exported struct {
		Created             int `json:"created"`
		Attachments         int `json:"attachments"`
		ArchivedAttachments int `json:"archived_attachments"`
		Changes             []struct {
			Kind string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &exported); err != nil || exported.Created != 1 || exported.Attachments != 1 || exported.ArchivedAttachments != 1 || len(exported.Changes) != 1 || exported.Changes[0].Kind != "new" {
		t.Fatalf("unexpected export JSON: %s (%v)", stdout.String(), err)
	}
	attachmentMatches, err := filepath.Glob(filepath.Join(output, "github.com-example", "cli", "*", "*", "attachments", "001-cli-note.txt"))
	if err != nil || len(attachmentMatches) != 1 {
		t.Fatalf("CLI attachment path missing: %v, %v", attachmentMatches, err)
	}

	stdout.Reset()
	stderr.Reset()
	code, err = Run(context.Background(), []string{"export", "--all", "--codex-home", codexHome}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("repeat export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	if stdout.String() != "No changes.\n" {
		t.Fatalf("repeat export showed old records: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code, err = Run(context.Background(), []string{"list", "--json"}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("list failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var listed struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != "cli-session" {
		t.Fatalf("unexpected list JSON: %s", stdout.String())
	}
}

func TestHumanChangesUseSemanticFieldsWithoutHashes(t *testing.T) {
	var output bytes.Buffer
	err := printChanges(&output, []archive.Change{{
		Kind: "new", RepositoryName: "project", Title: "Readable title", DeviceName: "workstation",
		SessionID: "native-session", SessionKey: "sha256:session-key", DocumentHash: "sha256:document",
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, hidden := range []string{"HASH", "sha256", "native-session"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("human output exposed %q: %s", hidden, text)
		}
	}
	for _, visible := range []string{"Readable title", "workstation", "project"} {
		if !strings.Contains(text, visible) {
			t.Fatalf("human output omitted %q: %s", visible, text)
		}
	}
}

func TestCleanupHumanOutputIsExplicitlyDryRun(t *testing.T) {
	var output bytes.Buffer
	result := archive.CleanupResult{
		DryRun: true, Candidates: 1,
		Changes: []archive.CleanupChange{{
			Kind: "remove", Reason: "subagent", RepositoryName: "project",
			DeviceName: "workstation", SessionID: "native-session",
			SessionKey: "sha256:hidden", Title: "Internal review",
		}},
	}
	if err := printCleanupChanges(&output, result); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{"REMOVE", "Internal review", "subagent", "Dry run", "--apply"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("cleanup output omitted %q: %s", expected, text)
		}
	}
	for _, hidden := range []string{"native-session", "sha256:hidden"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("cleanup output exposed %q: %s", hidden, text)
		}
	}
}

func TestCLIReportsBusySessionWithoutFailing(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "active.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"timestamp":"2026-08-05T01:00:00Z"`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONMGR_CONFIG", filepath.Join(root, "config.json"))
	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{
		"export", "--directory", filepath.Join(root, "exports"),
		"--codex-home", codexHome, "--json",
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("busy export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var result struct {
		Busy    int `json:"busy"`
		Skipped int `json:"skipped"`
		Created int `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Busy != 1 || result.Skipped != 0 || result.Created != 0 {
		t.Fatalf("unexpected busy JSON: %s", stdout.String())
	}
}
