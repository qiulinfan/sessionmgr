package archive

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const (
	repositoryMetadataName = ".sessionmgr-repository.json"
	sessionMetadataName    = ".sessionmgr-session.json"
	conversationName       = "conversation.md"
)

type repositoryMetadata struct {
	SchemaVersion   int    `json:"schema_version"`
	LayoutVersion   int    `json:"layout_version"`
	RepositoryKey   string `json:"repository_key"`
	RepositoryName  string `json:"repository_name"`
	CanonicalRemote string `json:"canonical_remote"`
}

type sessionMetadata struct {
	SchemaVersion   int    `json:"schema_version"`
	LayoutVersion   int    `json:"layout_version"`
	RendererVersion int    `json:"renderer_version"`
	RepositoryKey   string `json:"repository_key"`
	RepositoryName  string `json:"repository_name"`
	DeviceID        string `json:"device_id"`
	DeviceName      string `json:"device_name"`
	SessionID       string `json:"session_id"`
	SessionKey      string `json:"session_key"`
	Title           string `json:"title"`
	SourceHash      string `json:"source_hash"`
	DocumentHash    string `json:"document_hash"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

func repositoryRecord(repo Repository) repositoryMetadata {
	return repositoryMetadata{
		SchemaVersion: SchemaVersion, LayoutVersion: LayoutVersion,
		RepositoryKey: repo.Key, RepositoryName: repo.Name, CanonicalRemote: repo.CanonicalRemote,
	}
}

func sessionRecord(snapshot Snapshot, documentHash string) sessionMetadata {
	return sessionMetadata{
		SchemaVersion: SchemaVersion, LayoutVersion: LayoutVersion, RendererVersion: RendererVersion,
		RepositoryKey: snapshot.Repository.Key, RepositoryName: snapshot.Repository.Name,
		DeviceID: snapshot.DeviceID, DeviceName: snapshot.DeviceName,
		SessionID: snapshot.Session.ID, SessionKey: snapshot.SessionKey,
		Title: snapshot.Session.Title, SourceHash: snapshot.Session.RawHash,
		DocumentHash: documentHash, CreatedAt: formatTime(snapshot.Session.CreatedAt),
		UpdatedAt: formatTime(snapshot.SourceUpdate),
	}
}

func marshalMetadata(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func readMetadata(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to read metadata through symlink: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected trailing data")
	}
	return nil
}

func validateRepositoryMetadata(value repositoryMetadata) error {
	if value.SchemaVersion != SchemaVersion || value.LayoutVersion != LayoutVersion {
		return fmt.Errorf("unsupported repository metadata schema/layout %d/%d", value.SchemaVersion, value.LayoutVersion)
	}
	if value.RepositoryKey == "" || value.CanonicalRemote == "" {
		return fmt.Errorf("incomplete repository metadata")
	}
	if value.RepositoryKey != digest("git-remote-v1\x00"+value.CanonicalRemote) {
		return fmt.Errorf("repository key does not match canonical remote")
	}
	return nil
}

func validateSessionMetadata(value sessionMetadata) error {
	if value.SchemaVersion != SchemaVersion || value.LayoutVersion != LayoutVersion {
		return fmt.Errorf("unsupported session metadata schema/layout %d/%d", value.SchemaVersion, value.LayoutVersion)
	}
	if value.RepositoryKey == "" || value.RepositoryName == "" || value.DeviceID == "" || value.DeviceName == "" ||
		value.SessionID == "" || value.Title == "" ||
		value.SessionKey == "" || value.DocumentHash == "" {
		return fmt.Errorf("incomplete session metadata")
	}
	want := digest("device-session-v1\x00" + value.DeviceID + "\x00" + value.SessionID)
	if value.SessionKey != want {
		return fmt.Errorf("session key does not match device and native session identity")
	}
	if !validSHA256(value.SourceHash) || !validSHA256(value.DocumentHash) {
		return fmt.Errorf("invalid source or document hash")
	}
	return nil
}

func validSHA256(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}

func publishRepositoryMetadata(directory string, repo Repository) error {
	want := repositoryRecord(repo)
	path := filepath.Join(directory, repositoryMetadataName)
	var current repositoryMetadata
	if err := readMetadata(path, &current); err == nil {
		if err := validateRepositoryMetadata(current); err != nil {
			return err
		}
		if !reflect.DeepEqual(current, want) {
			return fmt.Errorf("semantic repository path belongs to a different identity: %s", directory)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read repository metadata: %w", err)
	}
	if _, err := os.Lstat(directory); err == nil {
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return fmt.Errorf("refusing to claim existing semantic repository directory: %s", directory)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := marshalMetadata(want)
	if err != nil {
		return err
	}
	if _, err := publishImmutable(path, data); err != nil {
		return err
	}
	return nil
}

func replaceOwnedFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to replace symlink: %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".sessionmgr-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func semanticRepositoryDirectory(repo Repository) string {
	parts := strings.Split(repo.CanonicalRemote, "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		clean = append(clean, semanticComponent(part, "repository"))
	}
	return filepath.Join(clean...)
}

func semanticSessionDirectory(snapshot Snapshot) string {
	title := semanticComponent(snapshot.Session.Title, "codex-session")
	if snapshot.Session.CreatedAt.IsZero() {
		return title
	}
	return snapshot.Session.CreatedAt.UTC().Format("2006-01-02T15-04-05Z") + "--" + title
}
