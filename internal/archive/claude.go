package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const harnessClaudeCode = "claude-code"

type claudeRecord struct {
	Type                    string          `json:"type"`
	UUID                    string          `json:"uuid"`
	ParentUUID              string          `json:"parentUuid"`
	SessionID               string          `json:"sessionId"`
	Timestamp               string          `json:"timestamp"`
	CWD                     string          `json:"cwd"`
	RelocatedCWD            string          `json:"relocatedCwd"`
	GitBranch               string          `json:"gitBranch"`
	Version                 string          `json:"version"`
	UserType                string          `json:"userType"`
	PromptSource            string          `json:"promptSource"`
	IsSidechain             bool            `json:"isSidechain"`
	AgentID                 string          `json:"agentId"`
	IsMeta                  bool            `json:"isMeta"`
	IsAPIErrorMessage       bool            `json:"isApiErrorMessage"`
	Error                   json.RawMessage `json:"error"`
	SourceToolAssistantUUID string          `json:"sourceToolAssistantUUID"`
	ToolUseResult           json.RawMessage `json:"toolUseResult"`
	Message                 json.RawMessage `json:"message"`
	AITitle                 string          `json:"aiTitle"`
	AgentName               string          `json:"agentName"`
	Entrypoint              string          `json:"entrypoint"`
	Origin                  struct {
		Kind string `json:"kind"`
	} `json:"origin"`
}

type claudeMessage struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
}

type claudeContentBlock struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Title  string `json:"title"`
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

type claudeNode struct {
	record      claudeRecord
	message     claudeMessage
	timestamp   time.Time
	order       int
	startOffset int
	endOffset   int
}

type claudeAssistantGroup struct {
	id        string
	timestamp time.Time
	texts     []string
}

func DefaultClaudeHome() (string, error) {
	return resolveClaudeHome("")
}

func resolveClaudeHome(configured string) (string, error) {
	userRoot, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(configured)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	}
	if value == "" {
		value = filepath.Join(userRoot, ".claude")
	} else if value == "~" {
		value = userRoot
	} else if strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		value = filepath.Join(userRoot, value[2:])
	}
	return filepath.Abs(value)
}

func discoverClaudeSessionFiles(home string) ([]string, error) {
	root := filepath.Join(home, "projects")
	projects, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	for _, project := range projects {
		if project.Type()&os.ModeSymlink != 0 || !project.IsDir() {
			continue
		}
		projectRoot := filepath.Join(root, project.Name())
		entries, readErr := os.ReadDir(projectRoot)
		if readErr != nil {
			return nil, readErr
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
				continue
			}
			paths = append(paths, filepath.Join(projectRoot, entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func parseClaudeSession(raw []byte, fallbackID string) (Session, error) {
	var partial bool
	var prefixErr error
	raw, partial, prefixErr = completeRecordPrefix(raw)
	if prefixErr != nil {
		return Session{}, prefixErr
	}
	result := Session{
		ID:         strings.TrimSpace(fallbackID),
		Harness:    harnessClaudeCode,
		Originator: "Claude Code",
		SourceKind: harnessClaudeCode,
		RawHash:    digestBytes(raw),
	}
	if result.ID == "" {
		return Session{}, fmt.Errorf("Claude Code transcript filename has no session ID")
	}

	occurrences := make(map[string][]*claudeNode)
	var anchor *claudeNode
	explicitTitle := ""
	generatedTitle := ""
	anyStopReason := false
	remaining := raw
	order := 0
	for len(remaining) > 0 {
		startOffset := len(raw) - len(remaining)
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
		order++
		result.RecordCount++
		var record claudeRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return Session{}, fmt.Errorf("parse Claude Code session %q record %d: %w", result.ID, order, err)
		}
		if record.Type == "teleported-from" ||
			record.Entrypoint == "claude-code-web" || record.Entrypoint == "web" ||
			record.Entrypoint == "cloud" || record.Entrypoint == "mobile" {
			result.ExcludeReason = "cloud"
			return result, nil
		}
		// Claude Code forks copy their ancestor records into the new transcript.
		// The filename identifies the current physical session; a different record
		// sessionId is permitted only as ancestry, never as a replacement identity.
		if record.SessionID == result.ID {
			if strings.TrimSpace(record.AITitle) != "" {
				generatedTitle = cleanTitle(record.AITitle)
			}
			if strings.TrimSpace(record.AgentName) != "" {
				explicitTitle = cleanTitle(record.AgentName)
			}
		}

		if record.UUID == "" {
			continue
		}
		node := &claudeNode{record: record, order: order, startOffset: startOffset, endOffset: len(raw) - len(remaining)}
		node.timestamp = parseTimestamp(record.Timestamp)
		if rawJSONPresent(record.Message) {
			if err := json.Unmarshal(record.Message, &node.message); err != nil {
				return Session{}, fmt.Errorf("parse Claude Code session %q message record %d: %w", result.ID, order, err)
			}
			if record.Type == "assistant" && node.message.StopReason != "" {
				anyStopReason = true
			}
		}
		occurrences[record.UUID] = append(occurrences[record.UUID], node)
		if record.SessionID == result.ID && (record.Type == "user" || record.Type == "assistant") {
			anchor = node
		}
	}
	// A direct JSONL can contain only bridge/management records after a move or
	// copy. It is not a user conversation and must not turn an otherwise useful
	// export into a partial failure.
	if anchor == nil {
		if partial {
			return Session{}, fmt.Errorf("%w: Claude Code source ends with an incomplete JSONL record", errSourceBusy)
		}
		return result, nil
	}

	chain := make([]*claudeNode, 0, len(occurrences))
	seen := make(map[*claudeNode]bool)
	for current := anchor; current != nil; {
		if seen[current] {
			return Session{}, fmt.Errorf("Claude Code session %q contains a parent cycle on its selected conversation path", result.ID)
		}
		seen[current] = true
		if err := validateClaudeReplay(current, occurrences[current.record.UUID]); err != nil {
			return Session{}, fmt.Errorf("Claude Code session %q duplicate UUID %q: %w", result.ID, current.record.UUID, err)
		}
		if current.record.Timestamp != "" && current.timestamp.IsZero() && (current.record.Type == "user" || current.record.Type == "assistant") {
			return Session{}, fmt.Errorf("Claude Code session %q %s record %d has an invalid timestamp", result.ID, current.record.Type, current.order)
		}
		chain = append(chain, current)
		if current.record.ParentUUID == "" {
			break
		}
		parent, err := claudeParentOccurrence(current, occurrences[current.record.ParentUUID])
		if err != nil {
			return Session{}, fmt.Errorf("Claude Code session %q current conversation parent: %w", result.ID, err)
		}
		current = parent
	}
	for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
		chain[left], chain[right] = chain[right], chain[left]
	}
	lastTerminal := -1
	markerSeen := anyStopReason
	for index, node := range chain {
		if node.record.Type != "assistant" || node.message.StopReason == "" {
			continue
		}
		markerSeen = true
		if node.message.StopReason == "end_turn" || node.message.StopReason == "stop_sequence" {
			lastTerminal = index
		}
	}
	activeStart := -1
	for index := lastTerminal + 1; index < len(chain); index++ {
		node := chain[index]
		if node.record.Type == "assistant" || (node.record.Type == "user" &&
			!rawJSONPresent(node.record.ToolUseResult) && node.record.SourceToolAssistantUUID == "" &&
			!node.record.IsMeta && node.record.Origin.Kind != "task-notification" && node.record.PromptSource != "system") {
			activeStart = node.startOffset
			break
		}
	}
	if partial || (markerSeen && activeStart >= 0) {
		if lastTerminal < 0 {
			return Session{}, fmt.Errorf("%w: Claude Code session has no completed turn", errSourceBusy)
		}
		cutoff := len(raw)
		if activeStart >= 0 {
			cutoff = activeStart
			if cutoff < chain[lastTerminal].endOffset {
				return Session{}, fmt.Errorf("%w: Claude Code completion boundary is out of order", errSourceBusy)
			}
		}
		return parseClaudeSession(raw[:cutoff], fallbackID)
	}
	selectedUUIDs := make(map[string]bool, len(chain))
	for _, node := range chain {
		selectedUUIDs[node.record.UUID] = true
		if node.record.CWD != "" {
			result.CWD = node.record.CWD
			result.WorkspaceCandidates = append(result.WorkspaceCandidates, node.record.CWD)
		}
		if node.record.RelocatedCWD != "" {
			result.CWD = node.record.RelocatedCWD
			result.WorkspaceCandidates = append(result.WorkspaceCandidates, node.record.RelocatedCWD)
		}
		if node.record.GitBranch != "" {
			result.Branch = node.record.GitBranch
		}
		if node.record.Version != "" {
			result.ClaudeVersion = node.record.Version
		}
		if node.record.IsSidechain || strings.TrimSpace(node.record.AgentID) != "" {
			result.ExcludeReason = "subagent"
		}
	}
	result.AlternateBranches = len(occurrences) - len(selectedUUIDs)

	closedAssistantIDs := make(map[string]bool)
	var assistant *claudeAssistantGroup
	flushAssistant := func() {
		if assistant == nil {
			return
		}
		closedAssistantIDs[assistant.id] = true
		text := strings.TrimSpace(strings.Join(assistant.texts, "\n\n"))
		if text != "" {
			result.Messages = append(result.Messages, Message{Role: "assistant", Text: text, Timestamp: assistant.timestamp})
		}
		assistant = nil
	}
	for _, node := range chain {
		if !node.timestamp.IsZero() {
			if result.CreatedAt.IsZero() {
				result.CreatedAt = node.timestamp
			}
			if node.timestamp.After(result.LastEventAt) {
				result.LastEventAt = node.timestamp
			}
		}
		switch node.record.Type {
		case "assistant":
			if node.message.Role != "assistant" || strings.TrimSpace(node.message.ID) == "" {
				return Session{}, fmt.Errorf("Claude Code session %q assistant record %d has invalid role or message ID", result.ID, node.order)
			}
			if assistant == nil || assistant.id != node.message.ID {
				flushAssistant()
				if closedAssistantIDs[node.message.ID] {
					return Session{}, fmt.Errorf("Claude Code session %q assistant message %q is non-contiguous", result.ID, node.message.ID)
				}
				assistant = &claudeAssistantGroup{id: node.message.ID, timestamp: node.timestamp}
			}
			texts, toolCalls, err := claudeAssistantParts(node)
			if err != nil {
				return Session{}, fmt.Errorf("Claude Code session %q assistant record %d: %w", result.ID, node.order, err)
			}
			assistant.texts = append(assistant.texts, texts...)
			result.ToolCallCount += toolCalls
		case "user":
			message, filtered, err := claudeUserMessage(node)
			if err != nil {
				return Session{}, fmt.Errorf("Claude Code session %q user record %d: %w", result.ID, node.order, err)
			}
			if filtered {
				result.FilteredUserInput++
				continue
			}
			if message != nil {
				flushAssistant()
				result.Messages = append(result.Messages, *message)
			}
		case "attachment", "system":
			// Runtime UI state and diagnostics are graph nodes, not conversation
			// messages. They remain part of source/omitted counts only.
		default:
			return Session{}, fmt.Errorf("unsupported UUID record type %q", node.record.Type)
		}
	}
	flushAssistant()

	for _, message := range result.Messages {
		switch message.Role {
		case "user":
			result.UserMessages++
		case "assistant":
			result.AssistantMessages++
		}
		if !message.Timestamp.IsZero() {
			if result.FirstMessageAt.IsZero() || message.Timestamp.Before(result.FirstMessageAt) {
				result.FirstMessageAt = message.Timestamp
			}
			if message.Timestamp.After(result.LastMessageAt) {
				result.LastMessageAt = message.Timestamp
			}
		}
	}
	result.OmittedCount = result.RecordCount - len(result.Messages)
	if result.OmittedCount < 0 {
		result.OmittedCount = 0
	}
	if result.ExcludeReason == "" && result.UserMessages == 0 && result.FilteredUserInput > 0 {
		result.ExcludeReason = "runtime_context"
	}
	switch {
	case explicitTitle != "":
		result.Title = explicitTitle
	case generatedTitle != "":
		result.Title = generatedTitle
	default:
		for _, message := range result.Messages {
			if message.Role == "user" && strings.TrimSpace(message.Text) != "" {
				result.Title = cleanTitle(message.Text)
				break
			}
		}
	}
	if result.Title == "" {
		result.Title = "Claude Code session " + result.ID
	}
	return result, nil
}

func claudeUserMessage(node *claudeNode) (*Message, bool, error) {
	if node.message.Role != "user" {
		return nil, false, fmt.Errorf("message has role %q", node.message.Role)
	}
	blocks, contentString, err := decodeClaudeContent(node.message.Content)
	if err != nil {
		return nil, false, err
	}
	toolResult := rawJSONPresent(node.record.ToolUseResult) || node.record.SourceToolAssistantUUID != ""
	for _, block := range blocks {
		if block.Type == "tool_result" {
			toolResult = true
		}
	}
	if toolResult || node.record.IsMeta || node.record.Origin.Kind == "task-notification" || node.record.PromptSource == "system" {
		return nil, true, nil
	}
	if node.record.Origin.Kind != "" && node.record.Origin.Kind != "human" {
		return nil, false, fmt.Errorf("unsupported user origin %q", node.record.Origin.Kind)
	}
	if node.record.UserType != "" && node.record.UserType != "external" {
		return nil, false, fmt.Errorf("unsupported user type %q", node.record.UserType)
	}

	texts := make([]string, 0)
	attachments := make([]Attachment, 0)
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text == "" || claudeStandaloneContext(text) ||
				(node.record.Origin.Kind == "" && node.record.PromptSource == "" && strings.HasPrefix(text, "[")) {
				continue
			}
			texts = append(texts, text)
		case "image":
			if block.Source.Type != "base64" || !strings.HasPrefix(strings.ToLower(block.Source.MediaType), "image/") || block.Source.Data == "" {
				return nil, false, fmt.Errorf("invalid structured image block")
			}
			attachments = append(attachments, Attachment{
				Name: "image", MIMEType: block.Source.MediaType, SourceKind: "embedded_data",
				SourceValue: "data:" + block.Source.MediaType + ";base64," + block.Source.Data,
			})
		case "document":
			if block.Source.Type != "text" || strings.TrimSpace(block.Source.MediaType) == "" || block.Source.Data == "" {
				return nil, false, fmt.Errorf("invalid structured document block")
			}
			name := strings.TrimSpace(block.Title)
			if name == "" {
				name = "document"
			}
			attachments = append(attachments, Attachment{
				Name: name, MIMEType: block.Source.MediaType, SourceKind: "embedded_bytes", Data: []byte(block.Source.Data),
			})
		case "tool_result":
			return nil, true, nil
		default:
			return nil, false, fmt.Errorf("unsupported user content block %q", block.Type)
		}
	}
	if contentString != "" && len(blocks) == 0 {
		text := strings.TrimSpace(contentString)
		if claudeStandaloneContext(text) ||
			(node.record.Origin.Kind == "" && node.record.PromptSource == "" && strings.HasPrefix(text, "[")) {
			return nil, true, nil
		}
		if text != "" {
			texts = append(texts, text)
		}
	}
	if node.record.Origin.Kind == "" && node.record.PromptSource == "" && len(texts) == 0 && len(attachments) == 0 {
		return nil, true, nil
	}
	if len(texts) == 0 && len(attachments) == 0 {
		return nil, true, nil
	}
	return &Message{Role: "user", Text: strings.Join(texts, "\n\n"), Timestamp: node.timestamp, Attachments: attachments}, false, nil
}

func claudeAssistantParts(node *claudeNode) ([]string, int, error) {
	blocks, contentString, err := decodeClaudeContent(node.message.Content)
	if err != nil {
		return nil, 0, err
	}
	texts := make([]string, 0)
	toolCalls := 0
	runtimeDiagnostic := node.record.IsAPIErrorMessage || rawJSONPresent(node.record.Error) || node.message.Model == "<synthetic>"
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if !runtimeDiagnostic && strings.TrimSpace(block.Text) != "" {
				texts = append(texts, block.Text)
			}
		case "tool_use", "server_tool_use":
			toolCalls++
		case "thinking", "redacted_thinking", "tool_result":
			// Intentionally omitted.
		default:
			return nil, 0, fmt.Errorf("unsupported assistant content block %q", block.Type)
		}
	}
	if len(blocks) == 0 && contentString != "" && !runtimeDiagnostic {
		texts = append(texts, contentString)
	}
	return texts, toolCalls, nil
}

func decodeClaudeContent(raw json.RawMessage) ([]claudeContentBlock, string, error) {
	if !rawJSONPresent(raw) {
		return nil, "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return nil, text, nil
	}
	var blocks []claudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, "", fmt.Errorf("parse message content: %w", err)
	}
	return blocks, "", nil
}

func claudeStandaloneContext(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	for _, envelope := range [][2]string{
		{"<ide_opened_file>", "</ide_opened_file>"},
		{"<ide_selection>", "</ide_selection>"},
		{"<system-reminder>", "</system-reminder>"},
		{"<command-name>", "</command-name>"},
		{"<local-command-stdout>", "</local-command-stdout>"},
		{"<local-command-caveat>", "</local-command-caveat>"},
		{"<task-notification>", "</task-notification>"},
	} {
		if strings.HasPrefix(value, envelope[0]) && strings.HasSuffix(value, envelope[1]) {
			return true
		}
	}
	for _, prefix := range []string{"<command-name>", "<local-command-stdout>", "<local-command-caveat>"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return strings.HasPrefix(value, "[Request interrupted by user]")
}

func rawJSONPresent(value json.RawMessage) bool {
	value = bytes.TrimSpace(value)
	return len(value) > 0 && !bytes.Equal(value, []byte("null"))
}

func claudeParentOccurrence(child *claudeNode, candidates []*claudeNode) (*claudeNode, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("missing parent %q", child.record.ParentUUID)
	}
	var previous *claudeNode
	forward := make([]*claudeNode, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.order < child.order {
			if previous == nil || candidate.order > previous.order {
				previous = candidate
			}
			continue
		}
		forward = append(forward, candidate)
	}
	if previous != nil {
		return previous, nil
	}
	if len(forward) == 1 {
		return forward[0], nil
	}
	return nil, fmt.Errorf("parent %q has %d ambiguous forward occurrences", child.record.ParentUUID, len(forward))
}

func validateClaudeReplay(selected *claudeNode, candidates []*claudeNode) error {
	for _, candidate := range candidates {
		if candidate == selected {
			continue
		}
		if selected.record.Type != candidate.record.Type ||
			!sameClaudeTimestamp(selected, candidate) ||
			selected.message.Role != candidate.message.Role ||
			selected.message.ID != candidate.message.ID {
			return fmt.Errorf("replayed node changes its type, timestamp, role, or message ID")
		}
		left, err := canonicalClaudeJSON(selected.message.Content)
		if err != nil {
			return err
		}
		right, err := canonicalClaudeJSON(candidate.message.Content)
		if err != nil {
			return err
		}
		if left != right {
			return fmt.Errorf("replayed node changes visible message content")
		}
	}
	return nil
}

func sameClaudeTimestamp(left, right *claudeNode) bool {
	if !left.timestamp.IsZero() || !right.timestamp.IsZero() {
		return left.timestamp.Equal(right.timestamp)
	}
	return left.record.Timestamp == right.record.Timestamp
}

func canonicalClaudeJSON(value json.RawMessage) (string, error) {
	if !rawJSONPresent(value) {
		return "", nil
	}
	var decoded interface{}
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "", fmt.Errorf("parse replay message content: %w", err)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
