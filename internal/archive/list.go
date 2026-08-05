package archive

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func List(opts ListOptions) ([]Entry, error) {
	root, err := filepath.Abs(opts.Output)
	if err != nil {
		return nil, err
	}
	var snapshots []Entry
	err = filepath.WalkDir(filepath.Join(root, "repositories"), func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if item.IsDir() {
			return nil
		}
		if item.Name() == sessionMetadataName {
			var metadata sessionMetadata
			if err := readMetadata(path, &metadata); err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			if err := validateSessionMetadata(metadata); err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			sessionDir := filepath.Dir(path)
			deviceDir := filepath.Dir(sessionDir)
			sessionsDir := filepath.Dir(deviceDir)
			if filepath.Base(sessionsDir) != "sessions" {
				return fmt.Errorf("read %s: session metadata is outside a semantic sessions directory", path)
			}
			repositoryDir := filepath.Dir(sessionsDir)
			var repository repositoryMetadata
			if err := readMetadata(filepath.Join(repositoryDir, repositoryMetadataName), &repository); err != nil {
				return fmt.Errorf("read %s: repository metadata: %w", path, err)
			}
			if err := validateRepositoryMetadata(repository); err != nil {
				return fmt.Errorf("read %s: repository metadata: %w", path, err)
			}
			expectedRepositoryDir := filepath.Join(root, "repositories", semanticRepositoryDirectory(Repository{
				Key: repository.RepositoryKey, Name: repository.RepositoryName, CanonicalRemote: repository.CanonicalRemote,
			}))
			if filepath.Clean(repositoryDir) != filepath.Clean(expectedRepositoryDir) {
				return fmt.Errorf("read %s: repository metadata is outside its semantic path", path)
			}
			if repository.RepositoryKey != metadata.RepositoryKey || repository.RepositoryName != metadata.RepositoryName {
				return fmt.Errorf("read %s: session and repository identities do not match", path)
			}
			document := filepath.Join(filepath.Dir(path), conversationName)
			if info, err := os.Lstat(document); err != nil {
				return fmt.Errorf("read %s: session document: %w", path, err)
			} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("read %s: session document is not a regular file", path)
			}
			snapshots = append(snapshots, Entry{
				RepositoryKey: metadata.RepositoryKey, RepositoryName: metadata.RepositoryName,
				DeviceID: metadata.DeviceID, DeviceName: metadata.DeviceName,
				SessionID: metadata.SessionID, SessionKey: metadata.SessionKey, Title: metadata.Title,
				DocumentHash: metadata.DocumentHash, SourceHash: metadata.SourceHash,
				UpdatedAt: metadata.UpdatedAt, Versions: 1, Path: document,
			})
			return nil
		}
		if item.Name() == conversationName || item.Name() == "repository.md" ||
			!strings.EqualFold(filepath.Ext(item.Name()), ".md") {
			return nil
		}
		metadata, err := readFrontmatter(path)
		if err != nil {
			return nil
		}
		if metadata["session_id"] == "" || metadata["snapshot_hash"] == "" {
			return nil
		}
		snapshots = append(snapshots, Entry{
			RepositoryKey: metadata["repository_key"], RepositoryName: metadata["repository_name"],
			SessionID: metadata["session_id"], Title: metadata["session_title"],
			SnapshotHash: metadata["snapshot_hash"], SourceHash: metadata["source_hash"],
			UpdatedAt: metadata["updated_at"], Versions: 1, Legacy: true, Path: path,
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if opts.History {
		sortEntries(snapshots)
		return snapshots, nil
	}
	currentNativeSessions := make(map[string]bool)
	for _, entry := range snapshots {
		if !entry.Legacy {
			currentNativeSessions[entry.RepositoryKey+"\x00"+entry.SessionID] = true
		}
	}
	latest := make(map[string]Entry)
	versions := make(map[string]int)
	for _, entry := range snapshots {
		if entry.Legacy && currentNativeSessions[entry.RepositoryKey+"\x00"+entry.SessionID] {
			continue
		}
		itemKey := entryIdentity(entry)
		versions[itemKey]++
		current, exists := latest[itemKey]
		if !exists || newerEntry(entry, current) {
			latest[itemKey] = entry
		}
	}
	result := make([]Entry, 0, len(latest))
	for itemKey, entry := range latest {
		entry.Versions = versions[itemKey]
		result = append(result, entry)
	}
	sortEntries(result)
	return result, nil
}

func entryIdentity(entry Entry) string {
	if entry.SessionKey != "" {
		return entry.RepositoryKey + "\x00" + entry.SessionKey
	}
	return entry.RepositoryKey + "\x00legacy\x00" + entry.SessionID
}

func newerEntry(candidate, current Entry) bool {
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidate.UpdatedAt)
	currentTime, currentErr := time.Parse(time.RFC3339Nano, current.UpdatedAt)
	if candidateErr == nil && currentErr == nil && !candidateTime.Equal(currentTime) {
		return candidateTime.After(currentTime)
	}
	if candidate.UpdatedAt != current.UpdatedAt {
		return candidate.UpdatedAt > current.UpdatedAt
	}
	candidateHash := candidate.DocumentHash + candidate.SnapshotHash
	currentHash := current.DocumentHash + current.SnapshotHash
	return candidateHash > currentHash
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].RepositoryName != entries[j].RepositoryName {
			return entries[i].RepositoryName < entries[j].RepositoryName
		}
		if entries[i].UpdatedAt != entries[j].UpdatedAt {
			return entries[i].UpdatedAt > entries[j].UpdatedAt
		}
		return entries[i].SessionID < entries[j].SessionID
	})
}

func readFrontmatter(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 16*1024), 1024*1024)
	if !scanner.Scan() || scanner.Text() != "---" {
		return nil, fmt.Errorf("missing frontmatter")
	}
	result := make(map[string]string)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			return result, nil
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			result[strings.TrimSpace(key)] = unquoted
		} else {
			var text string
			if json.Unmarshal([]byte(value), &text) == nil {
				result[strings.TrimSpace(key)] = text
			} else {
				result[strings.TrimSpace(key)] = value
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("unterminated frontmatter")
}
