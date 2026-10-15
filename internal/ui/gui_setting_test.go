package ui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sessionmgr/sessionmgr/internal/config"
)

func TestGUISwitchPersistsForCLIAndNextLaunch(t *testing.T) {
	store := config.Store{Path: filepath.Join(t.TempDir(), "config.json")}
	h, err := NewHandler("fixture-token", store, t.TempDir(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"gui_only":true}`, `{"gui_only":false}`} {
		req := httptest.NewRequest(http.MethodPut, "/api/gui-only", bytes.NewBufferString(body))
		req.Header.Set("X-Sessionmgr-Token", "fixture-token")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != http.StatusOK {
			t.Fatalf("toggle: %d %s", out.Code, out.Body.String())
		}
		value, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if value.GUIOnly != (body == `{"gui_only":true}`) {
			t.Fatal("switch was not persisted")
		}
	}
}
