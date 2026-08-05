package archive

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultStabilityWindow = 350 * time.Millisecond

var errSourceBusy = errors.New("session source is busy")

type sourceFingerprint struct {
	size    int64
	modTime time.Time
	info    os.FileInfo
}

type sourceIssue struct {
	path string
	err  error
}

type titleRecord struct {
	Title     string
	UpdatedAt time.Time
}

type orderedMessage struct {
	Message
	order int
}

func DefaultCodexHome() (string, error) {
	if value := os.Getenv("CODEX_HOME"); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func discoverSessionFiles(home string) ([]string, error) {
	var paths []string
	for _, directory := range []string{"sessions", "archived_sessions"} {
		base := filepath.Join(home, directory)
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func loadTitles(home string) (map[string]titleRecord, error) {
	result := make(map[string]titleRecord)
	file, err := os.Open(filepath.Join(home, "session_index.jsonl"))
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var record struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
			UpdatedAt  string `json:"updated_at"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.ID == "" || strings.TrimSpace(record.ThreadName) == "" {
			continue
		}
		updated := parseTimestamp(record.UpdatedAt)
		previous, exists := result[record.ID]
		if !exists || previous.UpdatedAt.IsZero() || !updated.Before(previous.UpdatedAt) {
			result[record.ID] = titleRecord{Title: cleanTitle(record.ThreadName), UpdatedAt: updated}
		}
	}
	return result, scanner.Err()
}

func observeStableSources(ctx context.Context, paths []string, window time.Duration) (map[string]sourceFingerprint, int, []sourceIssue, error) {
	first := make(map[string]sourceFingerprint, len(paths))
	issues := make([]sourceIssue, 0)
	busy := 0
	for _, path := range paths {
		fingerprint, err := fingerprintSource(path)
		if err != nil {
			if sourceErrorIsBusy(err) {
				busy++
			} else {
				issues = append(issues, sourceIssue{path: path, err: err})
			}
			continue
		}
		first[path] = fingerprint
	}
	if len(first) == 0 {
		return map[string]sourceFingerprint{}, busy, issues, nil
	}
	if window > 0 {
		timer := time.NewTimer(window)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, busy, issues, ctx.Err()
		case <-timer.C:
		}
	}
	stable := make(map[string]sourceFingerprint, len(first))
	for _, path := range paths {
		before, ok := first[path]
		if !ok {
			continue
		}
		after, err := fingerprintSource(path)
		if err != nil {
			if sourceErrorIsBusy(err) {
				busy++
			} else {
				issues = append(issues, sourceIssue{path: path, err: err})
			}
			continue
		}
		if !sameFingerprint(before, after) {
			busy++
			continue
		}
		stable[path] = after
	}
	return stable, busy, issues, nil
}

func fingerprintSource(path string) (sourceFingerprint, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return sourceFingerprint{}, err
	}
	if !info.Mode().IsRegular() {
		return sourceFingerprint{}, fmt.Errorf("session source is not a regular file")
	}
	return sourceFingerprint{size: info.Size(), modTime: info.ModTime(), info: info}, nil
}

func readObservedSource(ctx context.Context, path string, expected sourceFingerprint) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := fingerprintSource(path)
	if err != nil {
		if sourceErrorIsBusy(err) {
			return nil, fmt.Errorf("%w: %v", errSourceBusy, err)
		}
		return nil, err
	}
	if !sameFingerprint(expected, before) {
		return nil, fmt.Errorf("%w: source changed before it was opened", errSourceBusy)
	}
	file, err := os.Open(path)
	if err != nil {
		if sourceErrorIsBusy(err) {
			return nil, fmt.Errorf("%w: %v", errSourceBusy, err)
		}
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		file.Close()
		if sourceErrorIsBusy(err) {
			return nil, fmt.Errorf("%w: %v", errSourceBusy, err)
		}
		return nil, err
	}
	opened := sourceFingerprint{size: openedInfo.Size(), modTime: openedInfo.ModTime(), info: openedInfo}
	if !sameFingerprint(expected, opened) {
		file.Close()
		return nil, fmt.Errorf("%w: source was replaced while being opened", errSourceBusy)
	}
	data, readErr := io.ReadAll(file)
	handleInfo, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		if sourceErrorIsBusy(readErr) {
			return nil, fmt.Errorf("%w: %v", errSourceBusy, readErr)
		}
		return nil, readErr
	}
	if statErr != nil {
		if sourceErrorIsBusy(statErr) {
			return nil, fmt.Errorf("%w: %v", errSourceBusy, statErr)
		}
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	handleAfter := sourceFingerprint{size: handleInfo.Size(), modTime: handleInfo.ModTime(), info: handleInfo}
	pathAfter, err := fingerprintSource(path)
	if err != nil {
		if sourceErrorIsBusy(err) {
			return nil, fmt.Errorf("%w: %v", errSourceBusy, err)
		}
		return nil, err
	}
	if !sameFingerprint(expected, handleAfter) || !sameFingerprint(expected, pathAfter) {
		return nil, fmt.Errorf("%w: source changed while being read", errSourceBusy)
	}
	if !completeJSONL(data) {
		return nil, fmt.Errorf("%w: source ends with an incomplete JSONL record", errSourceBusy)
	}
	return data, nil
}

func sameFingerprint(left, right sourceFingerprint) bool {
	return left.size == right.size && left.modTime.Equal(right.modTime) && os.SameFile(left.info, right.info)
}

func completeJSONL(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return false
	}
	lastBreak := bytes.LastIndexByte(trimmed, '\n')
	lastRecord := trimmed[lastBreak+1:]
	return json.Valid(lastRecord)
}

func sourceErrorIsBusy(err error) bool {
	return errors.Is(err, errSourceBusy) || errors.Is(err, os.ErrNotExist) || isPlatformBusyError(err)
}

func parseSession(raw []byte, fallbackID string, titles map[string]titleRecord) (Session, error) {
	result := Session{RawHash: digestBytes(raw)}
	var responseUsers, responseAssistants, eventUsers, eventAssistants []orderedMessage
	remaining := raw
	for len(remaining) > 0 {
		line, rest, found := bytes.Cut(remaining, []byte{'\n'})
		if found {
			remaining = rest
		} else {
			remaining = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		result.RecordCount++
		var record map[string]interface{}
		if err := json.Unmarshal(line, &record); err != nil {
			result.MalformedCount++
			continue
		}
		timestamp := parseTimestamp(stringValue(record["timestamp"]))
		if timestamp.After(result.LastEventAt) {
			result.LastEventAt = timestamp
		}
		recordType := stringValue(record["type"])
		payload := mapValue(record["payload"])
		payloadType := stringValue(payload["type"])
		if recordType == "session_meta" && result.ID == "" {
			result.ID = firstString(payload["id"], payload["session_id"])
			result.CWD = stringValue(payload["cwd"])
			result.CodexVersion = firstString(payload["cli_version"], payload["version"])
			result.CreatedAt = firstTime(payload["timestamp"], record["timestamp"])
			git := mapValue(payload["git"])
			result.Remote = stringValue(git["repository_url"])
			result.Commit = stringValue(git["commit_hash"])
			result.Branch = stringValue(git["branch"])
		}
		if recordType == "response_item" && payloadType == "message" {
			role := stringValue(payload["role"])
			if role == "user" || role == "assistant" {
				text, attachments := contentParts(payload["content"])
				if role != "user" {
					attachments = nil
				}
				if strings.TrimSpace(text) != "" || len(attachments) > 0 {
					message := orderedMessage{Message: Message{
						Role: role, Text: text, Timestamp: timestamp, Attachments: attachments,
					}, order: result.RecordCount}
					if role == "user" {
						if !injectedUserContextContent(payload["content"]) {
							responseUsers = append(responseUsers, message)
						}
					} else {
						responseAssistants = append(responseAssistants, message)
					}
				}
			}
		}
		if recordType == "event_msg" && (payloadType == "user_message" || payloadType == "agent_message" || payloadType == "assistant_message") {
			role := "assistant"
			if payloadType == "user_message" {
				role = "user"
			}
			text := firstString(payload["message"], payload["text"])
			attachments := []Attachment(nil)
			if role == "user" {
				attachments = eventAttachments(payload)
			}
			if strings.TrimSpace(text) != "" || len(attachments) > 0 {
				message := orderedMessage{Message: Message{
					Role: role, Text: text, Timestamp: timestamp, Attachments: attachments,
				}, order: result.RecordCount}
				if role == "user" {
					eventUsers = append(eventUsers, message)
				} else {
					eventAssistants = append(eventAssistants, message)
				}
			}
		}
		if recordType == "response_item" && (payloadType == "function_call" || payloadType == "custom_tool_call" || payloadType == "tool_call") {
			result.ToolCallCount++
		}
	}
	if result.ID == "" {
		result.ID = fallbackID
	}
	if result.ID == "" {
		return Session{}, fmt.Errorf("session has no ID")
	}
	result.Messages = selectConversationMessages(responseUsers, responseAssistants, eventUsers, eventAssistants)
	for _, message := range result.Messages {
		switch message.Role {
		case "user":
			result.UserMessages++
		case "assistant":
			result.AssistantMessages++
		}
		if message.Timestamp.IsZero() {
			continue
		}
		if result.FirstMessageAt.IsZero() || message.Timestamp.Before(result.FirstMessageAt) {
			result.FirstMessageAt = message.Timestamp
		}
		if message.Timestamp.After(result.LastMessageAt) {
			result.LastMessageAt = message.Timestamp
		}
	}
	result.OmittedCount = result.RecordCount - len(result.Messages)
	if result.OmittedCount < 0 {
		result.OmittedCount = 0
	}
	if title, ok := titles[result.ID]; ok {
		result.Title = title.Title
		result.TitleUpdatedAt = title.UpdatedAt
	}
	if result.Title == "" {
		for _, message := range result.Messages {
			if message.Role == "user" {
				result.Title = cleanTitle(message.Text)
				break
			}
		}
	}
	if result.Title == "" {
		result.Title = "Codex session " + result.ID
	}
	return result, nil
}

func selectConversationMessages(responseUsers, responseAssistants, eventUsers, eventAssistants []orderedMessage) []Message {
	users := responseUsers
	if len(eventUsers) > 0 {
		users = canonicalEventUsers(eventUsers, responseUsers)
	}
	assistants := responseAssistants
	if len(assistants) == 0 {
		assistants = eventAssistants
	}
	selected := make([]orderedMessage, 0, len(users)+len(assistants))
	selected = append(selected, users...)
	selected = append(selected, assistants...)
	sort.SliceStable(selected, func(i, j int) bool {
		return selected[i].order < selected[j].order
	})
	result := make([]Message, 0, len(selected))
	for _, message := range selected {
		result = append(result, message.Message)
	}
	return result
}

func canonicalEventUsers(events, responses []orderedMessage) []orderedMessage {
	result := make([]orderedMessage, 0, len(events))
	used := make([]bool, len(responses))
	for _, event := range events {
		for index, response := range responses {
			if used[index] || !sameMessageText(event.Text, response.Text) {
				continue
			}
			used[index] = true
			// Response items can retain embedded attachment bytes that the event
			// message represents only as a local path. Keep the event as the user-
			// visible source, but prefer its matching structured attachment data.
			if len(response.Attachments) > 0 {
				event.Attachments = response.Attachments
			}
			break
		}
		result = append(result, event)
	}
	return result
}

func sameMessageText(left, right string) bool {
	normalize := func(value string) string {
		return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	}
	return normalize(left) == normalize(right)
}

func injectedUserContextContent(value interface{}) bool {
	items, ok := value.([]interface{})
	if !ok {
		return injectedUserContextPart(contentText(value))
	}
	found := false
	for _, item := range items {
		text := strings.TrimSpace(contentText(item))
		if text == "" {
			continue
		}
		found = true
		if !injectedUserContextPart(text) {
			return false
		}
	}
	return found
}

func injectedUserContextPart(value string) bool {
	value = strings.TrimSpace(value)
	return completeContextEnvelope(value, "<recommended_plugins>", "</recommended_plugins>") ||
		completeContextEnvelope(value, "<environment_context>", "</environment_context>") ||
		(strings.HasPrefix(value, "# AGENTS.md instructions for ") &&
			strings.Contains(value, "<INSTRUCTIONS>") && strings.HasSuffix(value, "</INSTRUCTIONS>"))
}

func completeContextEnvelope(value, opening, closing string) bool {
	return strings.HasPrefix(value, opening) && strings.HasSuffix(value, closing)
}

func contentParts(value interface{}) (string, []Attachment) {
	items, ok := value.([]interface{})
	if !ok {
		return contentText(value), nil
	}
	texts := make([]string, 0, len(items))
	attachments := make([]Attachment, 0)
	pendingPath := ""
	for _, value := range items {
		item := mapValue(value)
		itemType := stringValue(item["type"])
		switch itemType {
		case "input_text", "output_text", "text":
			text := firstString(item["text"], item["message"])
			if path, marker := localMediaMarkerPath(text); marker {
				pendingPath = path
				continue
			}
			if text == "</image>" || text == "</audio>" {
				pendingPath = ""
				continue
			}
			pendingPath = ""
			if strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		case "input_image":
			if attachment, found := attachmentFromValue(
				firstString(item["image_url"], item["url"]),
				firstString(item["filename"], item["name"]), pendingPath, "image",
			); found {
				attachments = append(attachments, attachment)
			}
			pendingPath = ""
		case "input_audio":
			if attachment, found := attachmentFromValue(
				firstString(item["audio_url"], item["url"]),
				firstString(item["filename"], item["name"]), pendingPath, "audio",
			); found {
				attachments = append(attachments, attachment)
			}
			pendingPath = ""
		case "input_file", "file", "attachment":
			if attachment, found := attachmentFromStructuredMap(item, pendingPath); found {
				attachments = append(attachments, attachment)
			}
			pendingPath = ""
		default:
			if text := strings.TrimSpace(contentText(value)); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n\n"), attachments
}

func localMediaMarkerPath(text string) (string, bool) {
	if !(strings.HasPrefix(text, "<image name=") || strings.HasPrefix(text, "<audio name=")) || !strings.HasSuffix(text, ">") {
		return "", false
	}
	marker := ` path="`
	index := strings.LastIndex(text, marker)
	if index < 0 {
		return "", true
	}
	path := strings.TrimSuffix(text[index+len(marker):], `">`)
	return path, true
}

func eventAttachments(payload map[string]interface{}) []Attachment {
	attachments := make([]Attachment, 0)
	appendValues := func(value interface{}, kind string, local bool) {
		values, ok := value.([]interface{})
		if !ok {
			return
		}
		for _, value := range values {
			if item, ok := value.(map[string]interface{}); ok {
				if attachment, found := attachmentFromStructuredMap(item, ""); found {
					attachments = append(attachments, attachment)
				}
				continue
			}
			source := stringValue(value)
			if source == "" {
				continue
			}
			if local {
				attachments = append(attachments, Attachment{
					Name: attachmentBaseName(source), SourceKind: "local_path", SourceValue: source, LocalPath: source,
				})
			} else if attachment, found := attachmentFromValue(source, "", "", kind); found {
				attachments = append(attachments, attachment)
			}
		}
	}
	appendValues(payload["images"], "image", false)
	appendValues(payload["local_images"], "image", true)
	appendValues(payload["audio"], "audio", false)
	appendValues(payload["local_audio"], "audio", true)
	for _, key := range []string{"local_files", "files", "attachments"} {
		appendValues(payload[key], "file", key == "local_files")
	}
	return attachments
}

func attachmentFromStructuredMap(item map[string]interface{}, fallbackPath string) (Attachment, bool) {
	name := firstString(item["filename"], item["file_name"], item["name"])
	localPath := firstString(item["path"], item["local_path"], fallbackPath)
	mediaType := firstString(item["mime_type"], item["mimeType"])
	embedded := firstString(item["file_data"], item["data"], item["image_url"], item["audio_url"])
	if embedded != "" {
		if !strings.HasPrefix(strings.ToLower(embedded), "data:") && stringValue(item["file_data"]) == embedded {
			embedded = "data:" + mediaType + ";base64," + embedded
		}
		if attachment, found := attachmentFromValue(embedded, name, localPath, "file"); found {
			attachment.MIMEType = mediaType
			return attachment, true
		}
	}
	if localPath != "" {
		return Attachment{
			Name: attachmentDisplayName(name, localPath), MIMEType: mediaType,
			SourceKind: "local_path", SourceValue: localPath, LocalPath: localPath,
		}, true
	}
	source := firstString(item["file_url"], item["url"])
	attachment, found := attachmentFromValue(source, name, fallbackPath, "file")
	if found && attachment.MIMEType == "" {
		attachment.MIMEType = mediaType
	}
	return attachment, found
}

func attachmentFromValue(source, name, localPath, kind string) (Attachment, bool) {
	if source == "" {
		return Attachment{}, false
	}
	sourceKind := "remote_reference"
	if strings.HasPrefix(strings.ToLower(source), "data:") {
		sourceKind = "embedded_data"
	}
	return Attachment{
		Name: attachmentDisplayName(name, localPath), SourceKind: sourceKind,
		SourceValue: source, LocalPath: localPath, MIMEType: mediaKindMIME(kind),
	}, true
}

func mediaKindMIME(kind string) string {
	switch kind {
	case "image":
		return "image/*"
	case "audio":
		return "audio/*"
	default:
		return ""
	}
}

func attachmentDisplayName(name, path string) string {
	if strings.TrimSpace(name) != "" {
		return attachmentBaseName(name)
	}
	return attachmentBaseName(path)
}

func attachmentBaseName(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	value = strings.TrimRight(value, "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		value = value[index+1:]
	}
	return value
}

func contentText(value interface{}) string {
	switch item := value.(type) {
	case string:
		return item
	case []interface{}:
		parts := make([]string, 0, len(item))
		for _, child := range item {
			if text := strings.TrimSpace(contentText(child)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n\n")
	case map[string]interface{}:
		return firstString(item["text"], item["message"])
	default:
		return ""
	}
}

func cleanTitle(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 160 {
		return string(runes[:157]) + "..."
	}
	return value
}

func parseTimestamp(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed.UTC()
}

func firstTime(values ...interface{}) time.Time {
	for _, value := range values {
		if result := parseTimestamp(stringValue(value)); !result.IsZero() {
			return result
		}
	}
	return time.Time{}
}

func firstString(values ...interface{}) string {
	for _, value := range values {
		if result := stringValue(value); result != "" {
			return result
		}
	}
	return ""
}

func stringValue(value interface{}) string {
	result, _ := value.(string)
	return result
}

func mapValue(value interface{}) map[string]interface{} {
	result, _ := value.(map[string]interface{})
	if result == nil {
		return map[string]interface{}{}
	}
	return result
}
