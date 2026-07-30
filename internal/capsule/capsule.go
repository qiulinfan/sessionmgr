package capsule

import (
	"archive/tar"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"

	"github.com/sessionmgr/sessionmgr/internal/canonical"
	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/store"
)

const (
	manifestName = "sessionmgr-capsule/manifest.json"
	maxManifest  = 16 * 1024 * 1024
	maxPayload   = int64(2 * 1024 * 1024 * 1024)
)

func ExportEncrypted(objectStore *store.Store, run domain.Run, recipients []age.Recipient, destination string) (string, error) {
	manifest, err := canonical.Marshal(run)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(destination)
		}
	}()
	encrypted, err := age.Encrypt(file, recipients...)
	if err != nil {
		return "", err
	}
	compressed, err := zstd.NewWriter(encrypted,
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderCRC(true),
		zstd.WithWindowSize(1<<20),
	)
	if err != nil {
		return "", err
	}
	archive := tar.NewWriter(compressed)
	if err := writeTarBytes(archive, manifestName, manifest); err != nil {
		return "", err
	}
	objects := append([]domain.ObjectDescriptor(nil), run.Objects...)
	sort.Slice(objects, func(i, j int) bool { return objects[i].Digest < objects[j].Digest })
	checksums := make(map[string]string, len(objects)+1)
	checksums[manifestName] = store.Digest(manifest)
	for _, desc := range objects {
		path, err := objectTarPath(desc.Digest)
		if err != nil {
			return "", err
		}
		source, err := objectStore.Open(desc.Digest)
		if err != nil {
			return "", err
		}
		if err := writeTarReader(archive, path, desc.Size, source); err != nil {
			source.Close()
			return "", err
		}
		source.Close()
		checksums[path] = desc.Digest
	}
	checksumBytes, err := canonical.Marshal(checksums)
	if err != nil {
		return "", err
	}
	if err := writeTarBytes(archive, "sessionmgr-capsule/checksums.json", checksumBytes); err != nil {
		return "", err
	}
	if err := archive.Close(); err != nil {
		return "", err
	}
	if err := compressed.Close(); err != nil {
		return "", err
	}
	if err := encrypted.Close(); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	data, err := os.ReadFile(destination)
	if err != nil {
		return "", err
	}
	return store.Digest(data), nil
}

func ImportEncrypted(objectStore *store.Store, source string, identities ...age.Identity) (domain.Run, string, error) {
	file, err := os.Open(source)
	if err != nil {
		return domain.Run{}, "", err
	}
	defer file.Close()
	decrypted, err := age.Decrypt(file, identities...)
	if err != nil {
		return domain.Run{}, "", err
	}
	compressed, err := zstd.NewReader(decrypted, zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return domain.Run{}, "", err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	var run domain.Run
	descriptors := make(map[string]domain.ObjectDescriptor)
	seenManifest := false
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return domain.Run{}, "", err
		}
		if err := validateTarPath(header.Name); err != nil {
			return domain.Run{}, "", err
		}
		if header.Typeflag != tar.TypeReg {
			return domain.Run{}, "", fmt.Errorf("Capsule contains non-regular entry %s", header.Name)
		}
		switch header.Name {
		case manifestName:
			if seenManifest {
				return domain.Run{}, "", fmt.Errorf("Capsule contains multiple manifests")
			}
			if header.Size < 0 || header.Size > maxManifest {
				return domain.Run{}, "", fmt.Errorf("invalid manifest size %d", header.Size)
			}
			data, err := io.ReadAll(io.LimitReader(archive, header.Size+1))
			if err != nil {
				return domain.Run{}, "", err
			}
			if int64(len(data)) != header.Size {
				return domain.Run{}, "", fmt.Errorf("truncated manifest")
			}
			if err := json.Unmarshal(data, &run); err != nil {
				return domain.Run{}, "", err
			}
			if run.SchemaVersion != domain.SchemaVersion {
				return domain.Run{}, "", fmt.Errorf("unsupported manifest schema %d", run.SchemaVersion)
			}
			var totalSize int64
			for _, desc := range run.Objects {
				if desc.Size < 0 || desc.Size > maxPayload || totalSize > maxPayload-desc.Size {
					return domain.Run{}, "", fmt.Errorf("Capsule payload exceeds %d bytes", maxPayload)
				}
				totalSize += desc.Size
				path, err := objectTarPath(desc.Digest)
				if err != nil {
					return domain.Run{}, "", err
				}
				if _, exists := descriptors[path]; exists {
					return domain.Run{}, "", fmt.Errorf("duplicate object descriptor %s", desc.Digest)
				}
				descriptors[path] = desc
			}
			seenManifest = true
		case "sessionmgr-capsule/checksums.json":
			// Descriptors in the signed/encrypted manifest are authoritative.
			if _, err := io.Copy(io.Discard, archive); err != nil {
				return domain.Run{}, "", err
			}
		default:
			if !seenManifest {
				return domain.Run{}, "", fmt.Errorf("manifest must be the first Capsule entry")
			}
			desc, ok := descriptors[header.Name]
			if !ok {
				return domain.Run{}, "", fmt.Errorf("Capsule contains undeclared object %s", header.Name)
			}
			if header.Size != desc.Size {
				return domain.Run{}, "", fmt.Errorf("object %s size mismatch", desc.Digest)
			}
			if err := objectStore.PutVerifiedReader(desc, archive); err != nil {
				return domain.Run{}, "", err
			}
			delete(descriptors, header.Name)
		}
	}
	if !seenManifest {
		return domain.Run{}, "", fmt.Errorf("Capsule has no manifest")
	}
	for path, desc := range descriptors {
		if desc.Required {
			return domain.Run{}, "", fmt.Errorf("Capsule is missing required object %s (%s)", desc.Digest, path)
		}
	}
	manifestDigest, err := objectStore.PublishRun(run)
	if err != nil {
		return domain.Run{}, "", err
	}
	if err := objectStore.Verify(run, true); err != nil {
		return domain.Run{}, "", err
	}
	return run, manifestDigest, nil
}

func FileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, bufio.NewReader(file)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func objectTarPath(digest string) (string, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("unsupported object digest %q", digest)
	}
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	if len(hexDigest) != 64 {
		return "", fmt.Errorf("invalid object digest %q", digest)
	}
	if _, err := hex.DecodeString(hexDigest); err != nil {
		return "", fmt.Errorf("invalid object digest %q", digest)
	}
	return "sessionmgr-capsule/objects/sha256/" + hexDigest[:2] + "/" + hexDigest[2:], nil
}

func validateTarPath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return fmt.Errorf("unsafe Capsule path %q", path)
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean != path || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("unsafe Capsule path %q", path)
	}
	if !strings.HasPrefix(path, "sessionmgr-capsule/") {
		return fmt.Errorf("Capsule path outside root: %q", path)
	}
	return nil
}

func writeTarBytes(archive *tar.Writer, name string, data []byte) error {
	return writeTarReader(archive, name, int64(len(data)), strings.NewReader(string(data)))
}

func writeTarReader(archive *tar.Writer, name string, size int64, reader io.Reader) error {
	header := &tar.Header{
		Name: name, Size: size, Mode: 0o600, Typeflag: tar.TypeReg,
		ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Time{}, ChangeTime: time.Time{},
		Uid: 0, Gid: 0, Uname: "", Gname: "",
	}
	if err := archive.WriteHeader(header); err != nil {
		return err
	}
	written, err := io.Copy(archive, io.LimitReader(reader, size))
	if err != nil {
		return err
	}
	if written != size {
		return fmt.Errorf("source for %s was truncated: got %d, want %d", name, written, size)
	}
	return nil
}
