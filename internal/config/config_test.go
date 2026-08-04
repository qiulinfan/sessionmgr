package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorePersistsExportDirectory(t *testing.T) {
	root := t.TempDir()
	store := Store{Path: filepath.Join(root, "config", "config.json")}
	directory := filepath.Join(root, "exports")

	written, err := store.SetExportDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != written || loaded.ExportDirectory != directory {
		t.Fatalf("loaded config differs: %+v vs %+v", loaded, written)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		t.Fatalf("export directory was not created: %v", err)
	}
}

func TestResolveDirectoryRequiresConfiguration(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "missing.json")}
	if _, err := store.ResolveDirectory("", false); err == nil {
		t.Fatal("missing export directory was accepted")
	}
}

func TestStoreRefusesConfigSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte("do not replace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.Symlink(target, configPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store := Store{Path: configPath}
	if _, err := store.SetExportDirectory(filepath.Join(root, "exports")); err == nil {
		t.Fatal("config symlink was accepted")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do not replace\n" {
		t.Fatalf("symlink target was changed: %q", data)
	}
}

func TestStoreDoesNotEraseUnknownConfigData(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	original := []byte("{\"schema_version\":1,\"export_directory\":\"/old\",\"future_field\":true}\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store := Store{Path: configPath}
	if _, err := store.SetExportDirectory(filepath.Join(root, "exports")); err == nil {
		t.Fatal("unknown config field was silently discarded")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("unknown config data was changed: %q", data)
	}
}
