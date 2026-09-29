package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const harnessOMP = "omp"

type ompHeader struct {
	Type                string   `json:"type"`
	Version             int      `json:"version"`
	ID                  string   `json:"id"`
	Timestamp           string   `json:"timestamp"`
	CWD                 string   `json:"cwd"`
	Title               string   `json:"title"`
	AdditionalDirectory []string `json:"additionalDirectories"`
}

type ompEntry struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	ParentID    *string         `json:"parentId"`
	Timestamp   string          `json:"timestamp"`
	Message     json.RawMessage `json:"message"`
	Title       string          `json:"title"`
	CustomType  string          `json:"customType"`
	startOffset int
	endOffset   int
}

type ompMessage struct {
	Role       string          `json:"role"`
	StopReason string          `json:"stopReason"`
	Content    json.RawMessage `json:"content"`
}

type ompContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
	URL      string `json:"url"`
	Name     string `json:"name"`
}

func DefaultOMPHome() (string, error) { return resolveOMPHome("") }

func DefaultOMPSessionDir(home string) (string, error) { return resolveOMPSessionDir("", home) }

func ResolveOMPSessionDir(configured, home string) (string, error) {
	return resolveOMPSessionDir(configured, home)
}

func resolveOMPHome(configured string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(configured)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR"))
	}
	if value == "" {
		value = filepath.Join(home, ".omp", "agent")
	} else if value == "~" {
		value = home
	} else if strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		value = filepath.Join(home, value[2:])
	}
	return filepath.Abs(value)
}

func resolveOMPSessionDir(configured, home string) (string, error) {
	value := strings.TrimSpace(configured)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
	}
	if value == "" {
		value = filepath.Join(home, "sessions")
	}
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = userHome
		} else {
			value = filepath.Join(userHome, value[2:])
		}
	}
	return filepath.Abs(value)
}

func discoverOMPSessionFiles(root string) ([]string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() ||
			!strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func parseOMPSession(raw []byte, home string) (Session, error) {
	var partial bool
	var prefixErr error
	raw, partial, prefixErr = completeRecordPrefix(raw)
	if prefixErr != nil {
		return Session{}, prefixErr
	}
	if len(raw) == 0 {
		return Session{}, fmt.Errorf("%w: OMP source has no complete record", errSourceBusy)
	}
	lines := bytes.Split(raw, []byte{'\n'})
	lineStarts := make([]int, len(lines))
	lineEnds := make([]int, len(lines))
	for number, line := range lines {
		if number > 0 {
			lineStarts[number] = lineEnds[number-1]
		}
		lineEnds[number] = len(line)
		if number > 0 {
			lineEnds[number] += lineEnds[number-1]
		}
		if number < len(lines)-1 {
			lineEnds[number]++
		}
	}
	index := 0
	var titleSlot struct {
		Type      string `json:"type"`
		Title     string `json:"title"`
		UpdatedAt string `json:"updatedAt"`
	}
	if len(lines) > 0 {
		var first struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(lines[0]), &first); err != nil {
			return Session{}, fmt.Errorf("parse OMP first record: %w", err)
		}
		if first.Type == "title" {
			if err := json.Unmarshal(bytes.TrimSpace(lines[0]), &titleSlot); err != nil {
				return Session{}, fmt.Errorf("parse OMP title slot: %w", err)
			}
			index++
		}
	}
	if index >= len(lines) || len(bytes.TrimSpace(lines[index])) == 0 {
		return Session{}, fmt.Errorf("OMP session has no header")
	}
	var header ompHeader
	if err := json.Unmarshal(bytes.TrimSpace(lines[index]), &header); err != nil {
		return Session{}, fmt.Errorf("parse OMP session header: %w", err)
	}
	if header.Type != "session" || header.Version != 3 || strings.TrimSpace(header.ID) == "" {
		return Session{}, fmt.Errorf("unsupported OMP session header type/version/id %q/%d/%q", header.Type, header.Version, header.ID)
	}
	created := parseTimestamp(header.Timestamp)
	if created.IsZero() || !filepath.IsAbs(header.CWD) {
		return Session{}, fmt.Errorf("OMP session %q has an invalid timestamp or cwd", header.ID)
	}
	result := Session{
		ID: header.ID, Harness: harnessOMP, Originator: "Oh My Pi", SourceKind: harnessOMP,
		CWD: header.CWD, CreatedAt: created, RawHash: digestBytes(raw), RecordCount: index + 1,
		Title: cleanTitle(titleSlot.Title), TitleUpdatedAt: parseTimestamp(titleSlot.UpdatedAt),
	}
	if result.Title == "" {
		result.Title = cleanTitle(header.Title)
	}
	// Additional roots are possible repository matches if the launch cwd moved.
	result.WorkspaceCandidates = append(result.WorkspaceCandidates, header.AdditionalDirectory...)

	entries := make([]ompEntry, 0, len(lines)-index-1)
	byID := make(map[string]int)
	for number, line := range lines[index+1:] {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			return Session{}, fmt.Errorf("parse OMP session %q record %d: %w", header.ID, number+index+2, err)
		}
		var entry ompEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return Session{}, fmt.Errorf("parse OMP session %q record %d: %w", header.ID, number+index+2, err)
		}
		if entry.ID == "" || entry.Type == "" {
			return Session{}, fmt.Errorf("OMP session %q has an incomplete record %d", header.ID, number+index+2)
		}
		entry.endOffset = lineEnds[number+index+1]
		entry.startOffset = lineStarts[number+index+1]
		if _, ok := fields["parentId"]; !ok {
			return Session{}, fmt.Errorf("OMP session %q record %d has no parentId", header.ID, number+index+2)
		}
		if _, duplicate := byID[entry.ID]; duplicate {
			return Session{}, fmt.Errorf("OMP session %q repeats entry ID %q", header.ID, entry.ID)
		}
		at := parseTimestamp(entry.Timestamp)
		if at.IsZero() {
			return Session{}, fmt.Errorf("OMP session %q record %d has an invalid timestamp", header.ID, number+index+2)
		}
		result.LastEventAt = laterTime(result.LastEventAt, at)
		if entry.Type == "title_change" && result.TitleUpdatedAt.Before(at) && strings.TrimSpace(entry.Title) != "" {
			result.Title = cleanTitle(entry.Title)
			result.TitleUpdatedAt = at
		}
		byID[entry.ID] = len(entries)
		entries = append(entries, entry)
		result.RecordCount++
	}
	if len(entries) == 0 {
		if partial {
			return Session{}, fmt.Errorf("%w: OMP source ends with an incomplete JSONL record", errSourceBusy)
		}
		if result.Title == "" {
			result.Title = "OMP session " + shortSessionID(result.ID)
		}
		return result, nil
	}
	selected := make(map[int]bool, len(entries))
	anyStopReason := false
	for _, entry := range entries {
		if entry.Type != "message" {
			continue
		}
		var message ompMessage
		if json.Unmarshal(entry.Message, &message) == nil && message.Role == "assistant" && message.StopReason != "" {
			anyStopReason = true
		}
	}
	chain := make([]int, 0, len(entries))
	for current := len(entries) - 1; ; {
		if selected[current] {
			return Session{}, fmt.Errorf("OMP session %q has a parent cycle", header.ID)
		}
		selected[current] = true
		chain = append(chain, current)
		parent := entries[current].ParentID
		if parent == nil {
			break
		}
		previous, found := byID[*parent]
		if !found || previous >= current {
			return Session{}, fmt.Errorf("OMP session %q has an unresolved parent %q", header.ID, *parent)
		}
		current = previous
	}
	for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
		chain[left], chain[right] = chain[right], chain[left]
	}
	lastTerminal := -1
	markerSeen := anyStopReason
	for index, number := range chain {
		entry := entries[number]
		if entry.Type != "message" {
			continue
		}
		var message ompMessage
		if err := json.Unmarshal(entry.Message, &message); err != nil {
			return Session{}, fmt.Errorf("parse OMP session %q message %q: %w", header.ID, entry.ID, err)
		}
		if message.Role != "assistant" || message.StopReason == "" {
			continue
		}
		markerSeen = true
		if message.StopReason == "stop" {
			lastTerminal = index
		}
	}
	activeStart := -1
	for index := lastTerminal + 1; index < len(chain); index++ {
		entry := entries[chain[index]]
		if entry.Type != "message" {
			continue
		}
		var message ompMessage
		if json.Unmarshal(entry.Message, &message) == nil && (message.Role == "user" || message.Role == "assistant") {
			activeStart = entry.startOffset
			break
		}
	}
	if partial || (markerSeen && activeStart >= 0) {
		if lastTerminal < 0 {
			return Session{}, fmt.Errorf("%w: OMP session has no completed turn", errSourceBusy)
		}
		cutoff := len(raw)
		if activeStart >= 0 {
			cutoff = activeStart
			if cutoff < entries[chain[lastTerminal]].endOffset {
				return Session{}, fmt.Errorf("%w: OMP completion boundary is out of order", errSourceBusy)
			}
		}
		return parseOMPSession(raw[:cutoff], home)
	}
	for number, entry := range entries {
		if entry.Type == "message" && !selected[number] {
			result.AlternateBranches++
		}
	}
	for _, number := range chain {
		entry := entries[number]
		if entry.Type != "message" {
			continue
		}
		var message ompMessage
		if err := json.Unmarshal(entry.Message, &message); err != nil {
			return Session{}, fmt.Errorf("parse OMP session %q message %q: %w", header.ID, entry.ID, err)
		}
		if message.Role != "user" && message.Role != "assistant" {
			result.OmittedCount++
			continue
		}
		text, attachments, tools, omitted, err := ompMessageContent(message.Content, home)
		if err != nil {
			return Session{}, fmt.Errorf("parse OMP session %q message %q: %w", header.ID, entry.ID, err)
		}
		result.ToolCallCount += tools
		result.OmittedCount += omitted
		if strings.TrimSpace(text) == "" && len(attachments) == 0 {
			continue
		}
		at := parseTimestamp(entry.Timestamp)
		result.Messages = append(result.Messages, Message{Role: message.Role, Text: text, Timestamp: at, Attachments: attachments})
		result.FirstMessageAt = earlierTime(result.FirstMessageAt, at)
		result.LastMessageAt = laterTime(result.LastMessageAt, at)
		if message.Role == "user" {
			result.UserMessages++
		} else {
			result.AssistantMessages++
		}
	}
	if result.Title == "" {
		for _, message := range result.Messages {
			if message.Role == "user" && strings.TrimSpace(message.Text) != "" {
				result.Title = cleanTitle(message.Text)
				break
			}
		}
	}
	if result.Title == "" {
		result.Title = "OMP session " + shortSessionID(result.ID)
	}
	return result, nil
}

func ompMessageContent(raw json.RawMessage, home string) (string, []Attachment, int, int, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil, 0, 0, nil
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain, nil, 0, 0, nil
	}
	var blocks []ompContent
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil, 0, 0, err
	}
	texts := make([]string, 0)
	attachments := make([]Attachment, 0)
	toolCalls, omitted := 0, 0
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				texts = append(texts, block.Text)
			}
		case "toolCall":
			toolCalls++
		case "thinking":
			// Reasoning is not a user-facing conversation message.
		case "image":
			value := block.Data
			if value == "" {
				value = block.URL
			}
			name := block.Name
			if name == "" {
				name = "image"
			}
			attachment := Attachment{Name: name, MIMEType: block.MIMEType}
			switch {
			case strings.HasPrefix(value, "blob:sha256:"):
				hash := strings.TrimPrefix(value, "blob:sha256:")
				if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
					omitted++
					continue
				}
				path := filepath.Join(home, "blobs", hash)
				attachment.SourceKind, attachment.SourceValue, attachment.LocalPath = "local_path", path, path
				attachment.ExpectedHash = "sha256:" + hash
			case strings.HasPrefix(value, "data:"):
				attachment.SourceKind, attachment.SourceValue = "embedded_data", value
			default:
				attachment.SourceKind = "remote_reference"
			}
			attachments = append(attachments, attachment)
		default:
			omitted++
		}
	}
	return strings.Join(texts, "\n\n"), attachments, toolCalls, omitted, nil
}

func shortSessionID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
