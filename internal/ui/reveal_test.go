package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sessionmgr/sessionmgr/internal/config"
)

func TestGUIRevealExportedSessionAndLocalScanHistory(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	source := filepath.Join(codexHome, "sessions", "fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"reveal-session","cwd":"/missing","git":{"repository_url":"https://github.com/example/reveal.git"}}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"show exported session"}}
{"timestamp":"2026-08-05T01:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"ready"}}
`
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(source, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	store := config.Store{Path: filepath.Join(root, "config.json")}
	if _, err := store.SetExportDirectory(filepath.Join(root, "exports")); err != nil {
		t.Fatal(err)
	}
	var revealed []string
	handler, err := newHandlerWithAllSources("test-token", store, codexHome, "", "", "", "", "", ".", func(path string) error {
		revealed = append(revealed, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, endpoint string, body any) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authenticatedRequest(method, endpoint, body))
		return response
	}
	payload := map[string]any{"scope": "all", "sources": sourceRequest(true, false, false)}
	first := decodeExportResponse(t, request(http.MethodPost, "/api/export", payload))
	if first.Error != "" || len(first.Result.Changes) != 1 || first.Result.LastExportAt == "" {
		t.Fatalf("initial GUI export: %+v", first)
	}
	path := first.Result.Changes[0].Path
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	reveal := request(http.MethodPost, "/api/reveal", map[string]string{"path": path})
	if reveal.Code != http.StatusOK || len(revealed) != 1 || revealed[0] != resolvedPath {
		t.Fatalf("reveal: %d %s, %v", reveal.Code, reveal.Body.String(), revealed)
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/reveal", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized reveal: %d", unauthorized.Code)
	}
	invalid := request(http.MethodPost, "/api/reveal", map[string]string{"path": filepath.Join(root, "outside", "conversation.md")})
	if invalid.Code != http.StatusBadRequest || len(revealed) != 1 {
		t.Fatal("arbitrary local path was passed to the file manager")
	}
	second := decodeExportResponse(t, request(http.MethodPost, "/api/export", payload))
	if second.Error != "" || second.Result.ScannedSources != 0 || second.Result.IgnoredUnchanged != 1 || !second.Result.Incremental {
		t.Fatalf("incremental GUI export: %+v", second)
	}
	stateResponse := request(http.MethodGet, "/api/state", nil)
	var state struct {
		LastExportAt string `json:"last_export_at"`
	}
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &state); err != nil || state.LastExportAt != second.Result.LastExportAt {
		t.Fatalf("GUI export time: %+v, %v", state, err)
	}
	payload["full_scan"] = true
	full := decodeExportResponse(t, request(http.MethodPost, "/api/export", payload))
	if full.Error != "" || full.Result.Incremental || full.Result.ScannedSources != 1 {
		t.Fatalf("GUI full scan: %+v", full)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing := request(http.MethodPost, "/api/reveal", map[string]string{"path": path})
	if missing.Code != http.StatusBadRequest || len(revealed) != 1 {
		t.Fatal("deleted exported file was passed to the file manager")
	}
}

func TestRevealPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	archiveRoot := filepath.Join(root, "archive")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{archiveRoot, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(outside, "conversation.md")
	if err := os.WriteFile(file, []byte("not an exported session"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(archiveRoot, "session")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := validateRevealPath(archiveRoot, filepath.Join(link, "conversation.md")); err == nil {
		t.Fatal("accepted escaped parent symlink")
	}
	linkFile := filepath.Join(archiveRoot, "conversation.md")
	if err := os.Symlink(file, linkFile); err != nil {
		t.Fatal(err)
	}
	if _, err := validateRevealPath(archiveRoot, linkFile); err == nil {
		t.Fatal("accepted symlinked conversation")
	}
}

func TestRevealCommandsKeepFilePathsAsArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session with spaces", "conversation.md")
	for _, test := range []struct {
		platform string
		args     []string
	}{
		{"darwin", []string{"open", "-R", path}},
		{"windows", []string{"explorer.exe", "/select," + path}},
		{"linux", []string{"xdg-open", filepath.Dir(path)}},
	} {
		command := revealCommand(test.platform, path)
		if !reflect.DeepEqual(command.Args, test.args) {
			t.Fatalf("%s arguments: %v", test.platform, command.Args)
		}
	}
}
