package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sessionmgr/sessionmgr/internal/canonical"
	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/ids"
	"github.com/sessionmgr/sessionmgr/internal/secretscan"
	"github.com/sessionmgr/sessionmgr/internal/store"
)

const AdapterVersion = "1"

var safeNativeID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Candidate struct {
	Path         string
	NativeID     string
	CWD          string
	StartedAt    time.Time
	CodexVersion string
	Title        string
}

type Query struct {
	Repo      string
	SessionID string
	Latest    bool
}

type CaptureResult struct {
	Session  domain.AgentSession
	Objects  []domain.ObjectDescriptor
	Findings []domain.SecurityFinding
	Events   int64
	Unknown  int64
}

func StateRoot() (string, error) {
	root := os.Getenv("CODEX_HOME")
	if root != "" {
		return filepath.Abs(root)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".codex"), nil
}

func Discover(query Query) ([]Candidate, error) {
	root, err := StateRoot()
	if err != nil {
		return nil, err
	}
	var candidates []Candidate
	for _, subdir := range []string{"sessions", "archived_sessions"} {
		base := filepath.Join(root, subdir)
		_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
				return nil
			}
			candidate, err := readMetadata(path)
			if err != nil {
				return nil
			}
			if query.SessionID != "" && candidate.NativeID != query.SessionID {
				return nil
			}
			if query.SessionID == "" && query.Repo != "" && !samePath(candidate.CWD, query.Repo) {
				return nil
			}
			candidates = append(candidates, candidate)
			return nil
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].StartedAt.After(candidates[j].StartedAt) })
	return candidates, nil
}

func Select(query Query) (Candidate, error) {
	candidates, err := Discover(query)
	if err != nil {
		return Candidate{}, err
	}
	if len(candidates) == 0 {
		if query.SessionID != "" {
			return Candidate{}, fmt.Errorf("Codex session %q not found", query.SessionID)
		}
		return Candidate{}, fmt.Errorf("no Codex session found for workspace %s", query.Repo)
	}
	if query.SessionID != "" || query.Latest || len(candidates) == 1 {
		return candidates[0], nil
	}
	return Candidate{}, fmt.Errorf("multiple Codex sessions match; use --session ID or --latest")
}

func Capture(ctx context.Context, objectStore *store.Store, candidate Candidate) (CaptureResult, error) {
	raw, err := readStable(ctx, candidate.Path)
	if err != nil {
		return CaptureResult{}, err
	}
	rawDesc, err := objectStore.PutBytes(raw, "application/vnd.sessionmgr.codex-session+jsonl", true)
	if err != nil {
		return CaptureResult{}, err
	}
	sessionID, err := ids.NewPrefixed("ses")
	if err != nil {
		return CaptureResult{}, err
	}
	events, unknown, err := normalize(raw, sessionID, rawDesc.Digest)
	if err != nil {
		return CaptureResult{}, err
	}
	var normalized bytes.Buffer
	for _, event := range events {
		line, err := canonical.Marshal(event)
		if err != nil {
			return CaptureResult{}, err
		}
		normalized.Write(line)
		normalized.WriteByte('\n')
	}
	normalizedDesc, err := objectStore.PutBytes(normalized.Bytes(), "application/vnd.sessionmgr.normalized-events.v1+jsonl", true)
	if err != nil {
		return CaptureResult{}, err
	}
	session := domain.AgentSession{
		ID:               sessionID,
		Platform:         "codex",
		NativeID:         candidate.NativeID,
		NativeVersion:    candidate.CodexVersion,
		AdapterVersion:   AdapterVersion,
		SourceCWDHint:    candidate.CWD,
		StartedAt:        candidate.StartedAt.UTC(),
		RawObjects:       []string{rawDesc.Digest},
		NormalizedObject: normalizedDesc.Digest,
		Capabilities: domain.AdapterCapabilities{
			Archive: true, Normalize: true, NativeRestore: "experimental", Handoff: true,
		},
	}
	return CaptureResult{
		Session:  session,
		Objects:  []domain.ObjectDescriptor{rawDesc, normalizedDesc},
		Findings: secretscan.Scan("codex:session:"+candidate.NativeID, raw),
		Events:   int64(len(events)),
		Unknown:  unknown,
	}, nil
}

func RestoreNative(objectStore *store.Store, session domain.AgentSession, targetWorktree string) (string, error) {
	if !safeNativeID.MatchString(session.NativeID) {
		return "failed", fmt.Errorf("unsafe Codex native session ID %q", session.NativeID)
	}
	if len(session.RawObjects) == 0 {
		return "failed", fmt.Errorf("Codex session has no raw object")
	}
	raw, err := objectStore.Get(session.RawObjects[0])
	if err != nil {
		return "failed", err
	}
	root, err := StateRoot()
	if err != nil {
		return "failed", err
	}
	date := session.StartedAt
	if date.IsZero() {
		date = time.Now()
	}
	dir := filepath.Join(root, "sessions", date.Format("2006"), date.Format("01"), date.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "failed", err
	}
	existing, err := findNativeID(root, session.NativeID)
	if err != nil {
		return "failed", err
	}
	if existing != "" {
		data, readErr := os.ReadFile(existing)
		if readErr != nil {
			return "failed", readErr
		}
		if store.Digest(data) == store.Digest(raw) {
			return "supported", nil
		}
		return "failed", fmt.Errorf("Codex native ID %s already exists with different content", session.NativeID)
	}
	filename := fmt.Sprintf("sessionmgr-%s-%s.jsonl", date.UTC().Format("2006-01-02T15-04-05"), session.NativeID)
	path := filepath.Join(dir, filename)
	if _, err := os.Lstat(path); err == nil {
		return "failed", fmt.Errorf("Codex session destination already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return "failed", err
	}
	if err := atomicWrite(path, raw); err != nil {
		return "failed", err
	}
	_ = targetWorktree // Codex is launched with -C; raw source cwd remains preserved.
	return "experimental", nil
}

func readMetadata(path string) (Candidate, error) {
	file, err := os.Open(path)
	if err != nil {
		return Candidate{}, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		return Candidate{}, fmt.Errorf("empty session")
	}
	var record map[string]interface{}
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		return Candidate{}, err
	}
	payload := asMap(record["payload"])
	nativeID := firstString(payload["id"], record["id"], payload["session_id"])
	if nativeID == "" {
		nativeID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	cwd := firstString(payload["cwd"], record["cwd"])
	version := firstString(payload["cli_version"], payload["version"], record["version"])
	started := parseTime(firstString(record["timestamp"], payload["timestamp"]))
	if started.IsZero() {
		if info, statErr := file.Stat(); statErr == nil {
			started = info.ModTime()
		}
	}
	return Candidate{
		Path: path, NativeID: nativeID, CWD: cwd,
		StartedAt: started.UTC(), CodexVersion: version,
	}, nil
}

func readStable(ctx context.Context, path string) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
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
	return nil, fmt.Errorf("Codex session changed while being captured; stop the active session and retry")
}

func normalize(raw []byte, sessionID, rawDigest string) ([]domain.NormalizedEvent, int64, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var events []domain.NormalizedEvent
	var sequence, unknown int64
	for scanner.Scan() {
		sequence++
		var record map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			unknown++
			continue
		}
		recordType := firstString(record["type"])
		payload := asMap(record["payload"])
		payloadType := firstString(payload["type"])
		actor, kind, known := classify(recordType, payloadType, payload)
		if !known {
			unknown++
		}
		summary, safePayload := summarize(recordType, payloadType, payload)
		eventID, err := ids.NewPrefixed("evt")
		if err != nil {
			return nil, unknown, err
		}
		events = append(events, domain.NormalizedEvent{
			SchemaVersion: domain.SchemaVersion,
			EventID:       eventID,
			SessionID:     sessionID,
			Sequence:      sequence,
			Timestamp:     parseTime(firstString(record["timestamp"], payload["timestamp"])),
			Actor:         actor,
			Kind:          kind,
			Summary:       summary,
			Payload:       safePayload,
			Source:        domain.EventSource{RawObject: rawDigest, Record: sequence},
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, unknown, err
	}
	return events, unknown, nil
}

func classify(recordType, payloadType string, payload map[string]interface{}) (string, string, bool) {
	switch payloadType {
	case "user_message":
		return "user", "message", true
	case "agent_message", "assistant_message", "reasoning":
		return "assistant", "message", true
	case "function_call", "tool_call":
		return "tool", "tool_call", true
	case "function_call_output", "tool_result":
		return "tool", "tool_result", true
	case "error":
		return "system", "error", true
	}
	switch recordType {
	case "session_meta":
		return "system", "checkpoint", true
	case "turn_context":
		return "system", "checkpoint", true
	case "event_msg", "response_item":
		return "system", "message", payloadType != ""
	}
	if role := firstString(payload["role"]); role == "user" || role == "assistant" || role == "system" {
		return role, "message", true
	}
	return "system", "message", false
}

func summarize(recordType, payloadType string, payload map[string]interface{}) (string, map[string]interface{}) {
	switch payloadType {
	case "function_call", "tool_call":
		name := firstString(payload["name"], payload["tool_name"])
		return trimSummary("Tool call: " + name), map[string]interface{}{"tool_name": name, "status": "requested"}
	case "function_call_output", "tool_result":
		name := firstString(payload["name"], payload["tool_name"])
		return trimSummary("Tool result: " + name), map[string]interface{}{"tool_name": name, "status": "completed"}
	case "error":
		return trimSummary("Error: " + firstString(payload["message"])), nil
	}
	text := firstString(payload["message"], payload["text"], payload["summary"])
	if text == "" {
		text = contentText(payload["content"])
	}
	if text == "" {
		if payloadType != "" {
			text = payloadType
		} else {
			text = recordType
		}
	}
	return trimSummary(text), nil
}

func contentText(value interface{}) string {
	switch item := value.(type) {
	case string:
		return item
	case []interface{}:
		var pieces []string
		for _, child := range item {
			if text := contentText(child); text != "" {
				pieces = append(pieces, text)
			}
		}
		return strings.Join(pieces, " ")
	case map[string]interface{}:
		return firstString(item["text"], item["message"])
	default:
		return ""
	}
}

func trimSummary(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 240 {
		return string(runes[:237]) + "..."
	}
	return value
}

func asMap(value interface{}) map[string]interface{} {
	if result, ok := value.(map[string]interface{}); ok {
		return result
	}
	return map[string]interface{}{}
}

func firstString(values ...interface{}) string {
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			return text
		}
	}
	return ""
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aAbs, aErr := filepath.Abs(a)
	bAbs, bErr := filepath.Abs(b)
	if aErr != nil || bErr != nil {
		return false
	}
	aResolved, aErr := filepath.EvalSymlinks(aAbs)
	bResolved, bErr := filepath.EvalSymlinks(bAbs)
	if aErr == nil {
		aAbs = aResolved
	}
	if bErr == nil {
		bAbs = bResolved
	}
	return aAbs == bAbs
}

func findNativeID(root, nativeID string) (string, error) {
	var match string
	for _, subdir := range []string{"sessions", "archived_sessions"} {
		err := filepath.WalkDir(filepath.Join(root, subdir), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			candidate, err := readMetadata(path)
			if err == nil && candidate.NativeID == nativeID {
				match = path
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if match != "" {
			return match, nil
		}
	}
	return "", nil
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
