package archive

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var redactionPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----.*?-----END (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{30,}\b`)},
	{"OpenAI token", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
	{"AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{20,}\b`)},
	{"URL credential", regexp.MustCompile(`(?i)\bhttps?://[^/\s:@]+:[^/\s@]+@`)},
}

var secretAssignment = regexp.MustCompile(`(?im)^(\s*(?:export\s+)?[A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|TOKEN|API_KEY|PRIVATE_KEY)[A-Z0-9_]*\s*=\s*)\S+`)

func makeSnapshot(repo Repository, session Session) Snapshot {
	redactions := 0
	session.Title, redactions = redact(session.Title)
	for _, value := range []*string{&session.CodexVersion, &session.Commit, &session.Branch} {
		var count int
		*value, count = redact(*value)
		redactions += count
	}
	for index := range session.Messages {
		var count int
		session.Messages[index].Text, count = redact(session.Messages[index].Text)
		redactions += count
	}
	updated := session.UpdatedAt
	if session.TitleUpdatedAt.After(updated) {
		updated = session.TitleUpdatedAt
	}
	identity := fmt.Sprintf("sessionmgr-markdown-v%d\x00%s\x00%s\x00%s\x00%s",
		RendererVersion, repo.Key, session.RawHash, session.Title, formatTime(session.TitleUpdatedAt))
	return Snapshot{
		Repository:   repo,
		Session:      session,
		Hash:         digest(identity),
		Redactions:   redactions,
		SourceUpdate: updated,
	}
}

func renderRepository(repo Repository) []byte {
	var output strings.Builder
	fmt.Fprintln(&output, "---")
	fmt.Fprintf(&output, "schema_version: %d\n", SchemaVersion)
	fmt.Fprintf(&output, "repository_key: %s\n", quote(repo.Key))
	fmt.Fprintf(&output, "repository_name: %s\n", quote(repo.Name))
	fmt.Fprintf(&output, "canonical_remote: %s\n", quote(repo.CanonicalRemote))
	fmt.Fprintln(&output, "---")
	fmt.Fprintf(&output, "\n# %s\n\n", repo.Name)
	fmt.Fprintln(&output, "Codex session snapshots for this Git repository. Snapshot files are immutable and keyed by content.")
	return []byte(output.String())
}

func renderSnapshot(snapshot Snapshot) []byte {
	session := snapshot.Session
	var output strings.Builder
	fmt.Fprintln(&output, "---")
	fmt.Fprintf(&output, "schema_version: %d\n", SchemaVersion)
	fmt.Fprintf(&output, "renderer_version: %d\n", RendererVersion)
	fmt.Fprintf(&output, "repository_key: %s\n", quote(snapshot.Repository.Key))
	fmt.Fprintf(&output, "repository_name: %s\n", quote(snapshot.Repository.Name))
	fmt.Fprintf(&output, "session_id: %s\n", quote(session.ID))
	fmt.Fprintf(&output, "session_title: %s\n", quote(session.Title))
	fmt.Fprintf(&output, "snapshot_hash: %s\n", quote(snapshot.Hash))
	fmt.Fprintf(&output, "source_hash: %s\n", quote(session.RawHash))
	writeTime(&output, "started_at", session.StartedAt)
	writeTime(&output, "updated_at", snapshot.SourceUpdate)
	writeTime(&output, "title_updated_at", session.TitleUpdatedAt)
	if session.CodexVersion != "" {
		fmt.Fprintf(&output, "codex_version: %s\n", quote(session.CodexVersion))
	}
	if session.Commit != "" {
		fmt.Fprintf(&output, "git_commit: %s\n", quote(session.Commit))
	}
	if session.Branch != "" {
		fmt.Fprintf(&output, "git_branch: %s\n", quote(session.Branch))
	}
	fmt.Fprintf(&output, "source_records: %d\n", session.RecordCount)
	fmt.Fprintf(&output, "malformed_records: %d\n", session.MalformedCount)
	fmt.Fprintf(&output, "omitted_records: %d\n", session.OmittedCount)
	fmt.Fprintf(&output, "tool_calls: %d\n", session.ToolCallCount)
	fmt.Fprintf(&output, "redactions: %d\n", snapshot.Redactions)
	fmt.Fprintln(&output, "---")
	fmt.Fprintf(&output, "\n# %s\n\n", session.Title)
	fmt.Fprintf(&output, "> Codex session `%s` · source `%s`\n\n", session.ID, session.RawHash)
	fmt.Fprintln(&output, "## Conversation")
	if len(session.Messages) == 0 {
		fmt.Fprintln(&output, "\n_No user or assistant messages could be rendered from this session._")
		return []byte(output.String())
	}
	for _, message := range session.Messages {
		role := "Assistant"
		if message.Role == "user" {
			role = "User"
		}
		fmt.Fprintf(&output, "\n### %s\n", role)
		if !message.Timestamp.IsZero() {
			fmt.Fprintf(&output, "\n_%s_\n", formatTime(message.Timestamp))
		}
		fmt.Fprintf(&output, "\n%s\n", strings.TrimSpace(message.Text))
	}
	return []byte(output.String())
}

func redact(value string) (string, int) {
	count := 0
	privateKey := redactionPatterns[0]
	matches := privateKey.pattern.FindAllStringIndex(value, -1)
	count += len(matches)
	value = privateKey.pattern.ReplaceAllString(value, "[REDACTED "+privateKey.name+"]")
	matches = secretAssignment.FindAllStringIndex(value, -1)
	count += len(matches)
	value = secretAssignment.ReplaceAllString(value, "${1}[REDACTED secret value]")
	for _, candidate := range redactionPatterns[1:] {
		matches := candidate.pattern.FindAllStringIndex(value, -1)
		count += len(matches)
		value = candidate.pattern.ReplaceAllString(value, "[REDACTED "+candidate.name+"]")
	}
	return value, count
}

func quote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func writeTime(output *strings.Builder, key string, value time.Time) {
	if !value.IsZero() {
		fmt.Fprintf(output, "%s: %s\n", key, quote(formatTime(value)))
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
