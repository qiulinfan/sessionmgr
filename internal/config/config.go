package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	SchemaVersion       = 2
	legacySchemaVersion = 1
)

type SourcePreferences struct {
	Codex      bool `json:"codex"`
	ClaudeCode bool `json:"claude_code"`
	DeepSeek   bool `json:"deepseek"`
}

type Config struct {
	SchemaVersion     int                `json:"schema_version"`
	ExportDirectory   string             `json:"export_directory"`
	DeviceID          string             `json:"device_id,omitempty"`
	DeviceName        string             `json:"device_name,omitempty"`
	SourcePreferences *SourcePreferences `json:"sources,omitempty"`
}

type Store struct {
	Path string
}

func DefaultStore() (Store, error) {
	if override := strings.TrimSpace(os.Getenv("SESSIONMGR_CONFIG")); override != "" {
		path, err := filepath.Abs(override)
		return Store{Path: path}, err
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return Store{}, err
	}
	return Store{Path: filepath.Join(root, "sessionmgr", "config.json")}, nil
}

func (store Store) Load() (Config, error) {
	data, err := os.ReadFile(store.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var result Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", store.Path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse config %s: unexpected trailing data", store.Path)
	}
	if result.SchemaVersion != SchemaVersion && result.SchemaVersion != legacySchemaVersion {
		return Config{}, fmt.Errorf("unsupported config schema %d", result.SchemaVersion)
	}
	if result.SchemaVersion == legacySchemaVersion {
		if result.SourcePreferences != nil {
			return Config{}, fmt.Errorf("legacy config declares source preferences")
		}
		result.SchemaVersion = SchemaVersion
	}
	return result, nil
}

func (store Store) SetExportDirectory(directory string) (Config, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return Config{}, fmt.Errorf("export directory is required")
	}
	current, err := store.Load()
	if err != nil {
		return Config{}, err
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return Config{}, fmt.Errorf("create export directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Config{}, err
	}
	if !info.IsDir() {
		return Config{}, fmt.Errorf("export path is not a directory: %s", abs)
	}
	result := Config{
		SchemaVersion: SchemaVersion, ExportDirectory: filepath.Clean(abs),
		DeviceID: current.DeviceID, DeviceName: current.DeviceName, SourcePreferences: current.SourcePreferences,
	}
	if err := store.save(result); err != nil {
		return Config{}, err
	}
	return result, nil
}

func (store Store) SetSourcePreferences(preferences SourcePreferences) (Config, error) {
	current, err := store.Load()
	if err != nil {
		return Config{}, err
	}
	current.SchemaVersion = SchemaVersion
	current.SourcePreferences = &SourcePreferences{
		Codex: preferences.Codex, ClaudeCode: preferences.ClaudeCode, DeepSeek: preferences.DeepSeek,
	}
	if err := store.save(current); err != nil {
		return Config{}, err
	}
	return current, nil
}

// EnsureDevice returns a stable, machine-local identity. It is intentionally
// kept in the local config rather than the Git-managed export directory so two
// machines never inherit the same device identity by pulling the archive.
func (store Store) EnsureDevice() (Config, error) {
	value, err := store.Load()
	if err != nil {
		return Config{}, err
	}
	changed := false
	if strings.TrimSpace(value.DeviceID) == "" {
		data := make([]byte, 16)
		if _, err := rand.Read(data); err != nil {
			return Config{}, fmt.Errorf("create device identity: %w", err)
		}
		value.DeviceID = "device:" + hex.EncodeToString(data)
		changed = true
	}
	if strings.TrimSpace(value.DeviceName) == "" {
		name, _ := os.Hostname()
		name = strings.TrimSpace(name)
		if name == "" {
			name = "device"
		}
		value.DeviceName = name
		changed = true
	}
	if changed {
		if err := store.save(value); err != nil {
			return Config{}, err
		}
	}
	return value, nil
}

func (store Store) save(value Config) error {
	if store.Path == "" {
		return fmt.Errorf("config path is required")
	}
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(store.Path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write config through symlink: %s", store.Path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(store.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func (store Store) ResolveDirectory(override string, remember bool) (string, error) {
	if strings.TrimSpace(override) != "" {
		if remember {
			value, err := store.SetExportDirectory(override)
			return value.ExportDirectory, err
		}
		abs, err := filepath.Abs(override)
		return filepath.Clean(abs), err
	}
	value, err := store.Load()
	if err != nil {
		return "", err
	}
	if value.ExportDirectory == "" {
		return "", fmt.Errorf("export directory is not configured; run: sessionmgr config set-directory PATH")
	}
	return value.ExportDirectory, nil
}
