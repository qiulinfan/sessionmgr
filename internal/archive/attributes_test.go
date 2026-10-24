package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestByteExactAttributesAreWrittenOnceAndOwnerFilesKept(t *testing.T) {
	fresh := t.TempDir()
	for range 2 {
		if err := ensureByteExactAttributes(fresh); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(fresh, ".gitattributes")); err != nil || string(data) != byteExactAttributes {
		t.Fatalf("new archive attributes = %q, %v", data, err)
	}

	owned := t.TempDir()
	path := filepath.Join(owned, ".gitattributes")
	if err := os.WriteFile(path, []byte("* text=auto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureByteExactAttributes(owned); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "* text=auto\n" {
		t.Fatalf("owner attributes changed: %q, %v", data, err)
	}
}
