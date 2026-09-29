package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// completedNativePrefix keeps the last complete turn when an append-only source
// already contains the start of another turn. Native files remain untouched.
// Formats without a turn marker retain their existing parser behavior.
func completedNativePrefix(harness string, raw []byte) ([]byte, error) {
	if harness == harnessDeepSeek && len(raw) >= 4 && bytes.Equal(raw[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		return raw, nil // The DeepSeek decoder handles complete Zstandard frames.
	}
	complete, partial, err := completeRecordPrefix(raw)
	if err != nil {
		return nil, err
	}
	if len(complete) == 0 {
		return nil, fmt.Errorf("%w: no complete JSONL record", errSourceBusy)
	}
	var lastDone int
	activeStart := -1
	markerSeen := false
	for offset := 0; offset < len(complete); {
		start := offset
		line, _, found := bytes.Cut(complete[offset:], []byte{'\n'})
		end := len(complete)
		if found {
			end = offset + len(line) + 1
		}
		offset = end
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("parse session turn marker: %w", err)
		}
		switch harness {
		case harnessCodex:
			var payload struct {
				Type string `json:"type"`
				Role string `json:"role"`
			}
			_ = json.Unmarshal(record.Payload, &payload)
			if record.Type == "event_msg" {
				switch payload.Type {
				case "task_started":
					markerSeen = true
					if activeStart < 0 {
						activeStart = start
					}
				case "task_complete":
					markerSeen, lastDone, activeStart = true, end, -1
				case "user_message":
					if activeStart < 0 {
						activeStart = start
					}
				}
			} else if record.Type == "response_item" && payload.Type == "message" && payload.Role == "user" {
				if activeStart < 0 {
					activeStart = start
				}
			}
		case harnessDeepSeek:
			if record.Type == "turn/start" {
				markerSeen = true
				if activeStart < 0 {
					activeStart = start
				}
			} else if record.Type == "turn/end" {
				markerSeen, lastDone, activeStart = true, end, -1
			}
		}
	}
	if markerSeen && activeStart >= 0 {
		if lastDone == 0 {
			return nil, fmt.Errorf("%w: no completed turn", errSourceBusy)
		}
		return complete[:activeStart], nil
	}
	if partial {
		if !markerSeen || lastDone == 0 {
			return nil, fmt.Errorf("%w: source ends with an incomplete JSONL record", errSourceBusy)
		}
		return complete, nil
	}
	return complete, nil
}

// A writer can leave an unfinished last line. A newline-terminated malformed
// record remains a parser error, rather than being mistaken for a busy writer.
func completeRecordPrefix(raw []byte) ([]byte, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	lastBreak := bytes.LastIndexByte(trimmed, '\n')
	if json.Valid(trimmed[lastBreak+1:]) {
		return raw, false, nil
	}
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		return nil, false, fmt.Errorf("malformed newline-terminated JSONL record")
	}
	if lastBreak < 0 {
		return nil, true, nil
	}
	return trimmed[:lastBreak+1], true, nil
}
