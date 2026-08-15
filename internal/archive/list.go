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
	repositoryDirs, err := discoverRepositoryDirectories(root)
	if err != nil {
		return nil, err
	}
	for _, repositoryDir := range repositoryDirs {
		entries, readErr := readRepositoryEntries(root, repositoryDir)
		if readErr != nil {
			return nil, readErr
		}
		snapshots = append(snapshots, entries...)
	}
	legacy, err := readLegacyEntries(filepath.Join(root, "repositories"))
	if err != nil {
		return nil, err
	}
	snapshots = append(snapshots, legacy...)
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

func discoverRepositoryDirectories(root string) ([]string, error) {
	seen := make(map[string]bool)
	legacyRoot := filepath.Join(root, "repositories")
	err := filepath.WalkDir(legacyRoot, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if item.IsDir() || item.Name() != repositoryMetadataName {
			return nil
		}
		seen[filepath.Dir(path)] = true
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	groups, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, group := range groups {
		if !group.IsDir() || group.Name() == "repositories" {
			continue
		}
		groupPath := filepath.Join(root, group.Name())
		repositories, readErr := os.ReadDir(groupPath)
		if readErr != nil {
			return nil, readErr
		}
		for _, repository := range repositories {
			if !repository.IsDir() {
				continue
			}
			repositoryDir := filepath.Join(groupPath, repository.Name())
			if info, statErr := os.Lstat(filepath.Join(repositoryDir, repositoryMetadataName)); statErr == nil && info.Mode().IsRegular() {
				seen[repositoryDir] = true
			} else if statErr != nil && !os.IsNotExist(statErr) {
				return nil, statErr
			}
		}
	}
	result := make([]string, 0, len(seen))
	for directory := range seen {
		result = append(result, directory)
	}
	sort.Strings(result)
	return result, nil
}

func readRepositoryEntries(root, repositoryDir string) ([]Entry, error) {
	metadataPath := filepath.Join(repositoryDir, repositoryMetadataName)
	var repository repositoryMetadata
	if err := readMetadata(metadataPath, &repository); err != nil {
		return nil, fmt.Errorf("read %s: %w", metadataPath, err)
	}
	if err := validateRepositoryMetadata(repository); err != nil {
		return nil, fmt.Errorf("read %s: %w", metadataPath, err)
	}
	repo := Repository{
		Key: repository.RepositoryKey, Name: repository.RepositoryName,
		CanonicalRemote: repository.CanonicalRemote, Kind: repository.RepositoryKind,
		DirectoryName: repository.DirectoryName, DirectoryID: repository.DirectoryID,
		DeviceID: repository.DeviceID, DeviceName: repository.DeviceName,
	}
	var expected string
	if repository.LayoutVersion == 3 {
		expected = filepath.Join(root, "repositories", semanticRepositoryDirectoryV3(repo))
	} else {
		expected = filepath.Join(root, semanticRepositoryDirectory(repo))
	}
	locationMatches := filepath.Clean(repositoryDir) == filepath.Clean(expected)
	draftLocalLocation := false
	if !locationMatches && repository.RepositoryKind == repositoryKindLocalDirectory {
		draft := filepath.Join(root, semanticLocalRepositoryDirectoryDraft(repo))
		locationMatches = filepath.Clean(repositoryDir) == filepath.Clean(draft)
		draftLocalLocation = locationMatches
	}
	if !locationMatches {
		return nil, fmt.Errorf("read %s: repository metadata is outside its semantic path", metadataPath)
	}
	var result []Entry
	err := filepath.WalkDir(repositoryDir, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if item.IsDir() || item.Name() != sessionMetadataName {
			return nil
		}
		var metadata sessionMetadata
		if err := readMetadata(path, &metadata); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := validateSessionMetadata(metadata); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if repository.RepositoryKind == repositoryKindLocalDirectory && metadata.LayoutVersion != LayoutVersion {
			return fmt.Errorf("read %s: local-directory session uses unsupported layout %d", path, metadata.LayoutVersion)
		}
		sessionDir := filepath.Dir(path)
		locationValid := validSessionMetadataLocation(repositoryDir, repository.LayoutVersion, filepath.Dir(sessionDir), metadata.LayoutVersion)
		if repository.RepositoryKind == repositoryKindLocalDirectory {
			locationValid = validLocalSessionMetadataLocation(repositoryDir, sessionDir, metadata, draftLocalLocation)
		}
		if !locationValid {
			return fmt.Errorf("read %s: session metadata is outside its semantic session directory", path)
		}
		if repository.RepositoryKey != metadata.RepositoryKey || repository.RepositoryName != metadata.RepositoryName {
			return fmt.Errorf("read %s: session and repository identities do not match", path)
		}
		document := filepath.Join(sessionDir, conversationName)
		if info, err := os.Lstat(document); err != nil {
			return fmt.Errorf("read %s: session document: %w", path, err)
		} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("read %s: session document is not a regular file", path)
		}
		result = append(result, Entry{
			RepositoryKey: metadata.RepositoryKey, RepositoryName: metadata.RepositoryName,
			Harness:  sessionMetadataHarness(metadata),
			DeviceID: metadata.DeviceID, DeviceName: metadata.DeviceName,
			SessionID: metadata.SessionID, SessionKey: metadata.SessionKey, Title: metadata.Title,
			DocumentHash: metadata.DocumentHash, SourceHash: metadata.SourceHash,
			UpdatedAt: metadata.UpdatedAt, Versions: 1, Path: document,
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return result, nil
}

func validLocalSessionMetadataLocation(repositoryDir, sessionDir string, metadata sessionMetadata, draftRepository bool) bool {
	if metadata.LayoutVersion != LayoutVersion {
		return false
	}
	if !draftRepository {
		return filepath.Clean(filepath.Dir(sessionDir)) == filepath.Clean(repositoryDir)
	}
	draftDeviceDir := filepath.Dir(sessionDir)
	return filepath.Clean(filepath.Dir(draftDeviceDir)) == filepath.Clean(repositoryDir) &&
		filepath.Base(draftDeviceDir) == semanticComponent(metadata.DeviceName, "device")
}

func validSessionMetadataLocation(repositoryDir string, repositoryLayout int, deviceDir string, sessionLayout int) bool {
	direct := filepath.Clean(filepath.Dir(deviceDir)) == filepath.Clean(repositoryDir)
	legacy := filepath.Clean(filepath.Dir(deviceDir)) == filepath.Clean(filepath.Join(repositoryDir, "sessions"))
	if repositoryLayout == 3 {
		return legacy && sessionLayout == 3
	}
	if direct {
		// A v3/v4 sidecar at the v5 destination is the recoverable state after
		// its verified directory move and before sidecar-last publication.
		return supportedLayoutVersion(sessionLayout)
	}
	return legacy && (sessionLayout == 3 || sessionLayout == 4)
}

func readLegacyEntries(root string) ([]Entry, error) {
	var result []Entry
	err := filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if item.IsDir() {
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
		result = append(result, Entry{
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
