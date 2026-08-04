package archive

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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

func readStable(ctx context.Context, path string) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		after, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if before.Size() == after.Size() && before.ModTime() == after.ModTime() {
			return data, nil
		}
	}
	return nil, fmt.Errorf("source changed while being read")
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
		if timestamp.After(result.UpdatedAt) {
			result.UpdatedAt = timestamp
		}
		recordType := stringValue(record["type"])
		payload := mapValue(record["payload"])
		payloadType := stringValue(payload["type"])
		if recordType == "session_meta" && result.ID == "" {
			result.ID = firstString(payload["id"], payload["session_id"])
			result.CWD = stringValue(payload["cwd"])
			result.CodexVersion = firstString(payload["cli_version"], payload["version"])
			result.StartedAt = firstTime(payload["timestamp"], record["timestamp"])
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
