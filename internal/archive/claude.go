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
	Origin                  struct {
		Kind string `json:"kind"`
	} `json:"origin"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
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
	record    claudeRecord
	message   claudeMessage
	timestamp time.Time
	order     int
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
	if !completeJSONL(raw) {
		return Session{}, fmt.Errorf("%w: Claude Code source ends with an incomplete JSONL record", errSourceBusy)
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

	nodes := make(map[string]*claudeNode)
	lastUUID := ""
	explicitTitle := ""
	generatedTitle := ""
	latestCWD := ""
	latestBranch := ""
	latestVersion := ""
	internal := false
	remaining := raw
	order := 0
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
		order++
		result.RecordCount++
		var record claudeRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return Session{}, fmt.Errorf("parse Claude Code session %q record %d: %w", result.ID, order, err)
		}
		if record.SessionID != "" && record.SessionID != result.ID {
			return Session{}, fmt.Errorf("Claude Code transcript filename ID %q does not match record session ID %q", result.ID, record.SessionID)
		}
		if record.CWD != "" {
			latestCWD = record.CWD
		}
		if record.RelocatedCWD != "" {
			latestCWD = record.RelocatedCWD
		}
		if record.GitBranch != "" {
			latestBranch = record.GitBranch
		}
		if record.Version != "" {
			latestVersion = record.Version
		}
		if strings.TrimSpace(record.AITitle) != "" {
			generatedTitle = cleanTitle(record.AITitle)
		}
		if strings.TrimSpace(record.AgentName) != "" {
			explicitTitle = cleanTitle(record.AgentName)
		}
		if record.IsSidechain || strings.TrimSpace(record.AgentID) != "" {
			internal = true
		}

		if record.UUID == "" {
			if record.Type == "user" || record.Type == "assistant" || record.Type == "attachment" || record.Type == "system" {
				return Session{}, fmt.Errorf("Claude Code session %q %s record %d has no UUID", result.ID, record.Type, order)
			}
			continue
		}
		if record.SessionID == "" {
			return Session{}, fmt.Errorf("Claude Code session %q UUID record %d has no session ID", result.ID, order)
		}
		if _, exists := nodes[record.UUID]; exists {
			return Session{}, fmt.Errorf("Claude Code session %q has duplicate UUID %q", result.ID, record.UUID)
		}
		node := &claudeNode{record: record, order: order}
		if record.Timestamp != "" {
			node.timestamp = parseTimestamp(record.Timestamp)
			if node.timestamp.IsZero() && (record.Type == "user" || record.Type == "assistant") {
				return Session{}, fmt.Errorf("Claude Code session %q %s record %d has an invalid timestamp", result.ID, record.Type, order)
			}
		}
		if rawJSONPresent(record.Message) {
			if err := json.Unmarshal(record.Message, &node.message); err != nil {
				return Session{}, fmt.Errorf("parse Claude Code session %q message record %d: %w", result.ID, order, err)
			}
		}
		nodes[record.UUID] = node
		lastUUID = record.UUID
	}
	if len(nodes) == 0 || lastUUID == "" {
		return Session{}, fmt.Errorf("Claude Code session %q has no conversation graph", result.ID)
	}

	children := make(map[string]int)
	roots := 0
	for id, node := range nodes {
		if node.record.ParentUUID == "" {
			roots++
			continue
		}
		if _, exists := nodes[node.record.ParentUUID]; !exists {
			return Session{}, fmt.Errorf("Claude Code session %q node %q references missing parent %q", result.ID, id, node.record.ParentUUID)
		}
		children[node.record.ParentUUID]++
	}
	if roots != 1 {
		return Session{}, fmt.Errorf("Claude Code session %q has %d graph roots, want 1", result.ID, roots)
	}
	states := make(map[string]uint8, len(nodes))
	var visit func(string) error
	visit = func(id string) error {
		switch states[id] {
		case 1:
			return fmt.Errorf("Claude Code session %q contains a parent cycle at %q", result.ID, id)
		case 2:
			return nil
		}
		states[id] = 1
		if parent := nodes[id].record.ParentUUID; parent != "" {
			if err := visit(parent); err != nil {
				return err
			}
		}
		states[id] = 2
		return nil
	}
	for id := range nodes {
		if err := visit(id); err != nil {
			return Session{}, err
		}
	}
	if children[lastUUID] != 0 {
		return Session{}, fmt.Errorf("Claude Code session %q final UUID record is not a graph leaf", result.ID)
	}

	chain := make([]*claudeNode, 0, len(nodes))
	for id := lastUUID; id != ""; id = nodes[id].record.ParentUUID {
		chain = append(chain, nodes[id])
	}
	for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
		chain[left], chain[right] = chain[right], chain[left]
	}
	result.AlternateBranches = len(nodes) - len(chain)
	result.CWD = latestCWD
	result.Branch = latestBranch
	result.ClaudeVersion = latestVersion
	if internal {
		result.ExcludeReason = "subagent"
	}

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
