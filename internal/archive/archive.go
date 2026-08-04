package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var safeSessionID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func Export(ctx context.Context, opts Options) (Result, error) {
	result := Result{SchemaVersion: SchemaVersion, Changes: []Change{}}
	if opts.CodexHome == "" {
		var err error
		opts.CodexHome, err = DefaultCodexHome()
		if err != nil {
			return result, err
		}
	}
	if opts.Output == "" {
		return result, fmt.Errorf("export directory is required")
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return result, err
	}
	result.Output = output
	if err := os.MkdirAll(filepath.Join(output, "repositories"), 0o755); err != nil {
		return result, err
	}
	existing, err := List(ListOptions{Output: output, History: true})
	if err != nil {
		return result, fmt.Errorf("inspect existing archive: %w", err)
	}
	history := make(map[string][]Entry)
	for _, entry := range existing {
		key := entry.RepositoryKey + "\x00" + entry.SessionID
		history[key] = append(history[key], entry)
	}
	files, err := discoverSessionFiles(opts.CodexHome)
	if err != nil {
		return result, err
	}
	result.Sources = len(files)
	titles, err := loadTitles(opts.CodexHome)
	if err != nil {
		return result, fmt.Errorf("read Codex session titles: %w", err)
	}
	var target Repository
	if !opts.AllRepos {
		if opts.Repo == "" {
			opts.Repo = "."
		}
		target, err = RepositoryFromPath(ctx, opts.Repo)
		if err != nil {
			return result, err
		}
	}
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		raw, readErr := readStable(ctx, path)
		if readErr != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", filepath.Base(path), readErr))
			continue
		}
		fallbackID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		session, parseErr := parseSession(raw, fallbackID, titles)
		if parseErr != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", filepath.Base(path), parseErr))
			continue
		}
		if opts.SessionID != "" && session.ID != opts.SessionID {
			continue
		}
		repo, repoErr := repositoryForSession(ctx, session)
		if repoErr != nil {
			if opts.AllRepos || opts.SessionID != "" {
				result.Skipped++
				result.Warnings = append(result.Warnings, fmt.Sprintf("session %s: %v", session.ID, repoErr))
			}
			continue
		}
		if !opts.AllRepos && repo.Key != target.Key {
			continue
		}
		result.Matched++
		snapshot := makeSnapshot(repo, session)
		created, snapshotPath, publishErr := publishSnapshot(output, snapshot)
		if publishErr != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("session %s: %v", session.ID, publishErr))
			continue
		}
		if created {
			result.Created++
			key := repo.Key + "\x00" + snapshot.Session.ID
			kind := changeKind(history[key], snapshot)
			change := Change{
				Kind: kind, RepositoryKey: repo.Key, RepositoryName: repo.Name,
				SessionID: snapshot.Session.ID, Title: snapshot.Session.Title,
				SnapshotHash: snapshot.Hash, SourceHash: snapshot.Session.RawHash,
				UpdatedAt: formatTime(snapshot.SourceUpdate), Path: snapshotPath,
			}
			result.Changes = append(result.Changes, change)
			history[key] = append(history[key], Entry{
				RepositoryKey: repo.Key, RepositoryName: repo.Name, SessionID: snapshot.Session.ID,
				Title: snapshot.Session.Title, SnapshotHash: snapshot.Hash,
				SourceHash: snapshot.Session.RawHash, UpdatedAt: formatTime(snapshot.SourceUpdate), Path: snapshotPath,
			})
		} else {
			result.Unchanged++
		}
	}
	if opts.SessionID != "" && result.Matched == 0 {
		return result, fmt.Errorf("Codex session %q was not found for the selected repository scope", opts.SessionID)
	}
	if result.Skipped > 0 {
		sortChanges(result.Changes)
		return result, fmt.Errorf("export completed with %d skipped session source(s)", result.Skipped)
	}
	sortChanges(result.Changes)
	return result, nil
}

func publishSnapshot(output string, snapshot Snapshot) (bool, string, error) {
	repositoryDir := filepath.Join(output, "repositories", repositoryDirectory(snapshot.Repository))
	if _, err := publishImmutable(filepath.Join(repositoryDir, "repository.md"), renderRepository(snapshot.Repository)); err != nil {
		return false, "", fmt.Errorf("publish repository identity: %w", err)
	}
	sessionDir := snapshot.Session.ID
	if !safeSessionID.MatchString(sessionDir) {
		sessionDir = "session--" + strings.TrimPrefix(digest(sessionDir), "sha256:")[:16]
	}
	filename := slug(snapshot.Session.Title) + "--" + strings.TrimPrefix(snapshot.Hash, "sha256:") + ".md"
	path := filepath.Join(repositoryDir, "sessions", sessionDir, filename)
	created, err := publishImmutable(path, renderSnapshot(snapshot))
	return created, path, err
}

func changeKind(history []Entry, snapshot Snapshot) string {
	if len(history) == 0 {
		return "new"
	}
	latest := history[0]
	for _, candidate := range history[1:] {
		if newerEntry(candidate, latest) {
			latest = candidate
		}
	}
	if latest.SourceHash == snapshot.Session.RawHash && latest.Title != snapshot.Session.Title {
		return "renamed"
	}
	return "updated"
}

func sortChanges(changes []Change) {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].RepositoryName != changes[j].RepositoryName {
			return changes[i].RepositoryName < changes[j].RepositoryName
		}
		if changes[i].UpdatedAt != changes[j].UpdatedAt {
			return changes[i].UpdatedAt > changes[j].UpdatedAt
		}
		return changes[i].SessionID < changes[j].SessionID
	})
}

func publishImmutable(path string, data []byte) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, data) {
			return false, nil
		}
		return false, fmt.Errorf("refusing to overwrite conflicting file %s", path)
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".sessionmgr-*")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return false, err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(path)
			if readErr == nil && bytes.Equal(existing, data) {
				return false, nil
			}
			if readErr != nil {
				return false, readErr
			}
			return false, fmt.Errorf("refusing to overwrite conflicting file %s", path)
		}
		return false, err
	}
	return true, nil
}
