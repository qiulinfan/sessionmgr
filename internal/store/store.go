package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sessionmgr/sessionmgr/internal/canonical"
	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/home"
)

type Store struct {
	layout home.Layout
}

func New(layout home.Layout) *Store {
	return &Store{layout: layout}
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Store) PutBytes(data []byte, mediaType string, required bool) (domain.ObjectDescriptor, error) {
	digest := Digest(data)
	path, err := s.ObjectPath(digest)
	if err != nil {
		return domain.ObjectDescriptor{}, err
	}
	if err := writeImmutable(path, data); err != nil {
		return domain.ObjectDescriptor{}, err
	}
	return domain.ObjectDescriptor{
		Digest: digest, MediaType: mediaType, Size: int64(len(data)),
		Encoding: "identity", Required: required,
	}, nil
}

func (s *Store) PutFile(path, mediaType string, required bool) (domain.ObjectDescriptor, error) {
	file, err := os.Open(path)
	if err != nil {
		return domain.ObjectDescriptor{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return domain.ObjectDescriptor{}, err
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	objectPath, err := s.ObjectPath(digest)
	if err != nil {
		return domain.ObjectDescriptor{}, err
	}
	if _, err := os.Stat(objectPath); os.IsNotExist(err) {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return domain.ObjectDescriptor{}, err
		}
		if err := copyImmutable(objectPath, file); err != nil {
			return domain.ObjectDescriptor{}, err
		}
	} else if err != nil {
		return domain.ObjectDescriptor{}, err
	}
	return domain.ObjectDescriptor{
		Digest: digest, MediaType: mediaType, Size: size,
		Encoding: "identity", Required: required,
	}, nil
}

func (s *Store) PutVerifiedReader(desc domain.ObjectDescriptor, r io.Reader) error {
	if desc.Size < 0 {
		return fmt.Errorf("invalid object size %d", desc.Size)
	}
	expectedPath, err := s.ObjectPath(desc.Digest)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(expectedPath); statErr == nil {
		if info.Size() != desc.Size {
			return fmt.Errorf("existing object %s size mismatch", desc.Digest)
		}
		return nil
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err := os.MkdirAll(filepath.Dir(expectedPath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(expectedPath), ".import-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(r, desc.Size+1))
	if err != nil {
		tmp.Close()
		return err
	}
	if written != desc.Size {
		tmp.Close()
		return fmt.Errorf("object %s size mismatch: got %d, want %d", desc.Digest, written, desc.Size)
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != desc.Digest {
		tmp.Close()
		return fmt.Errorf("object checksum mismatch: got %s, want %s", actual, desc.Digest)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, expectedPath)
}

func (s *Store) Get(digest string) ([]byte, error) {
	path, err := s.ObjectPath(digest)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (s *Store) Open(digest string) (*os.File, error) {
	path, err := s.ObjectPath(digest)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *Store) ObjectPath(digest string) (string, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("unsupported digest %q", digest)
	}
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	if len(hexDigest) != 64 {
		return "", fmt.Errorf("invalid sha256 digest %q", digest)
	}
	if _, err := hex.DecodeString(hexDigest); err != nil {
		return "", fmt.Errorf("invalid sha256 digest %q", digest)
	}
	return filepath.Join(s.layout.Objects, "sha256", hexDigest[:2], hexDigest[2:]), nil
}

func (s *Store) PublishRun(run domain.Run) (string, error) {
	manifest, err := canonical.Marshal(run)
	if err != nil {
		return "", err
	}
	desc, err := s.PutBytes(manifest, "application/vnd.sessionmgr.manifest.v1+json", true)
	if err != nil {
		return "", err
	}
	runDir := filepath.Join(s.layout.Runs, run.ID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return "", err
	}
	if err := writeImmutable(filepath.Join(runDir, "manifest.json"), manifest); err != nil {
		return "", err
	}
	refPath := filepath.Join(s.layout.Refs, "runs", run.ID)
	if existing, err := os.ReadFile(refPath); err == nil {
		if strings.TrimSpace(string(existing)) != desc.Digest {
			return "", fmt.Errorf("run ID conflict: %s already points to another manifest", run.ID)
		}
		return desc.Digest, nil
	}
	if err := atomicWrite(refPath, []byte(desc.Digest+"\n"), 0o600); err != nil {
		return "", err
	}
	return desc.Digest, nil
}

func (s *Store) LoadRun(id string) (domain.Run, error) {
	resolved, err := s.ResolveRunID(id)
	if err != nil {
		return domain.Run{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.layout.Runs, resolved, "manifest.json"))
	if err != nil {
		return domain.Run{}, err
	}
	var run domain.Run
	if err := json.Unmarshal(data, &run); err != nil {
		return domain.Run{}, err
	}
	if run.SchemaVersion != domain.SchemaVersion {
		return domain.Run{}, fmt.Errorf("unsupported manifest schema %d", run.SchemaVersion)
	}
	return run, nil
}

func (s *Store) ResolveRunID(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("run ID is required")
	}
	if _, err := os.Stat(filepath.Join(s.layout.Refs, "runs", id)); err == nil {
		return id, nil
	}
	entries, err := os.ReadDir(filepath.Join(s.layout.Refs, "runs"))
	if err != nil {
		return "", err
	}
	var matches []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), id) {
			matches = append(matches, entry.Name())
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("run %q not found", id)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("run prefix %q is ambiguous", id)
	}
	return matches[0], nil
}

func (s *Store) ListRunIDs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.layout.Refs, "runs"))
	if err != nil {
		return nil, err
	}
	var result []string
	for _, entry := range entries {
		if !entry.IsDir() {
			result = append(result, entry.Name())
		}
	}
	sort.Strings(result)
	return result, nil
}

func (s *Store) ManifestDigest(id string) (string, error) {
	resolved, err := s.ResolveRunID(id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(s.layout.Refs, "runs", resolved))
	if err != nil {
		return "", err
	}
	digest := strings.TrimSpace(string(data))
	if _, err := s.ObjectPath(digest); err != nil {
		return "", err
	}
	return digest, nil
}

func (s *Store) Verify(run domain.Run, deep bool) error {
	ref, err := os.ReadFile(filepath.Join(s.layout.Refs, "runs", run.ID))
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(s.layout.Runs, run.ID, "manifest.json")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if got := Digest(manifest); got != strings.TrimSpace(string(ref)) {
		return fmt.Errorf("manifest checksum mismatch: got %s", got)
	}
	for _, desc := range run.Objects {
		path, err := s.ObjectPath(desc.Digest)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			if desc.Required {
				return fmt.Errorf("required object %s: %w", desc.Digest, err)
			}
			continue
		}
		if info.Size() != desc.Size {
			return fmt.Errorf("object %s size mismatch: got %d, want %d", desc.Digest, info.Size(), desc.Size)
		}
		if deep {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if Digest(data) != desc.Digest {
				return fmt.Errorf("object %s checksum mismatch", desc.Digest)
			}
		}
	}
	return nil
}

func writeImmutable(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if Digest(existing) != Digest(data) {
			return fmt.Errorf("immutable object collision at %s", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

func copyImmutable(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".object-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".publish-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
