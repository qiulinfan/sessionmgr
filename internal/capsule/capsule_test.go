package capsule

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/home"
	"github.com/sessionmgr/sessionmgr/internal/store"
)

func TestEncryptedRoundTrip(t *testing.T) {
	sourceLayout := testLayout(t, filepath.Join(t.TempDir(), "source"))
	targetLayout := testLayout(t, filepath.Join(t.TempDir(), "target"))
	sourceStore := store.New(sourceLayout)
	targetStore := store.New(targetLayout)
	payload := []byte("private workspace content")
	desc, err := sourceStore.PutBytes(payload, "text/plain", true)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		SchemaVersion: 1,
		ID:            "019fb197-fa7d-7aa1-ae70-43e8e9434c0d",
		Title:         "encrypted",
		CreatedAt:     time.Unix(1, 0).UTC(),
		Relation:      "capture",
		Objects:       []domain.ObjectDescriptor{desc},
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(sourceLayout.Tmp, "test.smcap.age")
	if _, err := ExportEncrypted(sourceStore, run, []age.Recipient{identity.Recipient()}, output); err != nil {
		t.Fatal(err)
	}
	encrypted, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(encrypted) == string(payload) {
		t.Fatal("Capsule was not encrypted")
	}
	imported, _, err := ImportEncrypted(targetStore, output, identity)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ID != run.ID {
		t.Fatalf("imported Run %s, want %s", imported.ID, run.ID)
	}
	got, err := targetStore.Get(desc.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("imported payload %q", got)
	}
}

func testLayout(t *testing.T, root string) home.Layout {
	t.Helper()
	layout, err := home.ForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := home.Ensure(layout); err != nil {
		t.Fatal(err)
	}
	return layout
}
