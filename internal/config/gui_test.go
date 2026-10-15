package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGUIOnlyDefaultsOffAndSurvivesOtherSettings(t *testing.T) {
	root := t.TempDir()
	store := Store{Path: filepath.Join(root, "config.json")}
	os.WriteFile(store.Path, []byte(`{"schema_version":3,"export_directory":""}`), 0600)
	value, err := store.Load()
	if err != nil || value.GUIOnly || value.SchemaVersion != SchemaVersion {
		t.Fatalf("v3 default: %+v %v", value, err)
	}
	if _, err := store.SetGUIOnly(true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetExportDirectory(filepath.Join(root, "archive")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSourcePreferences(SourcePreferences{OpenCode: true}); err != nil {
		t.Fatal(err)
	}
	value, err = store.EnsureDevice()
	if err != nil || !value.GUIOnly || !value.SourcePreferences.OpenCode {
		t.Fatalf("setting lost: %+v %v", value, err)
	}
	if _, err := store.SetGUIOnly(false); err != nil {
		t.Fatal(err)
	}
	value, err = store.Load()
	if err != nil || value.GUIOnly {
		t.Fatalf("disable failed: %+v %v", value, err)
	}
}
