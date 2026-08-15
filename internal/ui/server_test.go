package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"export through GUI","attachments":[{"filename":"gui-note.txt","mime_type":"text/plain","file_data":"Z3VpIG5vdGUK"}]}}
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
	if firstResponse.Result.Attachments != 1 || firstResponse.Result.ArchivedAttachments != 1 {
		t.Fatalf("GUI did not return attachment counts: %+v", firstResponse.Result)
	}
	if firstResponse.Result.Changes[0].Attachments != 1 || firstResponse.Result.Changes[0].ArchivedFiles != 1 {
		t.Fatalf("GUI changeset omitted attachment counts: %+v", firstResponse.Result.Changes[0])
	}
	changePath := firstResponse.Result.Changes[0].Path
	if filepath.Base(changePath) != "conversation.md" || strings.Contains(changePath, "sha256") || strings.Contains(changePath, "gui-session") {
		t.Fatalf("GUI exposed a non-semantic document path: %s", changePath)
	}
	if strings.Contains(changePath, string(filepath.Separator)+"repositories"+string(filepath.Separator)) || !strings.Contains(changePath, "github.com-example") {
		t.Fatalf("GUI did not use the flattened repository layout: %s", changePath)
	}
	if strings.Contains(changePath, string(filepath.Separator)+"sessions"+string(filepath.Separator)) {
		t.Fatalf("GUI path retained the sessions wrapper: %s", changePath)
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(changePath), "attachments", "001-gui-note.txt")); err != nil || string(data) != "gui note\n" {
		t.Fatalf("GUI attachment was not exported: %q, %v", data, err)
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

func TestGUIArchivedSessionsRequireExplicitInclusion(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	source := filepath.Join(codexHome, "archived_sessions", "fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"archived-gui","cwd":"/missing","git":{"repository_url":"https://github.com/example/gui.git"}}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"include only on request"}}
`
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
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

	defaultRequest := authenticatedRequest(http.MethodPost, "/api/export", map[string]interface{}{"scope": "all"})
	defaultResult := httptest.NewRecorder()
	handler.ServeHTTP(defaultResult, defaultRequest)
	defaultResponse := decodeExportResponse(t, defaultResult)
	if defaultResponse.Error != "" || defaultResponse.Result.Sources != 0 || defaultResponse.Result.Created != 0 {
		t.Fatalf("default GUI export included archived session: %+v", defaultResponse)
	}

	includedRequest := authenticatedRequest(http.MethodPost, "/api/export", map[string]interface{}{
		"scope": "all", "include_archived": true,
	})
	includedResult := httptest.NewRecorder()
	handler.ServeHTTP(includedResult, includedRequest)
	includedResponse := decodeExportResponse(t, includedResult)
	if includedResponse.Error != "" || includedResponse.Result.Sources != 1 || includedResponse.Result.Created != 1 ||
		len(includedResponse.Result.Changes) != 1 || includedResponse.Result.Changes[0].SessionID != "archived-gui" {
		t.Fatalf("explicit GUI archived export failed: %+v", includedResponse)
	}
}

func TestGUINonGitDirectoriesRequireExplicitFullExport(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	localDirectory := filepath.Join(root, "loose-work")
	if err := os.MkdirAll(localDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "non-git.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"non-git-gui","cwd":%q}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"export loose GUI work"}}
`, localDirectory)
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store := config.Store{Path: filepath.Join(root, "config.json")}
	if _, err := store.SetExportDirectory(filepath.Join(root, "exports")); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("test-token", store, codexHome, localDirectory)
	if err != nil {
		t.Fatal(err)
	}

	defaultRequest := authenticatedRequest(http.MethodPost, "/api/export", map[string]interface{}{"scope": "all"})
	defaultResult := httptest.NewRecorder()
	handler.ServeHTTP(defaultResult, defaultRequest)
	defaultResponse := decodeExportResponse(t, defaultResult)
	if defaultResponse.Error != "" || defaultResponse.Result.FilteredNonGit != 1 || defaultResponse.Result.Created != 0 {
		t.Fatalf("default GUI export included non-Git data: %+v", defaultResponse)
	}

	includedRequest := authenticatedRequest(http.MethodPost, "/api/export", map[string]interface{}{
		"scope": "current", "include_non_git": true,
	})
	includedResult := httptest.NewRecorder()
	handler.ServeHTTP(includedResult, includedRequest)
	includedResponse := decodeExportResponse(t, includedResult)
	if includedResponse.Error != "" || includedResponse.Result.Created != 1 || len(includedResponse.Result.Changes) != 1 ||
		includedResponse.Result.Changes[0].Kind != "new" {
		t.Fatalf("explicit GUI non-Git export failed: %+v", includedResponse)
	}

	repeatRequest := authenticatedRequest(http.MethodPost, "/api/export", map[string]interface{}{
		"scope": "current", "include_non_git": true,
	})
	repeatResult := httptest.NewRecorder()
	handler.ServeHTTP(repeatResult, repeatRequest)
	repeatResponse := decodeExportResponse(t, repeatResult)
	if repeatResponse.Error != "" || repeatResponse.Result.FullExported != 1 || len(repeatResponse.Result.Changes) != 1 ||
		repeatResponse.Result.Changes[0].Kind != "full" {
		t.Fatalf("repeat GUI non-Git export was not full: %+v", repeatResponse)
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
	page := result.Body.Bytes()
	if !bytes.Contains(page, []byte(`<html lang="en">`)) || !bytes.Contains(page, []byte("Changes from this export")) {
		t.Fatal("GUI does not default to English")
	}
	if !bytes.Contains(page, []byte(`<option value="en">English</option>`)) || !bytes.Contains(page, []byte(`<option value="zh">中文</option>`)) {
		t.Fatal("GUI language selector is missing English or Chinese")
	}
	if !bytes.Contains(page, []byte(`id="include-archived"`)) || !bytes.Contains(page, []byte("Include archived Codex sessions")) {
		t.Fatal("GUI archived-session option is missing")
	}
	if !bytes.Contains(page, []byte(`id="include-deepseek"`)) || !bytes.Contains(page, []byte("Include DeepSeek Harness sessions")) {
		t.Fatal("GUI DeepSeek Harness option is missing")
	}
	if !bytes.Contains(page, []byte(`id="include-non-git"`)) || !bytes.Contains(page, []byte("Include non-Git directories")) {
		t.Fatal("GUI non-Git full-export option is missing")
	}
	if !bytes.Contains(page, []byte(`id="setup-git-status"`)) ||
		!bytes.Contains(page, []byte("winget install --id Git.Git -e --source winget")) ||
		!bytes.Contains(page, []byte("Portable EXE · no Go or Make needed")) {
		t.Fatal("GUI environment setup guidance is missing")
	}

	scriptRequest := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	scriptResult := httptest.NewRecorder()
	handler.ServeHTTP(scriptResult, scriptRequest)
	if scriptResult.Code != http.StatusOK {
		t.Fatalf("GUI script returned %d", scriptResult.Code)
	}
	script := scriptResult.Body.Bytes()
	if !bytes.Contains(script, []byte("function groupChanges")) || !bytes.Contains(script, []byte("repository-tree")) ||
		!bytes.Contains(script, []byte("repository.localDirectory")) || !bytes.Contains(script, []byte("repository-session-list")) ||
		!bytes.Contains(script, []byte("sessionmgr-language")) || !bytes.Contains(script, []byte("filtered_internal")) ||
		!bytes.Contains(script, []byte("include_archived")) || !bytes.Contains(script, []byte("include_deepseek")) ||
		!bytes.Contains(script, []byte("include_non_git")) ||
		!bytes.Contains(script, []byte("filtered_non_git")) || !bytes.Contains(script, []byte("badgeFull")) ||
		!bytes.Contains(script, []byte("renderEnvironment")) || !bytes.Contains(script, []byte("git_available")) {
		t.Fatal("GUI script is missing grouped directory changes or persistent language selection")
	}

	styleRequest := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	styleResult := httptest.NewRecorder()
	handler.ServeHTTP(styleResult, styleRequest)
	if styleResult.Code != http.StatusOK {
		t.Fatalf("GUI stylesheet returned %d", styleResult.Code)
	}
	style := styleResult.Body.Bytes()
	if !bytes.Contains(style, []byte("color-scheme: dark")) ||
		!bytes.Contains(style, []byte("--paper: #0d1117")) ||
		!bytes.Contains(style, []byte("--surface: #161b22")) ||
		!bytes.Contains(style, []byte(".setup-command")) ||
		bytes.Contains(style, []byte("color-scheme: light")) {
		t.Fatal("GUI stylesheet is not using the GitHub Dark palette")
	}
}

func TestGUIStateReportsRuntimeAndSessionSources(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	deepSeekHome := filepath.Join(root, "dsh")
	if err := os.MkdirAll(filepath.Join(codexHome, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithSources("test-token", config.Store{Path: filepath.Join(root, "config.json")}, codexHome, deepSeekHome, ".")
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(http.MethodGet, "/api/state", nil)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("state returned %d: %s", result.Code, result.Body.String())
	}
	var state struct {
		Environment environmentState `json:"environment"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	_, gitErr := exec.LookPath("git")
	if state.Environment.Platform != runtime.GOOS || state.Environment.Git != (gitErr == nil) {
		t.Fatalf("unexpected runtime state: %+v", state.Environment)
	}
	if !state.Environment.Codex.Available || state.Environment.Codex.Path != codexHome {
		t.Fatalf("Codex source was not detected: %+v", state.Environment.Codex)
	}
	if state.Environment.DeepSeek.Available || state.Environment.DeepSeek.Path != deepSeekHome {
		t.Fatalf("missing DeepSeek source was not reported: %+v", state.Environment.DeepSeek)
	}
}

func TestInspectEnvironmentReportsMissingGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	state := inspectEnvironment(filepath.Join(t.TempDir(), "codex"), filepath.Join(t.TempDir(), "dsh"))
	if state.Git {
		t.Fatal("Git was reported available with an empty tool path")
	}
}

func TestGUIDeepSeekOptInExportsConfiguredSource(t *testing.T) {
	root := t.TempDir()
	deepSeekHome := filepath.Join(root, "dsh")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(deepSeekHome, "sessions", "--workspace--", "session-gui-deepseek", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{"type":"session","version":0,"id":"session-gui-deepseek","createdAt":1786766400000,"cwd":%q,"delegationDepth":0,"agentPreset":"standard"}
{"type":"user/message","seq":0,"time":1786766401000,"data":{"id":"user","role":"user","content":[{"type":"text","text":"GUI export"}],"source":{"kind":"user"}},"surfaceOp":"append"}
`, workspace)
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store := config.Store{Path: filepath.Join(root, "config.json")}
	if _, err := store.SetExportDirectory(filepath.Join(root, "exports")); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithSources("test-token", store, filepath.Join(root, "codex"), deepSeekHome, workspace)
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(http.MethodPost, "/api/export", map[string]any{
		"scope": "all", "include_deepseek": true, "include_non_git": true,
	})
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	response := decodeExportResponse(t, result)
	if response.Error != "" || response.Result.Created != 1 || len(response.Result.Changes) != 1 || response.Result.Changes[0].Harness != "deepseek" {
		t.Fatalf("GUI did not export the DeepSeek session: %+v", response)
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

func TestGUIReportsFilteredInternalSessionsWithoutExportingThem(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "guardian.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"guardian","originator":"Codex Desktop","source":{"subagent":{"other":"guardian"}},"thread_source":"subagent","parent_thread_id":"parent","cwd":"/missing","git":{"repository_url":"https://github.com/example/gui.git"}}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"The following is internal agent history"}}
`
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
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
	if response.Error != "" || response.Result.FilteredInternal != 1 || response.Result.Created != 0 || len(response.Result.Changes) != 0 {
		t.Fatalf("GUI exported an internal session: %+v", response)
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
