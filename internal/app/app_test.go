package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"archive me"}}
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
		Created int `json:"created"`
		Changes []struct {
			Kind string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &exported); err != nil || exported.Created != 1 || len(exported.Changes) != 1 || exported.Changes[0].Kind != "new" {
		t.Fatalf("unexpected export JSON: %s (%v)", stdout.String(), err)
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
