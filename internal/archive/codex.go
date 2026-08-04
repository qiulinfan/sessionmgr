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
	var responseMessages, eventMessages []Message
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		result.RecordCount++
		var record map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
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
				if text := contentText(payload["content"]); strings.TrimSpace(text) != "" {
					responseMessages = append(responseMessages, Message{Role: role, Text: text, Timestamp: timestamp})
				}
			}
		}
		if recordType == "event_msg" && (payloadType == "user_message" || payloadType == "agent_message" || payloadType == "assistant_message") {
			role := "assistant"
			if payloadType == "user_message" {
				role = "user"
			}
			if text := firstString(payload["message"], payload["text"]); strings.TrimSpace(text) != "" {
				eventMessages = append(eventMessages, Message{Role: role, Text: text, Timestamp: timestamp})
			}
		}
		if recordType == "response_item" && (payloadType == "function_call" || payloadType == "custom_tool_call" || payloadType == "tool_call") {
			result.ToolCallCount++
		}
	}
	if err := scanner.Err(); err != nil {
		return Session{}, err
	}
	if result.ID == "" {
		result.ID = fallbackID
	}
	if result.ID == "" {
		return Session{}, fmt.Errorf("session has no ID")
	}
	if len(responseMessages) > 0 {
		result.Messages = responseMessages
	} else {
		result.Messages = eventMessages
	}
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
