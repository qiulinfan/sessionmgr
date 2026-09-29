package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func encodeTurnRecords(t *testing.T, records []map[string]any) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	return output.Bytes()
}

func TestCodexRunningTurnKeepsPreviousTaskComplete(t *testing.T) {
	meta := map[string]any{"type": "session_meta", "timestamp": "2026-09-29T01:00:00Z", "payload": map[string]any{"id": "codex-running", "cwd": t.TempDir()}}
	start := map[string]any{"type": "event_msg", "timestamp": "2026-09-29T01:00:01Z", "payload": map[string]any{"type": "task_started"}}
	complete := map[string]any{"type": "event_msg", "timestamp": "2026-09-29T01:00:04Z", "payload": map[string]any{"type": "task_complete"}}
	settings := map[string]any{"type": "event_msg", "timestamp": "2026-09-29T01:00:05Z", "payload": map[string]any{"type": "thread_settings_applied"}}
	user := func(text string) map[string]any {
		return map[string]any{"type": "response_item", "timestamp": "2026-09-29T01:00:02Z", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}}
	}
	assistant := func(text string) map[string]any {
		return map[string]any{"type": "response_item", "timestamp": "2026-09-29T01:00:03Z", "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}}
	}
	previous := encodeTurnRecords(t, []map[string]any{meta, start, user("first question"), assistant("first answer"), complete, settings})
	for _, records := range [][]map[string]any{
		{meta, start, user("first question"), assistant("first answer"), complete, settings, user("current question")},
		{meta, start, user("first question"), assistant("first answer"), complete, settings, start, user("current question"), assistant("partial answer")},
	} {
		parsed, err := parseSession(encodeTurnRecords(t, records), "", nil)
		if err != nil || parsed.UserMessages != 1 || parsed.AssistantMessages != 1 ||
			len(parsed.Messages) != 2 || parsed.Messages[1].Text != "first answer" || parsed.RawHash != digestBytes(previous) {
			t.Fatalf("running Codex turn was included: %+v / %v", parsed, err)
		}
	}
	finished, err := parseSession(encodeTurnRecords(t, []map[string]any{
		meta, start, user("first question"), assistant("first answer"), complete, settings,
		start, user("current question"), assistant("second answer"), complete,
	}), "", nil)
	if err != nil || finished.UserMessages != 2 || finished.AssistantMessages != 2 {
		t.Fatalf("completed Codex turn did not appear: %+v / %v", finished, err)
	}
	if _, err := parseSession(encodeTurnRecords(t, []map[string]any{meta, start, user("first question")}), "", nil); !errors.Is(err, errSourceBusy) {
		t.Fatalf("Codex turn without any completion was not busy: %v", err)
	}
}
