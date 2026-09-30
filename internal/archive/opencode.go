package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const harnessOpenCode = "opencode"

type openCodeSessionRow struct {
	id, directory, title, version string
	parentID                      sql.NullString
	created, updated, modified    int64
}

type openCodeMessageRow struct {
	id, data         string
	created, updated int64
}

type openCodeReadResult struct {
	sessions       []Session
	sources        int
	busy           int
	skipped        int
	warnings       []string
	ignored        int
	busySessionIDs []string
}

type openCodeMessageData struct {
	Role   string `json:"role"`
	Finish string `json:"finish"`
	Time   struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

type openCodePartData struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	URL      string `json:"url"`
	MIMEType string `json:"mime"`
	Filename string `json:"filename"`
}

func DefaultOpenCodeDB() (string, error) { return resolveOpenCodeDB("") }

func resolveOpenCodeDB(configured string) (string, error) {
	value := strings.TrimSpace(configured)
	if value != "" {
		if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			if value == "~" {
				value = home
			} else {
				value = filepath.Join(home, value[2:])
			}
		}
		return filepath.Abs(value)
	}
	root := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if root == "" {
		if runtime.GOOS == "windows" {
			root = strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		}
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "share")
	}
	return filepath.Abs(filepath.Join(root, "opencode", "opencode.db"))
}

func readOpenCodeSessions(ctx context.Context, path string, window time.Duration) (openCodeReadResult, error) {
	return readOpenCodeSessionsSelected(ctx, path, window, nil)
}

func readOpenCodeSessionsSelected(ctx context.Context, path string, _ time.Duration, selectSession func(openCodeSessionRow) bool) (openCodeReadResult, error) {
	result := openCodeReadResult{sessions: []Session{}}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, fmt.Errorf("OpenCode database is not a regular file: %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return result, err
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return result, fmt.Errorf("open OpenCode database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return result, fmt.Errorf("open OpenCode database read-only: %w", err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, fmt.Errorf("snapshot OpenCode database: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT s.id, s.directory, s.title, s.version, s.parent_id, s.time_created, s.time_updated,
		max(s.time_created, s.time_updated,
			coalesce((SELECT max(time_updated) FROM message WHERE session_id = s.id), 0),
			coalesce((SELECT max(time_updated) FROM part WHERE session_id = s.id), 0))
		FROM session s ORDER BY s.time_created, s.id`)
	if err != nil {
		return result, fmt.Errorf("read OpenCode sessions: %w", err)
	}
	var headers []openCodeSessionRow
	for rows.Next() {
		var row openCodeSessionRow
		if err := rows.Scan(&row.id, &row.directory, &row.title, &row.version, &row.parentID, &row.created, &row.updated, &row.modified); err != nil {
			rows.Close()
			return result, fmt.Errorf("read OpenCode session row: %w", err)
		}
		headers = append(headers, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	result.sources = len(headers)
	for _, header := range headers {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if selectSession != nil && !selectSession(header) {
			result.ignored++
			continue
		}
		session, activeTail, err := readOpenCodeSession(ctx, tx, header)
		if err != nil {
			if sourceErrorIsBusy(err) {
				result.busy++
				result.busySessionIDs = append(result.busySessionIDs, header.id)
				continue
			}
			result.skipped++
			result.warnings = append(result.warnings, fmt.Sprintf("OpenCode session %s: %v", header.id, err))
			continue
		}
		if activeTail {
			result.busy++
			result.busySessionIDs = append(result.busySessionIDs, header.id)
		}
		result.sessions = append(result.sessions, session)
	}
	return result, nil
}

func readOpenCodeSession(ctx context.Context, tx *sql.Tx, header openCodeSessionRow) (Session, bool, error) {
	if header.id == "" || header.created <= 0 || header.updated <= 0 || !filepath.IsAbs(header.directory) {
		return Session{}, false, fmt.Errorf("invalid session identity, time, or directory")
	}
	result := Session{
		ID: header.id, Harness: harnessOpenCode, Originator: "OpenCode", SourceKind: harnessOpenCode,
		CWD: header.directory, Title: cleanTitle(header.title), CreatedAt: time.UnixMilli(header.created).UTC(),
		RecordCount: 1,
	}
	if header.parentID.Valid && header.parentID.String != "" {
		result.ParentThreadID = header.parentID.String
		result.ExcludeReason = "subagent"
	}
	h := sha256.New()
	hashOpenCodeField(h, header.id)
	hashOpenCodeField(h, header.directory)
	hashOpenCodeField(h, header.title)
	hashOpenCodeField(h, header.version)
	hashOpenCodeField(h, header.parentID.String)
	hashOpenCodeInt(h, header.created)

	rows, err := tx.QueryContext(ctx, `SELECT id, data, time_created, time_updated FROM message
		WHERE session_id = ? ORDER BY time_created, id`, header.id)
	if err != nil {
		return Session{}, false, err
	}
	var messages []openCodeMessageRow
	for rows.Next() {
		var row openCodeMessageRow
		if err := rows.Scan(&row.id, &row.data, &row.created, &row.updated); err != nil {
			rows.Close()
			return Session{}, false, err
		}
		messages = append(messages, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Session{}, false, err
	}
	rows.Close()
	lastCompleted := -1
	lastConversation := -1
	for index, row := range messages {
		var info openCodeMessageData
		if err := json.Unmarshal([]byte(row.data), &info); err != nil {
			return Session{}, false, fmt.Errorf("message %q has invalid JSON: %w", row.id, err)
		}
		if info.Role != "user" && info.Role != "assistant" {
			continue
		}
		lastConversation = index
		if info.Role == "assistant" && info.Time.Completed > 0 && info.Finish != "tool-calls" {
			lastCompleted = index
		}
	}
	activeTail := lastConversation > lastCompleted
	if result.ExcludeReason != "" {
		activeTail = false
	}
	if activeTail && lastCompleted < 0 && result.ExcludeReason == "" {
		return Session{}, false, fmt.Errorf("%w: no completed OpenCode turn", errSourceBusy)
	}
	if activeTail && lastCompleted >= 0 {
		messages = messages[:lastCompleted+1]
	}
	if len(messages) > 0 {
		result.LastEventAt = time.UnixMilli(messages[len(messages)-1].updated).UTC()
	} else {
		result.LastEventAt = time.UnixMilli(header.updated).UTC()
	}
	for _, row := range messages {
		hashOpenCodeField(h, row.id)
		hashOpenCodeField(h, row.data)
		hashOpenCodeInt(h, row.created)
		hashOpenCodeInt(h, row.updated)
		result.RecordCount++
		var info openCodeMessageData
		if err := json.Unmarshal([]byte(row.data), &info); err != nil {
			return Session{}, false, fmt.Errorf("message %q has invalid JSON: %w", row.id, err)
		}
		if info.Role == "assistant" && info.Time.Completed <= 0 {
			return Session{}, false, fmt.Errorf("%w: OpenCode assistant message %q is not complete", errSourceBusy, row.id)
		}
		parts, err := tx.QueryContext(ctx, `SELECT id, data, time_created, time_updated FROM part
			WHERE session_id = ? AND message_id = ? ORDER BY time_created, id`, header.id, row.id)
		if err != nil {
			return Session{}, false, err
		}
		texts := make([]string, 0)
		attachments := make([]Attachment, 0)
		for parts.Next() {
			var id, data string
			var created, updated int64
			if err := parts.Scan(&id, &data, &created, &updated); err != nil {
				parts.Close()
				return Session{}, false, err
			}
			hashOpenCodeField(h, id)
			hashOpenCodeField(h, data)
			hashOpenCodeInt(h, created)
			hashOpenCodeInt(h, updated)
			result.RecordCount++
			var part openCodePartData
			if err := json.Unmarshal([]byte(data), &part); err != nil {
				parts.Close()
				return Session{}, false, fmt.Errorf("part %q has invalid JSON: %w", id, err)
			}
			switch part.Type {
			case "text":
				if strings.TrimSpace(part.Text) != "" {
					texts = append(texts, part.Text)
				}
			case "tool":
				result.ToolCallCount++
			case "file":
				attachment := Attachment{Name: part.Filename, MIMEType: part.MIMEType}
				if strings.HasPrefix(part.URL, "data:") {
					attachment.SourceKind, attachment.SourceValue = "embedded_data", part.URL
				} else {
					attachment.SourceKind = "remote_reference"
				}
				attachments = append(attachments, attachment)
			case "reasoning", "step-start", "step-finish":
				// Internal model and tool bookkeeping is counted but not rendered.
			default:
				result.OmittedCount++
			}
		}
		if err := parts.Err(); err != nil {
			parts.Close()
			return Session{}, false, err
		}
		parts.Close()
		if info.Role != "user" && info.Role != "assistant" {
			result.OmittedCount++
			continue
		}
		text := strings.Join(texts, "\n\n")
		if strings.TrimSpace(text) == "" && len(attachments) == 0 {
			continue
		}
		at := time.UnixMilli(row.created).UTC()
		if info.Time.Created > 0 {
			at = time.UnixMilli(info.Time.Created).UTC()
		}
		result.Messages = append(result.Messages, Message{Role: info.Role, Text: text, Timestamp: at, Attachments: attachments})
		result.FirstMessageAt = earlierTime(result.FirstMessageAt, at)
		result.LastMessageAt = laterTime(result.LastMessageAt, at)
		if info.Role == "user" {
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
		result.Title = "OpenCode session " + shortSessionID(result.ID)
	}
	result.RawHash = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return result, activeTail, nil
}

func hashOpenCodeField(h hash.Hash, value string) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(value))
}

func hashOpenCodeInt(h hash.Hash, value int64) {
	var data [8]byte
	binary.LittleEndian.PutUint64(data[:], uint64(value))
	_, _ = h.Write(data[:])
}
