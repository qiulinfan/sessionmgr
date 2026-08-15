package archive

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	harnessCodex               = "codex"
	harnessDeepSeek            = "deepseek"
	deepSeekSessionFormat      = 0
	maxDeepSeekDecodedBytes    = int64(512 * 1024 * 1024)
	deepSeekZstdMagic          = uint32(0xfd2fb528)
	deepSeekCompressedFilename = "session.jsonl.zstd"
	deepSeekPlainFilename      = "session.jsonl"
)

type deepSeekHeader struct {
	Type            string `json:"type"`
	Version         int    `json:"version"`
	ID              string `json:"id"`
	CreatedAt       *int64 `json:"createdAt"`
	CWD             string `json:"cwd"`
	ParentSession   string `json:"parentSession"`
	Origin          string `json:"origin"`
	DelegationDepth *int   `json:"delegationDepth"`
	AgentPreset     string `json:"agentPreset"`
}

type deepSeekEvent struct {
	Type      string          `json:"type"`
	Seq       *int            `json:"seq"`
	Time      *int64          `json:"time"`
	Data      json.RawMessage `json:"data"`
	SurfaceOp json.RawMessage `json:"surfaceOp"`
	Ignorable bool            `json:"ignorable"`
	Seq0      *int            `json:"seq0"`
	Time0     *int64          `json:"time0"`
}

type deepSeekMessage struct {
	Role    string                 `json:"role"`
	Content []deepSeekContentBlock `json:"content"`
	Source  deepSeekMessageSource  `json:"source"`
}

type deepSeekMessageSource struct {
	Kind   string `json:"kind"`
	Plugin string `json:"plugin"`
}

type deepSeekContentBlock struct {
	Type       string                 `json:"type"`
	Text       string                 `json:"text"`
	Attachment *deepSeekAttachmentRef `json:"attachment"`
}

type deepSeekAttachmentRef struct {
	AttachmentID string `json:"attachmentId"`
	MediaType    string `json:"mediaType"`
	Bytes        int64  `json:"bytes"`
	Name         string `json:"name"`
}

func DefaultDeepSeekHome() (string, error) {
	return resolveDeepSeekHome("")
}

func resolveDeepSeekHome(configured string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(configured)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("DSH_HOME"))
	}
	if value == "" {
		value = filepath.Join(home, ".dsh")
	} else if value == "~" {
		value = home
	} else if strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		value = filepath.Join(home, value[2:])
	}
	return filepath.Abs(value)
}

func discoverDeepSeekSessionFiles(home string) ([]string, error) {
	base := filepath.Join(home, "sessions")
	paths := make([]string, 0)
	byDirectory := make(map[string]string)
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() != deepSeekCompressedFilename && entry.Name() != deepSeekPlainFilename {
			return nil
		}
		directory := filepath.Dir(path)
		if previous := byDirectory[directory]; previous != "" {
			return fmt.Errorf("DeepSeek session directory contains both supported encodings: %s and %s", previous, path)
		}
		byDirectory[directory] = path
		paths = append(paths, path)
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func parseDeepSeekSession(raw []byte, compressed bool, home string) (Session, error) {
	plaintext := raw
	if compressed {
		if err := validateDeepSeekZstdFrames(raw); err != nil {
			return Session{}, err
		}
		decoder, err := zstd.NewReader(bytes.NewReader(raw),
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(uint64(maxDeepSeekDecodedBytes)),
		)
		if err != nil {
			return Session{}, fmt.Errorf("open DeepSeek Zstandard session: %w", err)
		}
		plaintext, err = io.ReadAll(io.LimitReader(decoder, maxDeepSeekDecodedBytes+1))
		decoder.Close()
		if err != nil {
			return Session{}, fmt.Errorf("decode DeepSeek Zstandard session: %w", err)
		}
		if int64(len(plaintext)) > maxDeepSeekDecodedBytes {
			return Session{}, fmt.Errorf("DeepSeek session exceeds the %d MiB decoded limit", maxDeepSeekDecodedBytes/(1024*1024))
		}
	}
	if !completeJSONL(plaintext) {
		return Session{}, fmt.Errorf("%w: DeepSeek source ends with an incomplete JSONL record", errSourceBusy)
	}

	first, remaining, found := bytes.Cut(plaintext, []byte{'\n'})
	if !found {
		return Session{}, fmt.Errorf("DeepSeek session has no event log after its header")
	}
	first = bytes.TrimSpace(first)
	var header deepSeekHeader
	if err := json.Unmarshal(first, &header); err != nil {
		return Session{}, fmt.Errorf("parse DeepSeek session header: %w", err)
	}
	if header.Type != "session" || header.Version != deepSeekSessionFormat || strings.TrimSpace(header.ID) == "" {
		return Session{}, fmt.Errorf("unsupported DeepSeek session header type/version/id %q/%d/%q", header.Type, header.Version, header.ID)
	}
	if header.DelegationDepth == nil || *header.DelegationDepth < 0 {
		return Session{}, fmt.Errorf("DeepSeek session %q has invalid delegationDepth", header.ID)
	}
	if header.CreatedAt == nil {
		return Session{}, fmt.Errorf("DeepSeek session %q is missing createdAt", header.ID)
	}
	createdAt, err := deepSeekTime(*header.CreatedAt)
	if err != nil {
		return Session{}, fmt.Errorf("DeepSeek session %q has invalid createdAt: %w", header.ID, err)
	}
	result := Session{
		ID: header.ID, Harness: harnessDeepSeek, Originator: "DeepSeek Harness",
		SourceKind: "deepseek", CWD: header.CWD, CreatedAt: createdAt,
		RawHash: digestBytes(raw), RecordCount: 1,
	}
	if header.Origin == "subagent" || *header.DelegationDepth > 0 || header.ParentSession != "" {
		result.ExcludeReason = "subagent"
		result.ParentThreadID = header.ParentSession
	}

	nextSeq := 0
	for len(remaining) > 0 {
		line, rest, hasBreak := bytes.Cut(remaining, []byte{'\n'})
		if hasBreak {
			remaining = rest
		} else {
			remaining = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event deepSeekEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return Session{}, fmt.Errorf("parse DeepSeek session %q record near seq %d: %w", header.ID, nextSeq, err)
		}
		if packedDeepSeekRow(event.Type) {
			count, lastAt, err := validatePackedDeepSeekRow(event, nextSeq)
			if err != nil {
				return Session{}, fmt.Errorf("parse DeepSeek session %q packed record: %w", header.ID, err)
			}
			nextSeq += count
			result.RecordCount += count
			if lastAt.After(result.LastEventAt) {
				result.LastEventAt = lastAt
			}
			continue
		}
		if event.Type == "" || event.Seq == nil || event.Time == nil || event.Data == nil {
			return Session{}, fmt.Errorf("DeepSeek session %q has an incomplete event near seq %d", header.ID, nextSeq)
		}
		if *event.Seq != nextSeq {
			return Session{}, fmt.Errorf("DeepSeek session %q event sequence discontinuity: want %d, got %d", header.ID, nextSeq, *event.Seq)
		}
		eventAt, err := deepSeekTime(*event.Time)
		if err != nil {
			return Session{}, fmt.Errorf("DeepSeek session %q event %d has invalid time: %w", header.ID, nextSeq, err)
		}
		nextSeq++
		result.RecordCount++
		if eventAt.After(result.LastEventAt) {
			result.LastEventAt = eventAt
		}
		switch event.Type {
		case "user/message":
			var message deepSeekMessage
			if err := json.Unmarshal(event.Data, &message); err != nil {
				return Session{}, fmt.Errorf("parse DeepSeek user message %d: %w", *event.Seq, err)
			}
			appendSurface, err := deepSeekAppendSurface(event.SurfaceOp)
			if err != nil {
				return Session{}, fmt.Errorf("parse DeepSeek user message %d: %w", *event.Seq, err)
			}
			if message.Role != "user" {
				return Session{}, fmt.Errorf("DeepSeek user message %d has role %q", *event.Seq, message.Role)
			}
			if message.Source.Kind != "user" || !appendSurface {
				result.FilteredUserInput++
				continue
			}
			text, attachments, err := deepSeekMessageParts(message.Content, home, true)
			if err != nil {
				return Session{}, fmt.Errorf("parse DeepSeek user message %d: %w", *event.Seq, err)
			}
			if strings.TrimSpace(text) != "" || len(attachments) > 0 {
				result.Messages = append(result.Messages, Message{Role: "user", Text: text, Timestamp: eventAt, Attachments: attachments})
			}
		case "assistant/message":
			var data struct {
				Message deepSeekMessage `json:"message"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return Session{}, fmt.Errorf("parse DeepSeek assistant message %d: %w", *event.Seq, err)
			}
			appendSurface, err := deepSeekAppendSurface(event.SurfaceOp)
			if err != nil {
				return Session{}, fmt.Errorf("parse DeepSeek assistant message %d: %w", *event.Seq, err)
			}
			if data.Message.Role != "assistant" || data.Message.Source.Kind != "model" {
				return Session{}, fmt.Errorf("DeepSeek assistant message %d has invalid role/source", *event.Seq)
			}
			if !appendSurface {
				continue
			}
			text, _, err := deepSeekMessageParts(data.Message.Content, home, false)
			if err != nil {
				return Session{}, fmt.Errorf("parse DeepSeek assistant message %d: %w", *event.Seq, err)
			}
			if strings.TrimSpace(text) != "" {
				result.Messages = append(result.Messages, Message{Role: "assistant", Text: text, Timestamp: eventAt})
			}
		case "tool/call":
			result.ToolCallCount++
		case "session/title":
			var data struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil || strings.TrimSpace(data.Title) == "" {
				return Session{}, fmt.Errorf("DeepSeek title event %d is invalid", *event.Seq)
			}
			result.Title = cleanTitle(data.Title)
			result.TitleUpdatedAt = eventAt
		default:
			if len(event.SurfaceOp) > 0 {
				switch event.Type {
				case "tool/result":
					if _, err := deepSeekAppendSurface(event.SurfaceOp); err != nil {
						return Session{}, fmt.Errorf("parse DeepSeek tool result %d: %w", *event.Seq, err)
					}
				default:
					return Session{}, fmt.Errorf("DeepSeek session %q contains unsupported surface event %q", header.ID, event.Type)
				}
			}
		}
	}

	for _, message := range result.Messages {
		switch message.Role {
		case "user":
			result.UserMessages++
		case "assistant":
			result.AssistantMessages++
		}
		if result.FirstMessageAt.IsZero() || message.Timestamp.Before(result.FirstMessageAt) {
			result.FirstMessageAt = message.Timestamp
		}
		if message.Timestamp.After(result.LastMessageAt) {
			result.LastMessageAt = message.Timestamp
		}
	}
	result.OmittedCount = result.RecordCount - len(result.Messages)
	if result.ExcludeReason == "" && result.UserMessages == 0 && result.FilteredUserInput > 0 {
		result.ExcludeReason = "runtime_context"
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
		result.Title = "DeepSeek session " + result.ID
	}
	return result, nil
}

func deepSeekAppendSurface(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 {
		return false, fmt.Errorf("message is missing surfaceOp")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		if value != "append" {
			return false, fmt.Errorf("unknown surface operation %q", value)
		}
		return true, nil
	}
	var replacement struct {
		Op    string `json:"op"`
		Start *int   `json:"start"`
		End   *int   `json:"end"`
	}
	if err := json.Unmarshal(raw, &replacement); err != nil || replacement.Op != "replace" || replacement.Start == nil || replacement.End == nil || *replacement.Start < 0 || *replacement.End < *replacement.Start {
		return false, fmt.Errorf("invalid surface replacement")
	}
	return false, nil
}

func deepSeekMessageParts(blocks []deepSeekContentBlock, home string, includeAttachments bool) (string, []Attachment, error) {
	texts := make([]string, 0)
	attachments := make([]Attachment, 0)
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				texts = append(texts, block.Text)
			}
		case "reasoning", "tool-call", "tool-result":
			// Model reasoning and tool payloads are intentionally not rendered.
		case "image":
			if !includeAttachments {
				continue
			}
			attachment, err := deepSeekAttachment(block.Attachment, home)
			if err != nil {
				return "", nil, err
			}
			attachments = append(attachments, attachment)
		default:
			return "", nil, fmt.Errorf("unsupported content block %q", block.Type)
		}
	}
	return strings.Join(texts, "\n\n"), attachments, nil
}

func deepSeekAttachment(ref *deepSeekAttachmentRef, home string) (Attachment, error) {
	if ref == nil || !validSHA256(ref.AttachmentID) || ref.Bytes <= 0 || strings.TrimSpace(ref.MediaType) == "" {
		return Attachment{}, fmt.Errorf("invalid DeepSeek image attachment reference")
	}
	digestHex := strings.TrimPrefix(strings.ToLower(ref.AttachmentID), "sha256:")
	if _, err := hex.DecodeString(digestHex); err != nil {
		return Attachment{}, fmt.Errorf("invalid DeepSeek image attachment digest")
	}
	path := filepath.Join(home, "attachments", "v1", "objects", digestHex[:2], digestHex)
	return Attachment{
		Name: ref.Name, MIMEType: ref.MediaType, SourceKind: "local_path",
		SourceValue: path, LocalPath: path, ExpectedHash: "sha256:" + digestHex, ExpectedSize: ref.Bytes,
	}, nil
}

func packedDeepSeekRow(value string) bool {
	return value == "text-chunks" || value == "reasoning-chunks" || value == "tool-call-chunks"
}

func validatePackedDeepSeekRow(event deepSeekEvent, wantSeq int) (int, time.Time, error) {
	if event.Seq0 == nil || event.Time0 == nil || *event.Seq0 != wantSeq {
		return 0, time.Time{}, fmt.Errorf("packed row sequence discontinuity: want %d", wantSeq)
	}
	var data struct {
		DT    []int64  `json:"dt"`
		Texts []string `json:"texts"`
		Args  []string `json:"args"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return 0, time.Time{}, err
	}
	count := len(data.Texts)
	if event.Type == "tool-call-chunks" {
		count = len(data.Args)
	}
	if count < 3 || len(data.DT) != count-1 {
		return 0, time.Time{}, fmt.Errorf("packed row has invalid member/timestamp counts")
	}
	current := *event.Time0
	last, err := deepSeekTime(current)
	if err != nil {
		return 0, time.Time{}, err
	}
	for _, delta := range data.DT {
		if (delta > 0 && current > math.MaxInt64-delta) || (delta < 0 && current < math.MinInt64-delta) {
			return 0, time.Time{}, fmt.Errorf("packed row timestamp overflows")
		}
		current += delta
		last, err = deepSeekTime(current)
		if err != nil {
			return 0, time.Time{}, err
		}
	}
	return count, last, nil
}

func deepSeekTime(milliseconds int64) (time.Time, error) {
	if milliseconds < 0 || milliseconds > 253402300799999 {
		return time.Time{}, fmt.Errorf("milliseconds %d are outside the supported range", milliseconds)
	}
	return time.UnixMilli(milliseconds).UTC(), nil
}

func validateDeepSeekZstdFrames(data []byte) error {
	for offset := 0; offset < len(data); {
		start := offset
		if len(data)-offset < 4 {
			return fmt.Errorf("%w: incomplete DeepSeek Zstandard frame at byte %d", errSourceBusy, start)
		}
		if binary.LittleEndian.Uint32(data[offset:offset+4]) != deepSeekZstdMagic {
			return fmt.Errorf("corrupt DeepSeek Zstandard session: invalid frame magic at byte %d", offset)
		}
		offset += 4
		if offset == len(data) {
			return fmt.Errorf("%w: incomplete DeepSeek Zstandard frame at byte %d", errSourceBusy, start)
		}
		descriptor := data[offset]
		offset++
		if descriptor&0x18 != 0 {
			return fmt.Errorf("corrupt DeepSeek Zstandard session: reserved frame-header bit at byte %d", offset-1)
		}
		contentSizeFlag := descriptor >> 6
		singleSegment := descriptor&0x20 != 0
		checksum := descriptor&0x04 != 0
		if !checksum {
			return fmt.Errorf("corrupt DeepSeek Zstandard session: frame at byte %d has no checksum", start)
		}
		dictionaryFlag := int(descriptor & 0x03)
		dictionaryBytes := dictionaryFlag
		if dictionaryFlag == 3 {
			dictionaryBytes = 4
		}
		contentSizeBytes := 0
		if contentSizeFlag == 0 {
			if singleSegment {
				contentSizeBytes = 1
			}
		} else {
			contentSizeBytes = 1 << contentSizeFlag
		}
		remainingHeader := dictionaryBytes + contentSizeBytes
		if !singleSegment {
			remainingHeader++
		}
		if len(data)-offset < remainingHeader {
			return fmt.Errorf("%w: incomplete DeepSeek Zstandard frame header at byte %d", errSourceBusy, start)
		}
		offset += remainingHeader
		for {
			if len(data)-offset < 3 {
				return fmt.Errorf("%w: incomplete DeepSeek Zstandard block header at byte %d", errSourceBusy, start)
			}
			blockHeader := int(data[offset]) | int(data[offset+1])<<8 | int(data[offset+2])<<16
			offset += 3
			lastBlock := blockHeader&1 != 0
			blockType := (blockHeader >> 1) & 3
			blockSize := blockHeader >> 3
			if blockType == 3 {
				return fmt.Errorf("corrupt DeepSeek Zstandard session: reserved block type at byte %d", offset-3)
			}
			payloadBytes := blockSize
			if blockType == 1 {
				payloadBytes = 1
			}
			if len(data)-offset < payloadBytes {
				return fmt.Errorf("%w: incomplete DeepSeek Zstandard block at byte %d", errSourceBusy, start)
			}
			offset += payloadBytes
			if lastBlock {
				break
			}
		}
		if checksum {
			if len(data)-offset < 4 {
				return fmt.Errorf("%w: incomplete DeepSeek Zstandard checksum at byte %d", errSourceBusy, start)
			}
			offset += 4
		}
	}
	if len(data) == 0 {
		return fmt.Errorf("DeepSeek Zstandard session is empty")
	}
	return nil
}
