package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func searchFixture(t *testing.T) (string, []Change) {
	t.Helper()
	root := t.TempDir()
	home, output := filepath.Join(root, "codex"), filepath.Join(root, "archive with spaces")
	writeSessionFixture(t, home, "first", "https://github.com/example/alpha.git", "hidden-topic answer")
	writeSessionFixture(t, home, "second", "https://github.com/example/beta.git", "other answer")
	writeTitles(t, home, titleLine("first", "Transformer discussion", "2026-08-05T03:00:00Z"), titleLine("second", "Unrelated task", "2026-08-05T04:00:00Z"))
	result, err := Export(context.Background(), testExportOptions(home, output))
	if err != nil {
		t.Fatal(err)
	}
	return output, result.Changes
}

func TestSearchFiltersMetadataAndOverlappingActivity(t *testing.T) {
	root, _ := searchFixture(t)
	ctx := context.Background()
	result, err := Search(ctx, SearchOptions{Output: root, Query: "TRANSFORMER", Repository: "https://github.com/example/alpha.git", Harness: "codex", Device: "test-device"})
	if err != nil || result.Total != 1 || result.Matches[0].SessionID != "first" {
		t.Fatalf("metadata search: %+v, %v", result, err)
	}
	match := result.Matches[0]
	if match.IntegrityVerified || match.CanonicalRemote != "github.com/example/alpha" || !match.TimeKnown {
		t.Fatalf("metadata provenance: %+v", match)
	}
	since, _ := time.Parse(time.RFC3339, "2026-08-05T02:30:00Z")
	until, _ := time.Parse(time.RFC3339, "2026-08-05T02:45:00Z")
	result, err = Search(ctx, SearchOptions{Output: root, Repository: "alpha", Since: since, Until: until})
	if err != nil || result.Total != 1 {
		t.Fatalf("session created earlier but continued into window was excluded: %+v, %v", result, err)
	}
	later, _ := time.Parse(time.RFC3339, "2026-08-06T00:00:00Z")
	result, err = Search(ctx, SearchOptions{Output: root, Since: later})
	if err != nil || result.Total != 0 {
		t.Fatalf("outside activity window: %+v, %v", result, err)
	}
	result, err = Search(ctx, SearchOptions{Output: root, Limit: 1})
	if err != nil || result.Total != 2 || !result.HasMore || len(result.Matches) != 1 {
		t.Fatalf("pagination: %+v, %v", result, err)
	}
	if result.Matches[0].SessionID != "second" {
		t.Fatal("results are not deterministically newest first")
	}
	last, err := Search(ctx, SearchOptions{Output: root, Limit: 1, Offset: 1})
	if err != nil || last.HasMore || last.Matches[0].SessionID != "first" {
		t.Fatalf("next page: %+v, %v", last, err)
	}
}

func TestContentSearchIsExplicitScopedAndVerified(t *testing.T) {
	root, changes := searchFixture(t)
	ctx := context.Background()
	metadata, err := Search(ctx, SearchOptions{Output: root, Query: "hidden-topic"})
	if err != nil || metadata.Total != 0 {
		t.Fatalf("metadata search read conversation body: %+v, %v", metadata, err)
	}
	if _, err := Search(ctx, SearchOptions{Output: root, Query: "hidden-topic", Content: true}); err == nil {
		t.Fatal("unbounded content search accepted")
	}
	content, err := Search(ctx, SearchOptions{Output: root, Query: "hidden-topic", Content: true, Repository: "alpha"})
	if err != nil || content.Total != 1 || !content.Matches[0].IntegrityVerified || len(content.Matches[0].MatchingLines) == 0 {
		t.Fatalf("scoped content search: %+v, %v", content, err)
	}
	var path string
	for _, change := range changes {
		if change.SessionID == "first" {
			path = change.Path
		}
	}
	if err := os.WriteFile(path, []byte("manual edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Metadata remains discoverable; body use requires the documented ownership
	// hash and does not quietly accept an edited archive.
	metadata, err = Search(ctx, SearchOptions{Output: root, SessionID: "first"})
	if err != nil || metadata.Total != 1 {
		t.Fatalf("metadata should remain inspectable: %+v, %v", metadata, err)
	}
	if _, err := Search(ctx, SearchOptions{Output: root, Query: "manual", Content: true, SessionID: "first"}); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("modified body accepted: %v", err)
	}
}

func TestShowVerifiesAndReturnsOnlySelectedLines(t *testing.T) {
	root, changes := searchFixture(t)
	ctx := context.Background()
	result, err := Show(ctx, ShowOptions{Output: root, SessionID: "first", FromLine: 1, ToLine: 3})
	if err != nil || !result.Verified || len(result.Lines) != 3 || result.SnapshotThrough == "" {
		t.Fatalf("verified excerpt: %+v, %v", result, err)
	}
	if result.Lines[0].Number != 1 || !result.Truncated {
		t.Fatal("excerpt lost numbering or continuation")
	}
	relative, _ := filepath.Rel(root, result.Session.Path)
	byPath, err := Show(ctx, ShowOptions{Output: root, Path: relative, FromLine: 4, ToLine: 8})
	if err != nil || byPath.Session.SessionID != "first" || byPath.FromLine != 4 {
		t.Fatalf("archive-relative path: %+v, %v", byPath, err)
	}
	if _, err := Show(ctx, ShowOptions{Output: root, SessionID: "first", ToLine: 501}); err == nil {
		t.Fatal("oversized excerpt accepted")
	}
	if _, err := Show(ctx, ShowOptions{Output: root, Path: "../outside/conversation.md"}); err == nil {
		t.Fatal("outside path accepted")
	}
	for _, change := range changes {
		if change.SessionID == "first" {
			if err := os.WriteFile(change.Path, []byte("user edit"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := Show(ctx, ShowOptions{Output: root, SessionID: "first"}); err == nil {
		t.Fatal("show returned modified content")
	}
}

func TestSearchMissingArchiveAndCancellationAreNotEmptySuccess(t *testing.T) {
	if _, err := Search(context.Background(), SearchOptions{Output: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing archive reported as no matches")
	}
	root, _ := searchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Search(ctx, SearchOptions{Output: root}); err == nil {
		t.Fatal("cancelled search returned success")
	}
}

func TestShowRefusesAmbiguousIdentityAndSearchReportsUnknownDates(t *testing.T) {
	root, changes := searchFixture(t)
	var first Entry
	entries, err := List(ListOptions{Output: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.SessionID == "first" {
			first = entry
		}
	}
	// A second device can archive the same native ID. No arbitrary selection.
	document, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	otherDir := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(first.Path))), "other-device", filepath.Base(filepath.Dir(first.Path)))
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var record sessionMetadata
	if err := readMetadata(filepath.Join(filepath.Dir(first.Path), sessionMetadataName), &record); err != nil {
		t.Fatal(err)
	}
	record.DeviceID = "device:other"
	record.DeviceName = "other-device"
	record.SessionKey = sessionKey(record.DeviceID, record.Harness, record.SessionID)
	meta, err := marshalMetadata(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, sessionMetadataName), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, conversationName), document, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Show(context.Background(), ShowOptions{Output: root, SessionID: "first"}); err == nil {
		t.Fatal("ambiguous native ID silently selected")
	}
	if _, err := Show(context.Background(), ShowOptions{Output: root, SessionKey: first.SessionKey}); err != nil {
		t.Fatalf("exact key did not resolve ambiguity: %v", err)
	}
	for _, change := range changes {
		if change.SessionID == "second" {
			var current sessionMetadata
			path := filepath.Join(filepath.Dir(change.Path), sessionMetadataName)
			if err := readMetadata(path, &current); err != nil {
				t.Fatal(err)
			}
			current.CreatedAt = ""
			data, err := marshalMetadata(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	since, _ := time.Parse(time.RFC3339, "2026-08-05T00:00:00Z")
	result, err := Search(context.Background(), SearchOptions{Output: root, Since: since})
	if err != nil || result.UnknownTimeSkipped != 1 || result.Total != 2 {
		t.Fatalf("unknown timing not explicit: %+v, %v", result, err)
	}
	result, err = Search(context.Background(), SearchOptions{Output: root, Since: since, IncludeUnknownTimes: true})
	if err != nil || result.Total != 3 {
		t.Fatalf("explicit unknown inclusion: %+v, %v", result, err)
	}
}
