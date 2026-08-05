package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const cleanupDirectoryName = ".sessionmgr-cleanup"

// CleanupInternal removes only current, machine-owned archive documents whose
// still-present Codex source proves that they are internal subagent or runtime-
// context sessions. It is a dry run unless Apply is true. Missing source files
// are never interpreted as cleanup instructions.
func CleanupInternal(ctx context.Context, opts CleanupOptions) (CleanupResult, error) {
	result := CleanupResult{SchemaVersion: SchemaVersion, DryRun: !opts.Apply, Changes: []CleanupChange{}}
	if opts.CodexHome == "" {
		var err error
		opts.CodexHome, err = DefaultCodexHome()
		if err != nil {
			return result, err
		}
	}
	if strings.TrimSpace(opts.Output) == "" {
		return result, fmt.Errorf("archive directory is required")
	}
	if strings.TrimSpace(opts.DeviceID) == "" {
		return result, fmt.Errorf("device identity is required")
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return result, err
	}
	result.Output = output

	entries, err := List(ListOptions{Output: output})
	if err != nil {
		return result, fmt.Errorf("inspect existing archive: %w", err)
	}
	bySessionID := make(map[string][]Entry)
	for _, entry := range entries {
		if entry.Legacy || entry.DeviceID != opts.DeviceID {
			continue
		}
		bySessionID[entry.SessionID] = append(bySessionID[entry.SessionID], entry)
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
	window := opts.StabilityWindow
	if window == 0 {
		window = defaultStabilityWindow
	} else if window < 0 {
		window = 0
	}
	stable, busy, observationIssues, err := observeStableSources(ctx, files, window)
	if err != nil {
		return result, err
	}
	result.Busy = busy
	for _, issue := range observationIssues {
		result.Skipped++
		result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", filepath.Base(issue.path), issue.err))
	}

	processed := make(map[string]bool)
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		expected, ok := stable[path]
		if !ok {
			continue
		}
		raw, readErr := readObservedSource(ctx, path, expected)
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
				return result, readErr
			}
			if sourceErrorIsBusy(readErr) {
				result.Busy++
				continue
			}
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
		if processed[session.ID] || session.ExcludeReason == "" {
			continue
		}
		processed[session.ID] = true
		candidates := bySessionID[session.ID]
		if len(candidates) == 0 {
			continue
		}
		repository, repoErr := repositoryForSession(ctx, session)
		if repoErr != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("session %s: prove repository identity: %v", session.ID, repoErr))
			continue
		}
		for _, entry := range candidates {
			if entry.RepositoryKey != repository.Key {
				result.Skipped++
				result.Warnings = append(result.Warnings, fmt.Sprintf("session %s: archive repository identity does not match its source", session.ID))
				continue
			}
			metadata, verifyErr := verifyCleanupCandidate(entry, opts.DeviceID, session, repository)
			if verifyErr != nil {
				result.Skipped++
				result.Warnings = append(result.Warnings, fmt.Sprintf("session %s: %v", session.ID, verifyErr))
				continue
			}
			change := CleanupChange{
				Kind: "remove", Reason: session.ExcludeReason,
				RepositoryName: entry.RepositoryName, DeviceName: entry.DeviceName,
				SessionID: entry.SessionID, SessionKey: entry.SessionKey,
				Title: entry.Title, Path: entry.Path,
			}
			result.Candidates++
			if opts.Apply {
				if err := removeCleanupCandidate(output, filepath.Dir(entry.Path), metadata); err != nil {
					result.Skipped++
					result.Warnings = append(result.Warnings, fmt.Sprintf("session %s: %v", session.ID, err))
					continue
				}
				change.Kind = "removed"
				result.Removed++
			}
			result.Changes = append(result.Changes, change)
		}
	}
	sortCleanupChanges(result.Changes)
	if result.Skipped > 0 {
		return result, fmt.Errorf("cleanup completed with %d skipped item(s)", result.Skipped)
	}
	return result, nil
}

func verifyCleanupCandidate(entry Entry, deviceID string, session Session, repository Repository) (sessionMetadata, error) {
	directory := filepath.Dir(entry.Path)
	var metadata sessionMetadata
	if err := readMetadata(filepath.Join(directory, sessionMetadataName), &metadata); err != nil {
		return sessionMetadata{}, fmt.Errorf("read session metadata: %w", err)
	}
	if err := validateSessionMetadata(metadata); err != nil {
		return sessionMetadata{}, err
	}
	if metadata.DeviceID != deviceID || metadata.DeviceID != entry.DeviceID ||
		metadata.SessionID != session.ID || metadata.SessionID != entry.SessionID ||
		metadata.SessionKey != entry.SessionKey || metadata.RepositoryKey != repository.Key ||
		metadata.RepositoryKey != entry.RepositoryKey {
		return sessionMetadata{}, fmt.Errorf("cleanup identity does not match source and archive sidecar")
	}
	if err := verifyCleanupOwnedTree(directory, metadata); err != nil {
		return sessionMetadata{}, err
	}
	return metadata, nil
}

func verifyCleanupOwnedTree(directory string, metadata sessionMetadata) error {
	var onDisk sessionMetadata
	if err := readMetadata(filepath.Join(directory, sessionMetadataName), &onDisk); err != nil {
		return fmt.Errorf("read owned session metadata: %w", err)
	}
	if err := validateSessionMetadata(onDisk); err != nil {
		return err
	}
	if !reflect.DeepEqual(onDisk, metadata) {
		return fmt.Errorf("session metadata changed during cleanup verification")
	}
	document, err := readOwnedDocument(filepath.Join(directory, conversationName))
	if err != nil {
		return fmt.Errorf("read owned document: %w", err)
	}
	if digestBytes(document) != metadata.DocumentHash {
		return fmt.Errorf("refusing to remove a modified session document: %s", filepath.Join(directory, conversationName))
	}
	if err := verifyOwnedAttachments(directory, metadata, metadata); err != nil {
		return err
	}

	allowedFiles := map[string]bool{
		conversationName:    true,
		sessionMetadataName: true,
	}
	allowedDirectories := make(map[string]bool)
	for _, attachment := range metadata.Attachments {
		if attachment.Status != attachmentStatusArchived {
			continue
		}
		allowedFiles[attachment.ArchivePath] = true
		parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(attachment.ArchivePath)))
		for parent != "." && parent != "" {
			allowedDirectories[parent] = true
			parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent)))
		}
	}
	return filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == directory {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to remove a session tree containing a symlink: %s", path)
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if !allowedDirectories[relative] {
				return fmt.Errorf("refusing to remove a session tree containing an unowned directory: %s", path)
			}
			return nil
		}
		if !allowedFiles[relative] {
			return fmt.Errorf("refusing to remove a session tree containing an unowned file: %s", path)
		}
		return nil
	})
}

func removeCleanupCandidate(output, directory string, metadata sessionMetadata) error {
	relative, err := filepath.Rel(output, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("cleanup target is outside the archive directory: %s", directory)
	}
	cleanupRoot := filepath.Join(output, cleanupDirectoryName)
	if err := ensureCleanupDirectory(cleanupRoot); err != nil {
		return err
	}
	trash, err := os.MkdirTemp(cleanupRoot, "session-")
	if err != nil {
		return err
	}
	if err := os.Remove(trash); err != nil {
		return err
	}
	if err := os.Rename(directory, trash); err != nil {
		return fmt.Errorf("move cleanup candidate to recovery directory: %w", err)
	}
	if err := verifyCleanupOwnedTree(trash, metadata); err != nil {
		if restoreErr := os.Rename(trash, directory); restoreErr != nil {
			return fmt.Errorf("reverify moved cleanup candidate: %v; recovery copy remains at %s: %v", err, trash, restoreErr)
		}
		return fmt.Errorf("reverify moved cleanup candidate: %w", err)
	}
	if err := removeCleanupTree(trash, metadata); err != nil {
		return fmt.Errorf("remove verified cleanup candidate; recoverable remainder is at %s: %w", trash, err)
	}
	_ = os.Remove(filepath.Dir(directory))
	_ = os.Remove(cleanupRoot)
	return nil
}

func ensureCleanupDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("cleanup recovery path is not an owned directory: %s", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func removeCleanupTree(directory string, metadata sessionMetadata) error {
	for _, attachment := range metadata.Attachments {
		if attachment.Status != attachmentStatusArchived {
			continue
		}
		if err := os.Remove(filepath.Join(directory, filepath.FromSlash(attachment.ArchivePath))); err != nil {
			return err
		}
	}
	var directories []string
	for _, attachment := range metadata.Attachments {
		if attachment.Status != attachmentStatusArchived {
			continue
		}
		parent := filepath.Dir(filepath.Join(directory, filepath.FromSlash(attachment.ArchivePath)))
		for parent != directory {
			directories = append(directories, parent)
			parent = filepath.Dir(parent)
		}
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, path := range directories {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(filepath.Join(directory, conversationName)); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(directory, sessionMetadataName)); err != nil {
		return err
	}
	return os.Remove(directory)
}

func sortCleanupChanges(changes []CleanupChange) {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].RepositoryName != changes[j].RepositoryName {
			return changes[i].RepositoryName < changes[j].RepositoryName
		}
		if changes[i].DeviceName != changes[j].DeviceName {
			return changes[i].DeviceName < changes[j].DeviceName
		}
		return changes[i].SessionID < changes[j].SessionID
	})
}
