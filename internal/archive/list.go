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
		if item.IsDir() || item.Name() == "repository.md" || !strings.EqualFold(filepath.Ext(item.Name()), ".md") {
			return nil
		}
		metadata, err := readFrontmatter(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if metadata["session_id"] == "" || metadata["snapshot_hash"] == "" {
			return nil
		}
		snapshots = append(snapshots, Entry{
			RepositoryKey: metadata["repository_key"], RepositoryName: metadata["repository_name"],
			SessionID: metadata["session_id"], Title: metadata["session_title"],
			SnapshotHash: metadata["snapshot_hash"], SourceHash: metadata["source_hash"],
			UpdatedAt: metadata["updated_at"], Versions: 1, Path: path,
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
	type key struct{ repo, session string }
	latest := make(map[key]Entry)
	versions := make(map[key]int)
	for _, entry := range snapshots {
		itemKey := key{entry.RepositoryKey, entry.SessionID}
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

func newerEntry(candidate, current Entry) bool {
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidate.UpdatedAt)
	currentTime, currentErr := time.Parse(time.RFC3339Nano, current.UpdatedAt)
	if candidateErr == nil && currentErr == nil && !candidateTime.Equal(currentTime) {
		return candidateTime.After(currentTime)
	}
	if candidate.UpdatedAt != current.UpdatedAt {
		return candidate.UpdatedAt > current.UpdatedAt
	}
	return candidate.SnapshotHash > current.SnapshotHash
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
