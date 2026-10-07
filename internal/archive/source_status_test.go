package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAbortedCodexIsIncompleteNotBusy(t *testing.T) {
	raw := []byte("{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_started\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"turn_aborted\"}}\n")
	_, err := completedNativePrefix(harnessCodex, raw)
	if !errors.Is(err, errSourceIncomplete) || sourceErrorIsBusy(err) {
		t.Fatalf("aborted classification: %v", err)
	}
}

func TestAutomatedDirectoryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{filepath.Join(os.TempDir(), "pocket-eval-job", "agent"), true},
		{filepath.Join("/private"+os.TempDir(), "pocket-eval-job", "agent"), true},
		{filepath.Join(os.TempDir(), "pocket-debug-eval-job", "agent"), true},
		{"/private/tmp/claude-501/project/scratchpad/probe", true},
		{"/Users/owner/Desktop/pocket-eval-project", false},
		{filepath.Join(os.TempDir(), "normal-user-project"), false},
	} {
		if got := automatedOpenCodeDirectory(tc.path); got != tc.want {
			t.Errorf("%s: %v", tc.path, got)
		}
	}
}

func TestOldIncompleteUpdatesBecomeEligibleAgain(t *testing.T) {
	opts := checkpointOptions(t)
	raw := `{"type":"session_meta","payload":{"id":"old-incomplete","cwd":"/missing","git":{"repository_url":"https://github.com/example/incremental.git"}}}
{"type":"event_msg","payload":{"type":"task_started"}}
`
	path := incrementalFixturePath(opts, "old-incomplete")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	ageSource(t, path, 3*time.Hour)
	first := mustExport(t, opts)
	second := mustExport(t, opts)
	if first.Incomplete != 1 || second.Incomplete != 1 || second.ScannedSources != 0 {
		t.Fatalf("retry not deferred: %+v / %+v", first, second)
	}
	writeSessionFixture(t, opts.CodexHome, "old-incomplete", "https://github.com/example/incremental.git", "now complete")
	third, err := Export(context.Background(), opts)
	if err != nil || third.Created != 1 || third.Incomplete != 0 {
		t.Fatalf("update lost: %+v / %v", third, err)
	}
}

func TestAutomatedOpenCodeWithoutParentIsFilteredBeforeCompletion(t *testing.T) {
	db := filepath.Join(t.TempDir(), "opencode.db")
	makeOpenCodeFixture(t, db, filepath.Join(os.TempDir(), "pocket-eval-test", "agent"))
	got, err := readOpenCodeSessions(context.Background(), db, 0)
	if err != nil || got.busy != 0 || len(got.incomplete) != 0 || len(got.sessions) != 2 {
		t.Fatalf("%+v / %v", got, err)
	}
	for _, s := range got.sessions {
		if s.ExcludeReason != "automated_cli" {
			t.Fatalf("unfiltered: %+v", s)
		}
	}
}

func TestReadableCopySupersedesAbortedSource(t *testing.T) {
	opts := checkpointOptions(t)
	writeSessionFixture(t, opts.CodexHome, "duplicate", "https://github.com/example/incremental.git", "complete copy")
	raw := `{"type":"session_meta","payload":{"id":"duplicate","cwd":"/missing","git":{"repository_url":"https://github.com/example/incremental.git"}}}
{"type":"event_msg","payload":{"type":"task_started"}}
{"type":"event_msg","payload":{"type":"turn_aborted"}}
`
	if err := os.WriteFile(filepath.Join(filepath.Dir(incrementalFixturePath(opts, "duplicate")), "rollout-old-copy.jsonl"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	first := mustExport(t, opts)
	if first.Created != 1 || first.Incomplete != 0 || first.Busy != 0 {
		t.Fatalf("copy inflated count or blocked export: %+v", first)
	}
	second := mustExport(t, opts)
	if second.Incomplete != 0 || second.Busy != 0 {
		t.Fatalf("copy repeated: %+v", second)
	}
}

func TestCodexCompletionAfterAbortIsNotLost(t *testing.T) {
	raw := []byte("{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_started\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"turn_aborted\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_started\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_complete\"}}\n")
	got, err := completedNativePrefix(harnessCodex, raw)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("later complete turn lost: %s / %v", got, err)
	}
}
