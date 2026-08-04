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
		RawHash: digest("raw-v1"), LastEventAt: when,
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

func TestRenderSnapshotIncludesConversationTimeline(t *testing.T) {
	repo := repositoryFromRemote("github.com/example/project")
	created := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	first := created.Add(time.Minute)
	last := created.Add(2 * time.Minute)
	event := created.Add(3 * time.Minute)
	snapshot := makeSnapshot(repo, Session{
		ID: "session-1", Title: "Timeline", RawHash: digest("raw"),
		CreatedAt: created, FirstMessageAt: first, LastMessageAt: last, LastEventAt: event,
		UserMessages: 1, AssistantMessages: 1,
		Messages: []Message{
			{Role: "user", Text: "question", Timestamp: first},
			{Role: "assistant", Text: "answer", Timestamp: last},
		},
	})
	markdown := string(renderSnapshot(snapshot))
	for _, expected := range []string{
		"renderer_version: 2",
		`created_at: "2026-08-05T01:00:00Z"`,
		`first_message_at: "2026-08-05T01:01:00Z"`,
		`last_message_at: "2026-08-05T01:02:00Z"`,
		`last_event_at: "2026-08-05T01:03:00Z"`,
		"messages: 2", "user_messages: 1", "assistant_messages: 1",
		"### 1 · User · 2026-08-05T01:01:00Z",
		"### 2 · Assistant · 2026-08-05T01:02:00Z",
	} {
		if !strings.Contains(markdown, expected) {
			t.Fatalf("timeline Markdown is missing %q:\n%s", expected, markdown)
		}
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
