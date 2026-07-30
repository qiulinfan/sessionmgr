package syncer

import (
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/sessionmgr/sessionmgr/internal/config"
	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/home"
	"github.com/sessionmgr/sessionmgr/internal/store"
)

func TestParseSSHTarget(t *testing.T) {
	target, err := parseSSHTarget("ssh://dev@example.com:2222/~/sessionmgr-store")
	if err != nil {
		t.Fatal(err)
	}
	if target.destination != "dev@example.com" || target.port != "2222" || target.basePath != "~/sessionmgr-store" {
		t.Fatalf("unexpected target: %#v", target)
	}
	args := target.sshArgs("true")
	if len(args) != 4 || args[0] != "-p" || args[1] != "2222" || args[2] != "dev@example.com" {
		t.Fatalf("unexpected ssh args: %#v", args)
	}
}

func TestParseSSHTargetRejectsUnsafeValues(t *testing.T) {
	values := []string{
		"ssh://example.com/~/../secret",
		"ssh://bad host/~/store",
		"file:///tmp/store",
	}
	for _, value := range values {
		if _, err := parseSSHTarget(value); err == nil {
			t.Errorf("unsafe SSH target accepted: %s", value)
		}
	}
}

func TestCapsuleDigestFromFilename(t *testing.T) {
	runID := "019fb197-fa7d-7aa1-ae70-43e8e9434c0d"
	hexDigest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	filename := runID + "." + hexDigest + ".smcap.age"
	if got := capsuleDigestFromFilename(runID, filename); got != "sha256:"+hexDigest {
		t.Fatalf("got %q", got)
	}
	if got := capsuleDigestFromFilename(runID, runID+"."+hexDigest[:63]+"z.smcap.age"); got != "" {
		t.Fatalf("accepted invalid digest %q", got)
	}
}

func TestCachedEncryptedCapsuleIsReused(t *testing.T) {
	layout, err := home.ForRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := home.Ensure(layout); err != nil {
		t.Fatal(err)
	}
	objectStore := store.New(layout)
	desc, err := objectStore.PutBytes([]byte("payload"), "text/plain", true)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		SchemaVersion: 1,
		ID:            "019fb197-fa7d-7aa1-ae70-43e8e9434c0d",
		Title:         "retry",
		CreatedAt:     time.Unix(1, 0).UTC(),
		Relation:      "capture",
		Objects:       []domain.ObjectDescriptor{desc},
	}
	if _, err := objectStore.PublishRun(run); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.StoreConfig{AgeRecipients: []string{identity.Recipient().String()}}
	firstPath, firstDigest, err := cachedEncryptedCapsule(
		layout, objectStore, run, cfg, []age.Recipient{identity.Recipient()})
	if err != nil {
		t.Fatal(err)
	}
	secondPath, secondDigest, err := cachedEncryptedCapsule(
		layout, objectStore, run, cfg, []age.Recipient{identity.Recipient()})
	if err != nil {
		t.Fatal(err)
	}
	if firstPath != secondPath || firstDigest != secondDigest {
		t.Fatalf("cache was not reused: %s/%s != %s/%s",
			filepath.Base(firstPath), firstDigest, filepath.Base(secondPath), secondDigest)
	}
}
