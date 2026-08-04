package archive

import (
	"strings"
	"testing"
	"time"
)

func TestSnapshotHashTracksContentAndRename(t *testing.T) {
	repo := repositoryFromRemote("github.com/example/project")
	when := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	base := Session{
		ID: "session-1", Title: "Useful name", TitleUpdatedAt: when,
		RawHash: digest("raw-v1"), UpdatedAt: when,
		Messages: []Message{{Role: "user", Text: "hello", Timestamp: when}},
	}
	first := makeSnapshot(repo, base)
	second := makeSnapshot(repo, base)
	if first.Hash != second.Hash {
		t.Fatalf("same snapshot produced different hashes: %s != %s", first.Hash, second.Hash)
	}
	updated := base
	updated.RawHash = digest("raw-v2")
	if makeSnapshot(repo, updated).Hash == first.Hash {
		t.Fatal("source update did not change snapshot hash")
	}
	renamed := base
	renamed.Title = "Renamed session"
	renamed.TitleUpdatedAt = when.Add(time.Minute)
	if makeSnapshot(repo, renamed).Hash == first.Hash {
		t.Fatal("rename did not change snapshot hash")
	}
}

func TestRenderSnapshotRedactsSecretsAndOmitsToolPayloads(t *testing.T) {
	repo := repositoryFromRemote("github.com/example/project")
	secret := "sk-abcdefghijklmnopqrstuvwxyz123456"
	snapshot := makeSnapshot(repo, Session{
		ID: "session-1", Title: "Token " + secret, RawHash: digest("raw"),
		Messages:      []Message{{Role: "user", Text: "OPENAI_API_KEY=" + secret + "\nDATABASE_PASSWORD=plain-secret-value"}},
		ToolCallCount: 2,
	})
	markdown := string(renderSnapshot(snapshot))
	if strings.Contains(markdown, secret) {
		t.Fatal("rendered Markdown contains a complete secret")
	}
	if strings.Contains(markdown, "plain-secret-value") {
		t.Fatal("rendered Markdown contains a secret environment value")
	}
	if !strings.Contains(markdown, "[REDACTED") {
		t.Fatal("rendered Markdown does not explain redaction")
	}
	if !strings.Contains(markdown, "tool_calls: 2") {
		t.Fatal("tool activity count is missing")
	}
	if !strings.Contains(markdown, "## Conversation") {
		t.Fatal("conversation section is missing")
	}
}
