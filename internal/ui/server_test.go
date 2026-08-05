package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sessionmgr/sessionmgr/internal/config"
)

func TestGUIConfigAndIncrementalExport(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"gui-session","cwd":"/missing","git":{"repository_url":"https://github.com/example/gui.git"}}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"export through GUI"}}
{"timestamp":"2026-08-05T01:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"done"}}
`
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store := config.Store{Path: filepath.Join(root, "config.json")}
	handler, err := NewHandler("test-token", store, codexHome, ".")
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	unauthorizedResult := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedResult, unauthorized)
	if unauthorizedResult.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized API returned %d", unauthorizedResult.Code)
	}

	directory := filepath.Join(root, "exports")
	put := authenticatedRequest(http.MethodPut, "/api/config", map[string]string{"directory": directory})
	putResult := httptest.NewRecorder()
	handler.ServeHTTP(putResult, put)
	if putResult.Code != http.StatusOK {
		t.Fatalf("config returned %d: %s", putResult.Code, putResult.Body.String())
	}
	loaded, err := store.Load()
	if err != nil || loaded.ExportDirectory != directory {
		t.Fatalf("GUI config was not persisted: %+v, %v", loaded, err)
	}

	first := authenticatedRequest(http.MethodPost, "/api/export", map[string]string{"scope": "all"})
	firstResult := httptest.NewRecorder()
	handler.ServeHTTP(firstResult, first)
	firstResponse := decodeExportResponse(t, firstResult)
	if len(firstResponse.Result.Changes) != 1 || firstResponse.Result.Changes[0].Kind != "new" {
		t.Fatalf("first GUI export changes: %+v", firstResponse.Result.Changes)
	}
	changePath := firstResponse.Result.Changes[0].Path
	if filepath.Base(changePath) != "conversation.md" || strings.Contains(changePath, "sha256") || strings.Contains(changePath, "gui-session") {
		t.Fatalf("GUI exposed a non-semantic document path: %s", changePath)
	}
	withDevice, err := store.Load()
	if err != nil || withDevice.DeviceID == "" || withDevice.DeviceName == "" {
		t.Fatalf("GUI did not persist device identity: %+v, %v", withDevice, err)
	}

	second := authenticatedRequest(http.MethodPost, "/api/export", map[string]string{"scope": "all"})
	secondResult := httptest.NewRecorder()
	handler.ServeHTTP(secondResult, second)
	secondResponse := decodeExportResponse(t, secondResult)
	if len(secondResponse.Result.Changes) != 0 {
		t.Fatalf("repeat GUI export exposed old changes: %+v", secondResponse.Result.Changes)
	}
}

func TestGUIStaticPageHasSecurityHeaders(t *testing.T) {
	handler, err := NewHandler("token", config.Store{Path: filepath.Join(t.TempDir(), "config.json")}, "", ".")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("static page returned %d", result.Code)
	}
	if result.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("static page has no content security policy")
	}
	if !bytes.Contains(result.Body.Bytes(), []byte("本次导出变化")) {
		t.Fatal("GUI content is missing")
	}
}

func TestGUIBusySourceIsNotAnError(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "active.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"timestamp":"2026-08-05T01:00:00Z"`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := config.Store{Path: filepath.Join(root, "config.json")}
	if _, err := store.SetExportDirectory(filepath.Join(root, "exports")); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("test-token", store, codexHome, ".")
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(http.MethodPost, "/api/export", map[string]string{"scope": "all"})
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	response := decodeExportResponse(t, result)
	if response.Error != "" || response.Result.Busy != 1 || response.Result.Skipped != 0 {
		t.Fatalf("busy GUI export was not successful: %+v", response)
	}
}

func TestGUIRejectsNonLoopbackListener(t *testing.T) {
	if err := validateListenAddress("0.0.0.0:8080"); err == nil {
		t.Fatal("non-loopback GUI listener was accepted")
	}
	if err := validateListenAddress("127.0.0.1:0"); err != nil {
		t.Fatalf("loopback listener was rejected: %v", err)
	}
}

func TestGUIRejectsTrailingJSON(t *testing.T) {
	handler, err := NewHandler("test-token", config.Store{Path: filepath.Join(t.TempDir(), "config.json")}, "", ".")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{"directory":"/tmp"} {}`))
	request.Header.Set("X-Sessionmgr-Token", "test-token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON returned %d: %s", result.Code, result.Body.String())
	}
}

func authenticatedRequest(method, target string, body interface{}) *http.Request {
	data, _ := json.Marshal(body)
	request := httptest.NewRequest(method, target, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Sessionmgr-Token", "test-token")
	return request
}

func decodeExportResponse(t *testing.T, result *httptest.ResponseRecorder) exportResponse {
	t.Helper()
	if result.Code != http.StatusOK {
		t.Fatalf("export returned %d: %s", result.Code, result.Body.String())
	}
	var response exportResponse
	if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}
