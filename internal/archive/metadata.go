package archive

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
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
	CanonicalRemote string `json:"canonical_remote,omitempty"`
	RepositoryKind  string `json:"repository_kind,omitempty"`
	DirectoryName   string `json:"directory_name,omitempty"`
	DirectoryID     string `json:"directory_id,omitempty"`
	DeviceID        string `json:"device_id,omitempty"`
	DeviceName      string `json:"device_name,omitempty"`
}

type sessionMetadata struct {
	SchemaVersion           int                  `json:"schema_version"`
	LayoutVersion           int                  `json:"layout_version"`
	RendererVersion         int                  `json:"renderer_version"`
	AttachmentSchemaVersion int                  `json:"attachment_schema_version,omitempty"`
	RepositoryKey           string               `json:"repository_key"`
	RepositoryName          string               `json:"repository_name"`
	DeviceID                string               `json:"device_id"`
	DeviceName              string               `json:"device_name"`
	SessionID               string               `json:"session_id"`
	SessionKey              string               `json:"session_key"`
	Title                   string               `json:"title"`
	SourceHash              string               `json:"source_hash"`
	DocumentHash            string               `json:"document_hash"`
	CreatedAt               string               `json:"created_at,omitempty"`
	UpdatedAt               string               `json:"updated_at,omitempty"`
	Attachments             []attachmentMetadata `json:"attachments,omitempty"`
}

type attachmentMetadata struct {
	MessageIndex    int    `json:"message_index"`
	AttachmentIndex int    `json:"attachment_index"`
	Name            string `json:"name"`
	MIMEType        string `json:"mime_type,omitempty"`
	SourceKind      string `json:"source_kind"`
	Status          string `json:"status"`
	ArchivePath     string `json:"archive_path,omitempty"`
	RepositoryPath  string `json:"repository_path,omitempty"`
	GitCommit       string `json:"git_commit,omitempty"`
	Size            int64  `json:"size,omitempty"`
	ContentHash     string `json:"content_hash,omitempty"`
}

func repositoryRecord(repo Repository) repositoryMetadata {
	record := repositoryMetadata{
		SchemaVersion: SchemaVersion, LayoutVersion: LayoutVersion,
		RepositoryKey: repo.Key, RepositoryName: repo.Name, CanonicalRemote: repo.CanonicalRemote,
	}
	if repo.Kind == repositoryKindLocalDirectory {
		record.SchemaVersion = LocalRepositorySchema
		record.RepositoryKind = repo.Kind
		record.DirectoryName = repo.DirectoryName
		record.DirectoryID = repo.DirectoryID
		record.DeviceID = repo.DeviceID
		record.DeviceName = repo.DeviceName
	}
	return record
}

func sessionRecord(snapshot Snapshot, documentHash string) sessionMetadata {
	record := sessionMetadata{
		SchemaVersion: SchemaVersion, LayoutVersion: LayoutVersion, RendererVersion: RendererVersion,
		AttachmentSchemaVersion: 1,
		RepositoryKey:           snapshot.Repository.Key, RepositoryName: snapshot.Repository.Name,
		DeviceID: snapshot.DeviceID, DeviceName: snapshot.DeviceName,
		SessionID: snapshot.Session.ID, SessionKey: snapshot.SessionKey,
		Title: snapshot.Session.Title, SourceHash: snapshot.Session.RawHash,
		DocumentHash: documentHash, CreatedAt: formatTime(snapshot.Session.CreatedAt),
		UpdatedAt: formatTime(snapshot.SourceUpdate),
	}
	for _, message := range snapshot.Session.Messages {
		for _, attachment := range message.Attachments {
			record.Attachments = append(record.Attachments, attachmentRecord(attachment))
		}
	}
	return record
}

func attachmentRecord(value Attachment) attachmentMetadata {
	return attachmentMetadata{
		MessageIndex: value.MessageIndex, AttachmentIndex: value.AttachmentIndex,
		Name: value.Name, MIMEType: value.MIMEType, SourceKind: value.SourceKind,
		Status: value.Status, ArchivePath: value.ArchivePath,
		RepositoryPath: value.RepositoryPath, GitCommit: value.GitCommit,
		Size: value.Size, ContentHash: value.ContentHash,
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
	if (value.SchemaVersion != SchemaVersion && value.SchemaVersion != LocalRepositorySchema) || !supportedLayoutVersion(value.LayoutVersion) {
		return fmt.Errorf("unsupported repository metadata schema/layout %d/%d", value.SchemaVersion, value.LayoutVersion)
	}
	if value.RepositoryKey == "" || value.RepositoryName == "" {
		return fmt.Errorf("incomplete repository metadata")
	}
	if value.RepositoryKind == "" {
		if value.SchemaVersion != SchemaVersion || value.CanonicalRemote == "" || value.DirectoryName != "" || value.DirectoryID != "" ||
			value.DeviceID != "" || value.DeviceName != "" {
			return fmt.Errorf("invalid hosted repository metadata")
		}
		if value.RepositoryKey != digest("git-remote-v1\x00"+value.CanonicalRemote) {
			return fmt.Errorf("repository key does not match canonical remote")
		}
		return nil
	}
	if value.SchemaVersion != LocalRepositorySchema || value.RepositoryKind != repositoryKindLocalDirectory ||
		value.LayoutVersion != LayoutVersion || value.CanonicalRemote != "" ||
		value.DirectoryName == "" || !validSHA256(value.DirectoryID) ||
		value.DeviceID == "" || value.DeviceName == "" ||
		value.RepositoryKey != digest("local-directory-v1\x00"+value.DeviceID+"\x00"+value.DirectoryID) {
		return fmt.Errorf("invalid local-directory repository metadata")
	}
	return nil
}

func validateSessionMetadata(value sessionMetadata) error {
	if value.SchemaVersion != SchemaVersion || !supportedLayoutVersion(value.LayoutVersion) {
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
	if value.AttachmentSchemaVersion != 0 && value.AttachmentSchemaVersion != 1 {
		return fmt.Errorf("unsupported attachment metadata schema %d", value.AttachmentSchemaVersion)
	}
	if len(value.Attachments) > 0 && value.AttachmentSchemaVersion != 1 {
		return fmt.Errorf("attachment metadata has no supported schema")
	}
	seen := make(map[string]bool, len(value.Attachments))
	for _, attachment := range value.Attachments {
		if err := validateAttachmentMetadata(attachment); err != nil {
			return err
		}
		identity := fmt.Sprintf("%d/%d", attachment.MessageIndex, attachment.AttachmentIndex)
		if seen[identity] {
			return fmt.Errorf("duplicate attachment identity %s", identity)
		}
		seen[identity] = true
	}
	return nil
}

func supportedLayoutVersion(value int) bool {
	return value == 3 || value == 4 || value == LayoutVersion
}

func validateAttachmentMetadata(value attachmentMetadata) error {
	if value.MessageIndex < 1 || value.AttachmentIndex < 1 || strings.TrimSpace(value.Name) == "" || value.SourceKind == "" {
		return fmt.Errorf("incomplete attachment metadata")
	}
	switch value.Status {
	case attachmentStatusArchived:
		if !validAttachmentPath(value.ArchivePath) || !validSHA256(value.ContentHash) || value.Size < 0 || value.Size > MaxAttachmentBytes {
			return fmt.Errorf("invalid archived attachment metadata")
		}
	case attachmentStatusGitTracked:
		if value.RepositoryPath == "" || value.GitCommit == "" || !validSHA256(value.ContentHash) || value.Size < 0 || value.Size > MaxAttachmentBytes {
			return fmt.Errorf("invalid Git attachment metadata")
		}
		clean := pathpkg.Clean(value.RepositoryPath)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || pathpkg.IsAbs(clean) || clean != value.RepositoryPath {
			return fmt.Errorf("unsafe Git attachment path")
		}
	case attachmentStatusTooLarge:
		if value.Size <= MaxAttachmentBytes {
			return fmt.Errorf("invalid oversized attachment metadata")
		}
	case attachmentStatusBusy, attachmentStatusUnavailable, attachmentStatusRemoteReference, attachmentStatusSensitive:
		if value.ArchivePath != "" {
			return fmt.Errorf("unarchived attachment has an archive path")
		}
	default:
		return fmt.Errorf("unknown attachment status %q", value.Status)
	}
	return nil
}

func validAttachmentPath(value string) bool {
	clean := pathpkg.Clean(value)
	return strings.HasPrefix(clean, "attachments/") && clean == value && !pathpkg.IsAbs(clean) && !strings.Contains(clean, "../")
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
		if reflect.DeepEqual(current, want) {
			return nil
		}
		if current.SchemaVersion != want.SchemaVersion || current.RepositoryKey != want.RepositoryKey ||
			current.RepositoryName != want.RepositoryName || current.CanonicalRemote != want.CanonicalRemote ||
			current.LayoutVersion >= want.LayoutVersion {
			return fmt.Errorf("semantic repository path belongs to a different identity: %s", directory)
		}
		data, err := marshalMetadata(want)
		if err != nil {
			return err
		}
		return replaceOwnedFile(path, data)
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
	if repo.Kind == repositoryKindLocalDirectory {
		namespace := semanticComponent("non-git-"+repo.DeviceName, "non-git-device")
		name := semanticComponent(repo.DirectoryName, "directory")
		return filepath.Join(namespace, name)
	}
	parts := strings.Split(repo.CanonicalRemote, "/")
	if len(parts) < 2 {
		return semanticComponent(repo.CanonicalRemote, "repository")
	}
	namespace := semanticComponent(strings.Join(parts[:len(parts)-1], "-"), "repository")
	name := semanticComponent(parts[len(parts)-1], "repository")
	return filepath.Join(namespace, name)
}

func semanticRepositoryDirectoryV3(repo Repository) string {
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
