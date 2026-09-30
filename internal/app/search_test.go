package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sessionmgr/sessionmgr/internal/archive"
	"github.com/sessionmgr/sessionmgr/internal/config"
)

func TestSearchShowCustomPathsAndConfigRemainReadOnly(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "native with spaces", "sessions")
	if err := os.MkdirAll(native, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"session_meta","timestamp":"2026-08-05T01:00:00Z","payload":{"id":"query-cli","cwd":"/deleted","git":{"repository_url":"https://github.com/example/query-cli.git"}}}
{"type":"event_msg","timestamp":"2026-08-05T01:00:01Z","payload":{"type":"user_message","message":"Review transformer architecture"}}
{"type":"event_msg","timestamp":"2026-08-05T01:00:02Z","payload":{"type":"agent_message","message":"selected-answer body"}}
`
	if err := os.WriteFile(filepath.Join(native, "fixture.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	t.Setenv("SESSIONMGR_CONFIG", configPath)
	store := config.Store{Path: configPath}
	if _, err := store.SetExportDirectory(filepath.Join(root, "saved archive")); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "one-off archive")
	run := func(args ...string) []byte {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code, err := Run(context.Background(), args, &stdout, &stderr)
		if err != nil || code != 0 {
			t.Fatalf("%v: code=%d err=%v stderr=%s", args, code, err, stderr.String())
		}
		return stdout.Bytes()
	}
	run("export", "--session", "query-cli", "--sources", "codex", "--codex-home", filepath.Dir(native), "--output", output, "--json")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var result archive.SearchResult
	if err := json.Unmarshal(run("search", "TRANSFORMER", "--directory", output, "--since", "2026-08-05", "--until", "2026-08-05", "--timezone", "UTC", "--json"), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Matches[0].SessionID != "query-cli" {
		t.Fatalf("custom path and mixed-position flags: %+v", result)
	}
	var evidence archive.EvidenceResult
	if err := json.Unmarshal(run("show", "--directory", output, "--key", result.Matches[0].SessionKey, "--from-line", "1", "--to-line", "5", "--json"), &evidence); err != nil {
		t.Fatal(err)
	}
	if !evidence.Verified || len(evidence.Lines) != 5 || evidence.SnapshotThrough != "2026-08-05T01:00:02Z" {
		t.Fatalf("show evidence: %+v", evidence)
	}
	if err := json.Unmarshal(run("search", "selected-answer", "--output", output, "--repo", "query-cli", "--content", "--json"), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Matches[0].MatchedIn != "content" {
		t.Fatalf("bounded content query: %+v", result)
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("search/show changed saved configuration")
	}
	if loaded, err := store.Load(); err != nil || !strings.HasSuffix(loaded.ExportDirectory, "saved archive") {
		t.Fatal("one-off export changed remembered path")
	}
}

func TestSearchDateBoundsAndArgumentErrors(t *testing.T) {
	location, err := time.LoadLocation("America/Detroit")
	if err != nil {
		t.Fatal(err)
	}
	start, err := parseSearchTime("2026-03-08", false, location)
	if err != nil {
		t.Fatal(err)
	}
	end, err := parseSearchTime("2026-03-08", true, location)
	if err != nil {
		t.Fatal(err)
	}
	if end.Sub(start)+time.Nanosecond != 23*time.Hour {
		t.Fatal("date-only upper bound ignored DST")
	}
	for _, value := range []string{"tomorrow", "2026-09-30T14:00:00"} {
		if _, err := parseSearchTime(value, false, location); err == nil {
			t.Fatalf("ambiguous time accepted: %s", value)
		}
	}
	for _, args := range [][]string{{"search", "topic", "--query", "other"}, {"show", "extra"}, {"search", "--timezone", "not-a-zone"}} {
		var stdout, stderr bytes.Buffer
		code, err := Run(context.Background(), args, &stdout, &stderr)
		if err == nil || code == 0 {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
}
