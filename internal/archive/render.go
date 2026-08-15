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

func sessionKey(deviceID, harness, sessionID string) string {
	if harness == "" || harness == harnessCodex {
		return digest("device-session-v1\x00" + deviceID + "\x00" + sessionID)
	}
	return digest("device-harness-session-v1\x00" + deviceID + "\x00" + harness + "\x00" + sessionID)
}

func makeSnapshot(repo Repository, session Session, deviceID, deviceName string) Snapshot {
	if session.Harness == "" {
		session.Harness = harnessCodex
	}
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
		for attachmentIndex := range session.Messages[index].Attachments {
			attachment := &session.Messages[index].Attachments[attachmentIndex]
			attachment.Name, count = redact(attachment.Name)
			redactions += count
			attachment.RepositoryPath, count = redact(attachment.RepositoryPath)
			redactions += count
		}
	}
	updated := session.LastEventAt
	if session.TitleUpdatedAt.After(updated) {
		updated = session.TitleUpdatedAt
	}
	return Snapshot{
		Repository:   repo,
		Session:      session,
		DeviceID:     deviceID,
		DeviceName:   deviceName,
		SessionKey:   sessionKey(deviceID, session.Harness, session.ID),
		Redactions:   redactions,
		SourceUpdate: updated,
	}
}

func renderSnapshot(snapshot Snapshot) []byte {
	session := snapshot.Session
	var output strings.Builder
	fmt.Fprintln(&output, "---")
	fmt.Fprintf(&output, "schema_version: %d\n", SchemaVersion)
	fmt.Fprintf(&output, "renderer_version: %d\n", RendererVersion)
	fmt.Fprintf(&output, "repository_name: %s\n", quote(snapshot.Repository.Name))
	fmt.Fprintf(&output, "device_name: %s\n", quote(snapshot.DeviceName))
	fmt.Fprintf(&output, "harness: %s\n", quote(session.Harness))
	fmt.Fprintf(&output, "session_title: %s\n", quote(session.Title))
	writeTime(&output, "created_at", session.CreatedAt)
	writeTime(&output, "first_message_at", session.FirstMessageAt)
	writeTime(&output, "last_message_at", session.LastMessageAt)
	writeTime(&output, "last_event_at", session.LastEventAt)
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
	fmt.Fprintf(&output, "messages: %d\n", len(session.Messages))
	fmt.Fprintf(&output, "user_messages: %d\n", session.UserMessages)
	fmt.Fprintf(&output, "assistant_messages: %d\n", session.AssistantMessages)
	attachments, archivedAttachments := attachmentCounts(session)
	fmt.Fprintf(&output, "attachments: %d\n", attachments)
	fmt.Fprintf(&output, "archived_attachments: %d\n", archivedAttachments)
	fmt.Fprintf(&output, "redactions: %d\n", snapshot.Redactions)
	fmt.Fprintln(&output, "---")
	fmt.Fprintf(&output, "\n# %s\n\n", session.Title)
	harnessName := "Codex"
	if session.Harness == harnessDeepSeek {
		harnessName = "DeepSeek Harness"
	}
	fmt.Fprintf(&output, "> Exported from %s on %s for `%s`.\n\n", harnessName, snapshot.DeviceName, snapshot.Repository.Name)
	fmt.Fprintln(&output, "## Conversation")
	if len(session.Messages) == 0 {
		fmt.Fprintln(&output, "\n_No user or assistant messages could be rendered from this session._")
		return []byte(output.String())
	}
	for index, message := range session.Messages {
		role := "Assistant"
		if message.Role == "user" {
			role = "User"
		}
		fmt.Fprintf(&output, "\n### %d · %s", index+1, role)
		if !message.Timestamp.IsZero() {
			fmt.Fprintf(&output, " · %s", formatTime(message.Timestamp))
		}
		fmt.Fprintln(&output)
		if text := strings.TrimSpace(message.Text); text != "" {
			fmt.Fprintf(&output, "\n%s\n", text)
		}
		if len(message.Attachments) > 0 {
			fmt.Fprintln(&output, "\n**Attachments**")
			for _, attachment := range message.Attachments {
				fmt.Fprintf(&output, "\n- %s\n", renderAttachment(attachment))
			}
		}
	}
	return []byte(output.String())
}

func renderAttachment(attachment Attachment) string {
	name := markdownEscape(attachment.Name)
	switch attachment.Status {
	case attachmentStatusArchived:
		return fmt.Sprintf("[%s](%s) (%s, %s)", name, attachment.ArchivePath, attachment.MIMEType, humanBytes(attachment.Size))
	case attachmentStatusGitTracked:
		return fmt.Sprintf("%s — covered by Git as `%s`", name, strings.ReplaceAll(attachment.RepositoryPath, "`", "\\`"))
	case attachmentStatusTooLarge:
		return fmt.Sprintf("%s — not archived: exceeds the 50 MiB limit", name)
	case attachmentStatusBusy:
		return fmt.Sprintf("%s — not archived yet: source was busy", name)
	case attachmentStatusRemoteReference:
		return fmt.Sprintf("%s — not archived: remote reference", name)
	case attachmentStatusSensitive:
		return fmt.Sprintf("%s — not archived: sensitive credential-like content", name)
	default:
		return fmt.Sprintf("%s — not archived: source unavailable", name)
	}
}

func markdownEscape(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`")
	return replacer.Replace(value)
}

func humanBytes(size int64) string {
	const (
		kiB = int64(1024)
		miB = 1024 * kiB
	)
	if size >= miB {
		return fmt.Sprintf("%.1f MiB", float64(size)/float64(miB))
	}
	if size >= kiB {
		return fmt.Sprintf("%.1f KiB", float64(size)/float64(kiB))
	}
	return fmt.Sprintf("%d B", size)
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
