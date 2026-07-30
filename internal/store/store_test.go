package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/home"
)

func TestPublishAndVerifyRun(t *testing.T) {
	t.Setenv("SESSIONMGR_HOME", t.TempDir())
	layout, err := home.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := home.Ensure(layout); err != nil {
		t.Fatal(err)
	}
	objectStore := New(layout)
	desc, err := objectStore.PutBytes([]byte("payload"), "text/plain", true)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		SchemaVersion: 1,
		ID:            "019fb197-fa7d-7aa1-ae70-43e8e9434c0d",
		Title:         "test",
		CreatedAt:     time.Unix(1, 0).UTC(),
		Relation:      "capture",
		Objects:       []domain.ObjectDescriptor{desc},
	}
	if _, err := objectStore.PublishRun(run); err != nil {
		t.Fatal(err)
	}
	loaded, err := objectStore.LoadRun(run.ID[:12])
	if err != nil {
		t.Fatal(err)
	}
	if err := objectStore.Verify(loaded, true); err != nil {
		t.Fatal(err)
	}
	path, err := objectStore.ObjectPath(desc.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := objectStore.Verify(loaded, true); err == nil {
		t.Fatal("corrupt object passed verification")
	}
	if _, err := os.Stat(filepath.Join(layout.Refs, "runs", run.ID)); err != nil {
		t.Fatal(err)
	}
}
