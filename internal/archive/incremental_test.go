package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func checkpointOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	opts := testExportOptions(filepath.Join(root, "codex"), filepath.Join(root, "archive"))
	opts.CheckpointPath = filepath.Join(root, "local-config", "export-state.json")
	return opts
}

func incrementalFixturePath(opts Options, id string) string {
	return filepath.Join(opts.CodexHome, "sessions", "2026", "08", "05", "rollout-"+id+".jsonl")
}

func ageSource(t *testing.T, path string, ago time.Duration) {
	t.Helper()
	stamp := time.Now().Add(-ago)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func mustExport(t *testing.T, opts Options) Result {
	t.Helper()
	result, err := Export(context.Background(), opts)
	if err != nil {
		t.Fatalf("export failed: %v; warnings: %v", err, result.Warnings)
	}
	return result
}

func TestIncrementalExportOldSourcesNewBackdatedSourceAndOldTimestampUpdate(t *testing.T) {
	opts := checkpointOptions(t)
	writeSessionFixture(t, opts.CodexHome, "old", "https://github.com/example/incremental.git", "first")
	writeTitles(t, opts.CodexHome, titleLine("old", "Existing session", "2026-08-05T02:00:00Z"))
	ageSource(t, incrementalFixturePath(opts, "old"), 3*time.Hour)
	first := mustExport(t, opts)
	if first.Created != 1 || first.Incremental || first.LastExportAt == "" {
		t.Fatalf("first scan: %+v", first)
	}
	second := mustExport(t, opts)
	if second.ScannedSources != 0 || second.IgnoredUnchanged != 1 || !second.Incremental || second.Created != 0 {
		t.Fatalf("old unchanged payload was reread: %+v", second)
	}
	previous, err := time.Parse(time.RFC3339Nano, first.LastExportAt)
	if err != nil {
		t.Fatal(err)
	}
	cutoff, err := time.Parse(time.RFC3339Nano, second.Since)
	if err != nil || !cutoff.Equal(previous.Add(-time.Hour)) {
		t.Fatalf("overlap cutoff: %s, %v", second.Since, err)
	}
	writeSessionFixture(t, opts.CodexHome, "copied", "https://github.com/example/incremental.git", "backdated new session")
	writeTitles(t, opts.CodexHome,
		titleLine("old", "Existing session", "2026-08-05T02:00:00Z"),
		titleLine("copied", "Copied session", "2026-08-05T02:00:00Z"),
	)
	ageSource(t, incrementalFixturePath(opts, "copied"), 48*time.Hour)
	newSession := mustExport(t, opts)
	if newSession.ScannedSources != 1 || newSession.Created != 1 || newSession.Changes[0].SessionID != "copied" {
		t.Fatalf("unseen backdated session was missed: %+v", newSession)
	}
	writeSessionFixture(t, opts.CodexHome, "old", "https://github.com/example/incremental.git", "updated despite older mtime")
	ageSource(t, incrementalFixturePath(opts, "old"), 48*time.Hour)
	updated := mustExport(t, opts)
	if updated.ScannedSources != 1 || updated.Created != 1 || updated.Changes[0].Kind != "updated" {
		t.Fatalf("changed old-timestamp source was missed: %+v", updated)
	}
	document, err := os.ReadFile(updated.Changes[0].Path)
	if err != nil || !strings.Contains(string(document), "updated despite older mtime") {
		t.Fatalf("updated document: %v", err)
	}
	latest, err := LastExportTime(opts.CheckpointPath, opts.Output)
	if err != nil || latest.IsZero() {
		t.Fatalf("persisted export time: %v, %v", latest, err)
	}
	if _, err := os.Stat(filepath.Join(opts.Output, "export-state.json")); !os.IsNotExist(err) {
		t.Fatal("local state leaked into archive")
	}
}

func TestIncrementalExportRechecksOverlapAndAllowsFullScan(t *testing.T) {
	opts := checkpointOptions(t)
	writeSessionFixture(t, opts.CodexHome, "recent", "https://github.com/example/incremental.git", "recent answer")
	ageSource(t, incrementalFixturePath(opts, "recent"), 30*time.Minute)
	mustExport(t, opts)
	overlap := mustExport(t, opts)
	if overlap.ScannedSources != 1 || overlap.Unchanged != 1 || overlap.IgnoredUnchanged != 0 {
		t.Fatalf("overlap was not reread: %+v", overlap)
	}
	ageSource(t, incrementalFixturePath(opts, "recent"), 3*time.Hour)
	mustExport(t, opts)
	old := mustExport(t, opts)
	if old.ScannedSources != 0 {
		t.Fatalf("old source was not omitted: %+v", old)
	}
	opts.FullScan = true
	full := mustExport(t, opts)
	if full.Incremental || full.ScannedSources != 1 || full.Unchanged != 1 {
		t.Fatalf("full scan did not bypass cutoff: %+v", full)
	}
}

func TestIncrementalExportRetriesOldBusyAndMalformedSources(t *testing.T) {
	opts := checkpointOptions(t)
	writeSessionFixture(t, opts.CodexHome, "good", "https://github.com/example/incremental.git", "good answer")
	writeTitles(t, opts.CodexHome,
		titleLine("good", "Good session", "2026-08-05T02:00:00Z"),
		titleLine("bad", "Recovered session", "2026-08-05T02:00:00Z"),
	)
	busy := `{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"busy","cwd":"/missing","git":{"repository_url":"https://github.com/example/incremental.git"}}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"task_started"}}
{"timestamp":"2026-08-05T01:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"still working"}}
`
	for id, text := range map[string]string{"busy": busy, "bad": "not JSON\n"} {
		if err := os.WriteFile(incrementalFixturePath(opts, id), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"good", "busy", "bad"} {
		ageSource(t, incrementalFixturePath(opts, id), 3*time.Hour)
	}
	first, err := Export(context.Background(), opts)
	if err == nil || first.Created != 1 || first.Skipped != 1 || first.Busy != 1 || first.LastExportAt == "" {
		t.Fatalf("partial first export: %+v, %v", first, err)
	}
	second, err := Export(context.Background(), opts)
	if err == nil || second.ScannedSources != 2 || second.IgnoredUnchanged != 1 || second.Skipped != 1 || second.Busy != 1 {
		t.Fatalf("old pending sources were dropped: %+v, %v", second, err)
	}
	writeSessionFixture(t, opts.CodexHome, "bad", "https://github.com/example/incremental.git", "repaired")
	ageSource(t, incrementalFixturePath(opts, "bad"), 48*time.Hour)
	repaired := mustExport(t, opts)
	if repaired.Created != 1 || repaired.Busy != 1 || repaired.Changes[0].SessionID != "bad" {
		t.Fatalf("pending repair: %+v", repaired)
	}
}

func TestIncrementalExportRetriesPublicationConflictWithoutOverwriting(t *testing.T) {
	opts := checkpointOptions(t)
	writeSessionFixture(t, opts.CodexHome, "edited", "https://github.com/example/incremental.git", "original")
	ageSource(t, incrementalFixturePath(opts, "edited"), 3*time.Hour)
	first := mustExport(t, opts)
	path := first.Changes[0].Path
	if err := os.WriteFile(path, []byte("user edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSessionFixture(t, opts.CodexHome, "edited", "https://github.com/example/incremental.git", "new native content")
	ageSource(t, incrementalFixturePath(opts, "edited"), 3*time.Hour)
	for i := 0; i < 2; i++ {
		result, err := Export(context.Background(), opts)
		if err == nil || result.Skipped != 1 || result.ScannedSources != 1 || result.LastExportAt == "" {
			t.Fatalf("conflict retry %d: %+v, %v", i, result, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "user edit\n" {
		t.Fatal("incremental retry overwrote an edited archive")
	}
}

func TestIncrementalExportReadsWholeFragmentBundleWhenOneMemberChanges(t *testing.T) {
	opts := checkpointOptions(t)
	const id = "incremental-fragments"
	const remote = "https://github.com/example/fragments.git"
	writeTitles(t, opts.CodexHome, titleLine(id, "Incremental fragment bundle", "2026-08-05T02:00:00Z"))
	first := writeCodexFragmentFixture(t, opts.CodexHome, "first.jsonl", id, remote, "2026-08-05T01:00:00Z", "first user", "first answer")
	second := writeCodexFragmentFixture(t, opts.CodexHome, "second.jsonl", id, remote, "2026-08-05T01:02:00Z", "second user", "second answer")
	ageSource(t, first, 3*time.Hour)
	ageSource(t, second, 3*time.Hour)
	mustExport(t, opts)
	ignored := mustExport(t, opts)
	if ignored.ScannedSources != 0 || ignored.IgnoredUnchanged != 2 {
		t.Fatalf("old bundle scan: %+v", ignored)
	}
	appendCodexFragmentMessages(t, second, "2026-08-05T01:04:00Z", "new fragment user", "new fragment answer")
	updated := mustExport(t, opts)
	if updated.ScannedSources != 2 || updated.Created != 1 {
		t.Fatalf("fragment selection: %+v", updated)
	}
	document, err := os.ReadFile(updated.Changes[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	assertFragmentMarkersOnce(t, string(document), "first user", "first answer", "second user", "second answer", "new fragment user", "new fragment answer")
	third := writeCodexFragmentFixture(t, opts.CodexHome, "third.jsonl", id, remote, "2026-08-05T01:06:00Z", "third user", "third answer")
	ageSource(t, second, 3*time.Hour)
	ageSource(t, third, 48*time.Hour)
	added := mustExport(t, opts)
	if added.ScannedSources != 3 || added.Created != 1 {
		t.Fatalf("new backdated fragment lost old members: %+v", added)
	}
	document, err = os.ReadFile(added.Changes[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	assertFragmentMarkersOnce(t, string(document), "first user", "second user", "new fragment user", "third user")
}

func TestIncrementalExportFindsTitleOnlyRename(t *testing.T) {
	opts := checkpointOptions(t)
	writeSessionFixture(t, opts.CodexHome, "renamed", "https://github.com/example/incremental.git", "answer")
	ageSource(t, incrementalFixturePath(opts, "renamed"), 48*time.Hour)
	writeTitles(t, opts.CodexHome, titleLine("renamed", "Original title", "2026-08-05T02:00:00Z"))
	mustExport(t, opts)
	writeTitles(t, opts.CodexHome, titleLine("renamed", "New title", "2026-08-05T03:00:00Z"))
	renamed := mustExport(t, opts)
	if renamed.ScannedSources != 1 || renamed.Created != 1 || renamed.Changes[0].Kind != "renamed" || renamed.Changes[0].Title != "New title" {
		t.Fatalf("title-only rename was missed: %+v", renamed)
	}
}

func TestIncrementalExportScopesDestinationAndNonGitPolicy(t *testing.T) {
	opts := checkpointOptions(t)
	missing := filepath.Join(filepath.Dir(opts.Output), "deleted-workspace")
	writeSessionFixtureWithCWD(t, opts.CodexHome, "deleted", missing, "", "keep deleted workspace")
	ageSource(t, incrementalFixturePath(opts, "deleted"), 48*time.Hour)
	filtered := mustExport(t, opts)
	if filtered.FilteredNonGit != 1 || filtered.Created != 0 {
		t.Fatalf("default non-Git policy: %+v", filtered)
	}
	opts.IncludeNonGit = true
	included := mustExport(t, opts)
	if included.Incremental || included.Created != 1 {
		t.Fatalf("new non-Git selection reused old cutoff: %+v", included)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("export recreated the deleted native directory")
	}
	opts.Output += "-other"
	other := mustExport(t, opts)
	if other.Incremental || other.Created != 1 {
		t.Fatalf("new destination reused old cutoff: %+v", other)
	}
	if err := os.RemoveAll(opts.Output); err != nil {
		t.Fatal(err)
	}
	recreated := mustExport(t, opts)
	if recreated.Incremental || recreated.Created != 1 {
		t.Fatalf("recreated destination was missed: %+v", recreated)
	}
}

func TestIncrementalOpenCodeSkipsOldPayloadAndDetectsPartUpdate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "opencode.db")
	makeOpenCodeFixture(t, path, filepath.Join(root, "deleted-work"))
	opts := Options{
		OpenCodeDB: path, Sources: &SourceSelection{OpenCode: true}, Output: filepath.Join(root, "archive"),
		AllRepos: true, IncludeNonGit: true, DeviceID: "device:test", DeviceName: "test-device",
		CheckpointPath: filepath.Join(root, "config", "export-state.json"), StabilityWindow: -1,
	}
	first := mustExport(t, opts)
	if first.Created != 1 {
		t.Fatalf("deleted OpenCode directory: %+v", first)
	}
	second := mustExport(t, opts)
	if second.ScannedSources != 0 || second.IgnoredUnchanged != 2 {
		t.Fatalf("OpenCode reread old messages: %+v", second)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE part SET data = '{"type":"text","text":"updated part only"}', time_updated = ? WHERE id = 'part-answer'`, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	updated := mustExport(t, opts)
	if updated.ScannedSources != 1 || updated.IgnoredUnchanged != 1 || updated.Created != 1 {
		t.Fatalf("OpenCode part-only update missed: %+v", updated)
	}
	data, err := os.ReadFile(updated.Changes[0].Path)
	if err != nil || !strings.Contains(string(data), "updated part only") {
		t.Fatalf("updated OpenCode document: %v", err)
	}
}

func TestIncrementalCheckpointRejectsUnknownSchema(t *testing.T) {
	opts := checkpointOptions(t)
	if err := os.MkdirAll(filepath.Dir(opts.CheckpointPath), 0o700); err != nil {
		t.Fatal(err)
	}
	unknown := []byte(`{"schema_version":999,"scopes":{}}`)
	if err := os.WriteFile(opts.CheckpointPath, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), opts); err == nil {
		t.Fatal("unknown local checkpoint schema was overwritten")
	}
	data, err := os.ReadFile(opts.CheckpointPath)
	if err != nil || string(data) != string(unknown) {
		t.Fatal("unrecognized checkpoint was changed")
	}
}

func TestIncrementalExportDoesNotDropAnUnreadableFragmentFromArchive(t *testing.T) {
	opts := checkpointOptions(t)
	const id = "pending-bundle"
	const remote = "https://github.com/example/pending.git"
	writeTitles(t, opts.CodexHome, titleLine(id, "Pending bundle", "2026-08-05T02:00:00Z"))
	firstPath := writeCodexFragmentFixture(t, opts.CodexHome, "first.jsonl", id, remote, "2026-08-05T01:00:00Z", "first bundle user", "first bundle answer")
	secondPath := writeCodexFragmentFixture(t, opts.CodexHome, "second.jsonl", id, remote, "2026-08-05T01:02:00Z", "second bundle user", "second bundle answer")
	for _, path := range []string{firstPath, secondPath} {
		ageSource(t, path, 3*time.Hour)
	}
	first := mustExport(t, opts)
	documentPath := first.Changes[0].Path
	before, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("malformed fragment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ageSource(t, secondPath, 3*time.Hour)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := Export(context.Background(), opts)
		if err == nil || result.Skipped != 1 || result.Created != 0 || result.ScannedSources != 2 {
			t.Fatalf("incomplete bundle retry: %+v, %v", result, err)
		}
	}
	after, err := os.ReadFile(documentPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("unreadable fragment caused archived content to be dropped")
	}
	writeCodexFragmentFixture(t, opts.CodexHome, "second.jsonl", id, remote, "2026-08-05T01:02:00Z", "second bundle user", "repaired bundle answer")
	ageSource(t, secondPath, 48*time.Hour)
	repaired := mustExport(t, opts)
	if repaired.Created != 1 || repaired.ScannedSources != 2 {
		t.Fatalf("repaired bundle was not retried: %+v", repaired)
	}
}

func TestIncrementalArchiveLocalGitRoundTrip(t *testing.T) {
	opts := checkpointOptions(t)
	opts.IncludeNonGit = true
	writeSessionFixture(t, opts.CodexHome, "hosted", "https://github.com/example/roundtrip.git", "hosted conversation")
	writeSessionFixtureWithCWD(t, opts.CodexHome, "deleted", filepath.Join(filepath.Dir(opts.Output), "deleted-workspace"), "", "deleted workspace conversation")
	for _, id := range []string{"hosted", "deleted"} {
		ageSource(t, incrementalFixturePath(opts, id), 3*time.Hour)
	}
	first := mustExport(t, opts)
	if first.Created != 2 {
		t.Fatalf("initial roundtrip archive: %+v", first)
	}
	second := mustExport(t, opts)
	if second.ScannedSources != 0 || second.IgnoredUnchanged != 2 {
		t.Fatalf("roundtrip incremental no-op: %+v", second)
	}
	root := filepath.Dir(opts.Output)
	remote := filepath.Join(root, "remote.git")
	gitForTest(t, root, "init", "--bare", remote)
	gitForTest(t, root, "init", "-b", "main", opts.Output)
	gitForTest(t, opts.Output, "config", "user.name", "Session Manager fixture")
	gitForTest(t, opts.Output, "config", "user.email", "fixture@example.com")
	gitForTest(t, opts.Output, "add", ".")
	gitForTest(t, opts.Output, "commit", "-m", "Archive synthetic sessions")
	gitForTest(t, opts.Output, "remote", "add", "origin", remote)
	gitForTest(t, opts.Output, "push", "origin", "main")
	restored := filepath.Join(root, "restored")
	gitForTest(t, root, "clone", "--branch", "main", remote, restored)
	entries, err := List(ListOptions{Output: restored})
	if err != nil || len(entries) != 2 {
		t.Fatalf("restored archive index: %+v, %v", entries, err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(entry.Path)
		if err != nil || digestBytes(data) != entry.DocumentHash {
			t.Fatalf("restored document integrity: %s, %v", entry.SessionID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(restored, "export-state.json")); !os.IsNotExist(err) {
		t.Fatal("local checkpoint was shared in Git")
	}
}

func TestIncrementalSessionScopeKeepsNotFoundDistinctFromUnchanged(t *testing.T) {
	opts := checkpointOptions(t)
	writeSessionFixture(t, opts.CodexHome, "requested", "https://github.com/example/scope.git", "scope answer")
	ageSource(t, incrementalFixturePath(opts, "requested"), 3*time.Hour)
	opts.SessionID = "requested"
	mustExport(t, opts)
	unchanged := mustExport(t, opts)
	if unchanged.IgnoredUnchanged != 1 || unchanged.Created != 0 {
		t.Fatalf("requested unchanged session: %+v", unchanged)
	}
	opts.SessionID = "does-not-exist"
	for attempt := 0; attempt < 2; attempt++ {
		result, err := Export(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "was not found") || result.LastExportAt != "" {
			t.Fatalf("missing requested session: %+v, %v", result, err)
		}
	}
}
