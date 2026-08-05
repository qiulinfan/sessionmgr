package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

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
	if strings.TrimSpace(opts.DeviceID) == "" || strings.TrimSpace(opts.DeviceName) == "" {
		return result, fmt.Errorf("device identity is required")
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return result, err
	}
	result.Output = output
	if err := os.MkdirAll(output, 0o755); err != nil {
		return result, err
	}
	existing, err := List(ListOptions{Output: output, History: true})
	if err != nil {
		return result, fmt.Errorf("inspect existing archive: %w", err)
	}
	history := make(map[string][]Entry)
	for _, entry := range existing {
		key := entryIdentity(entry)
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
	window := opts.StabilityWindow
	if window == 0 {
		window = defaultStabilityWindow
	} else if window < 0 {
		window = 0
	}
	stable, busy, observationIssues, observeErr := observeStableSources(ctx, files, window)
	if observeErr != nil {
		return result, observeErr
	}
	result.Busy += busy
	for _, issue := range observationIssues {
		result.Skipped++
		result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", filepath.Base(issue.path), issue.err))
	}
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
		if opts.SessionID != "" && session.ID != opts.SessionID {
			continue
		}
		if session.ExcludeReason != "" {
			result.FilteredInternal++
			continue
		}
		if session.UserMessages == 0 {
			// Context-only startup records are not conversations. Keep the raw
			// Codex source untouched and silently leave it out of the archive.
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
		session = prepareSessionAttachments(ctx, session, repo)
		snapshot := makeSnapshot(repo, session, opts.DeviceID, opts.DeviceName)
		key := repo.Key + "\x00" + snapshot.SessionKey
		created, snapshotPath, documentHash, publishErr := publishSnapshot(output, &snapshot, history[key])
		if publishErr != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("session %s: %v", session.ID, publishErr))
			continue
		}
		attachments, archivedFiles := attachmentCounts(snapshot.Session)
		result.Attachments += attachments
		result.ArchivedAttachments += archivedFiles
		if created {
			result.Created++
			for _, warning := range attachmentWarnings(snapshot.Session) {
				result.Warnings = append(result.Warnings, fmt.Sprintf("session %s: %s", session.ID, warning))
			}
			kind := changeKind(history[key], snapshot)
			change := Change{
				Kind: kind, RepositoryKey: repo.Key, RepositoryName: repo.Name,
				DeviceName: snapshot.DeviceName, SessionID: snapshot.Session.ID,
				SessionKey: snapshot.SessionKey, Title: snapshot.Session.Title,
				DocumentHash: documentHash, SourceHash: snapshot.Session.RawHash,
				UpdatedAt: formatTime(snapshot.SourceUpdate), Path: snapshotPath,
				Attachments: attachments, ArchivedFiles: archivedFiles,
			}
			result.Changes = append(result.Changes, change)
			history[key] = append(history[key], Entry{
				RepositoryKey: repo.Key, RepositoryName: repo.Name, SessionID: snapshot.Session.ID,
				DeviceID: snapshot.DeviceID, DeviceName: snapshot.DeviceName, SessionKey: snapshot.SessionKey,
				Title: snapshot.Session.Title, DocumentHash: documentHash,
				SourceHash: snapshot.Session.RawHash, UpdatedAt: formatTime(snapshot.SourceUpdate), Path: snapshotPath,
			})
		} else {
			result.Unchanged++
		}
	}
	if opts.SessionID != "" && result.Matched == 0 && result.Busy == 0 && result.FilteredInternal == 0 {
		return result, fmt.Errorf("Codex session %q was not found for the selected repository scope", opts.SessionID)
	}
	if result.Skipped > 0 {
		sortChanges(result.Changes)
		return result, fmt.Errorf("export completed with %d skipped session source(s)", result.Skipped)
	}
	sortChanges(result.Changes)
	return result, nil
}

func publishSnapshot(output string, snapshot *Snapshot, history []Entry) (bool, string, string, error) {
	repositoryDir := filepath.Join(output, semanticRepositoryDirectory(snapshot.Repository))
	if err := publishRepositoryMetadata(repositoryDir, snapshot.Repository); err != nil {
		return false, "", "", fmt.Errorf("publish repository identity: %w", err)
	}
	deviceDir := semanticComponent(snapshot.DeviceName, "device")
	desiredDir := filepath.Join(repositoryDir, deviceDir, semanticSessionDirectory(*snapshot))

	if len(history) > 0 {
		latest := history[0]
		for _, candidate := range history[1:] {
			if newerEntry(candidate, latest) {
				latest = candidate
			}
		}
		var current sessionMetadata
		if err := readMetadata(filepath.Join(filepath.Dir(latest.Path), sessionMetadataName), &current); err != nil {
			return false, "", "", fmt.Errorf("read existing session metadata: %w", err)
		}
		if err := validateSessionMetadata(current); err != nil {
			return false, "", "", err
		}
		retainPublishedAttachments(&snapshot.Session, current)
		document := renderSnapshot(*snapshot)
		documentHash := digestBytes(document)
		record := sessionRecord(*snapshot, documentHash)
		return updatePublishedSession(latest, desiredDir, document, record, snapshot.Session)
	}

	metadataPath := filepath.Join(desiredDir, sessionMetadataName)
	if _, err := os.Lstat(desiredDir); err == nil {
		var current sessionMetadata
		if readErr := readMetadata(metadataPath, &current); readErr == nil {
			if err := validateSessionMetadata(current); err != nil {
				return false, "", "", err
			}
			if current.RepositoryKey != snapshot.Repository.Key || current.SessionKey != snapshot.SessionKey {
				return false, "", "", fmt.Errorf("semantic session path belongs to a different identity: %s", desiredDir)
			}
			retainPublishedAttachments(&snapshot.Session, current)
			document := renderSnapshot(*snapshot)
			documentHash := digestBytes(document)
			record := sessionRecord(*snapshot, documentHash)
			return updatePublishedSession(entryFromMetadata(current, filepath.Join(desiredDir, conversationName)), desiredDir, document, record, snapshot.Session)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return false, "", "", fmt.Errorf("read existing session metadata: %w", readErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, "", "", err
	}
	document := renderSnapshot(*snapshot)
	documentHash := digestBytes(document)
	record := sessionRecord(*snapshot, documentHash)
	if _, err := os.Lstat(desiredDir); err == nil {
		if err := verifyClaimableSessionDirectory(desiredDir, document, snapshot.Session); err != nil {
			return false, "", "", err
		}
	}
	if err := os.MkdirAll(desiredDir, 0o755); err != nil {
		return false, "", "", err
	}
	if _, err := publishAttachmentFiles(desiredDir, snapshot.Session, nil); err != nil {
		return false, "", "", err
	}
	documentPath := filepath.Join(desiredDir, conversationName)
	if _, err := publishImmutable(documentPath, document); err != nil {
		return false, "", "", err
	}
	metadata, err := marshalMetadata(record)
	if err != nil {
		return false, "", "", err
	}
	if _, err := publishImmutable(metadataPath, metadata); err != nil {
		return false, "", "", err
	}
	return true, documentPath, documentHash, nil
}

func updatePublishedSession(previous Entry, desiredDir string, document []byte, record sessionMetadata, session Session) (bool, string, string, error) {
	oldDocumentPath := previous.Path
	oldDir := filepath.Dir(oldDocumentPath)
	previousDir := oldDir
	var current sessionMetadata
	if err := readMetadata(filepath.Join(oldDir, sessionMetadataName), &current); err != nil {
		return false, "", "", fmt.Errorf("read existing session metadata: %w", err)
	}
	if err := validateSessionMetadata(current); err != nil {
		return false, "", "", err
	}
	if current.RepositoryKey != record.RepositoryKey || current.SessionKey != record.SessionKey ||
		current.DeviceID != record.DeviceID || current.SessionID != record.SessionID {
		return false, "", "", fmt.Errorf("existing session metadata belongs to a different identity: %s", oldDir)
	}
	if err := verifyOwnedAttachments(oldDir, current, record); err != nil {
		return false, "", "", err
	}
	existing, err := readOwnedDocument(oldDocumentPath)
	if err != nil {
		return false, "", "", err
	}
	actualHash := digestBytes(existing)
	newHash := digestBytes(document)
	if actualHash != current.DocumentHash && actualHash != newHash {
		return false, "", "", fmt.Errorf("refusing to overwrite a modified session document: %s", oldDocumentPath)
	}

	dirChanged := filepath.Clean(oldDir) != filepath.Clean(desiredDir)
	contentChanged := actualHash != newHash
	metadataChanged := !reflect.DeepEqual(current, record)
	if !dirChanged && !contentChanged && !metadataChanged {
		return false, oldDocumentPath, newHash, nil
	}
	if dirChanged {
		if _, err := os.Lstat(desiredDir); err == nil {
			return false, "", "", fmt.Errorf("refusing to overwrite conflicting semantic session directory: %s", desiredDir)
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, "", "", err
		}
		if err := os.MkdirAll(filepath.Dir(desiredDir), 0o755); err != nil {
			return false, "", "", err
		}
		if err := os.Rename(oldDir, desiredDir); err != nil {
			return false, "", "", fmt.Errorf("rename semantic session directory: %w", err)
		}
		oldDocumentPath = filepath.Join(desiredDir, conversationName)
	}
	attachmentsChanged, err := publishAttachmentFiles(filepath.Dir(oldDocumentPath), session, &current)
	if err != nil {
		return false, "", "", err
	}
	if contentChanged {
		if err := replaceOwnedFile(oldDocumentPath, document); err != nil {
			return false, "", "", err
		}
	}
	metadata, err := marshalMetadata(record)
	if err != nil {
		return false, "", "", err
	}
	if err := replaceOwnedFile(filepath.Join(filepath.Dir(oldDocumentPath), sessionMetadataName), metadata); err != nil {
		return false, "", "", err
	}
	if dirChanged {
		removeEmptyLegacySessionParents(previousDir)
	}
	return dirChanged || contentChanged || metadataChanged || attachmentsChanged, oldDocumentPath, newHash, nil
}

func removeEmptyLegacySessionParents(sessionDir string) {
	deviceDir := filepath.Dir(sessionDir)
	sessionsDir := filepath.Dir(deviceDir)
	if filepath.Base(sessionsDir) != "sessions" {
		return
	}
	// os.Remove only succeeds for empty directories, so user files or another
	// session always keep both legacy parents in place.
	if err := os.Remove(deviceDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	_ = os.Remove(sessionsDir)
}

func retainPublishedAttachments(session *Session, current sessionMetadata) {
	previous := make(map[string]attachmentMetadata, len(current.Attachments))
	for _, attachment := range current.Attachments {
		previous[attachmentIdentity(attachment.MessageIndex, attachment.AttachmentIndex)] = attachment
	}
	for messageIndex := range session.Messages {
		for attachmentIndex := range session.Messages[messageIndex].Attachments {
			attachment := &session.Messages[messageIndex].Attachments[attachmentIndex]
			old, ok := previous[attachmentIdentity(attachment.MessageIndex, attachment.AttachmentIndex)]
			if !ok || (old.Status != attachmentStatusArchived && old.Status != attachmentStatusGitTracked) {
				continue
			}
			if attachment.SourceKind != "local_path" && attachment.Status != attachmentStatusBusy && attachment.Status != attachmentStatusUnavailable {
				continue
			}
			sourceValue, localPath := attachment.SourceValue, attachment.LocalPath
			*attachment = attachmentFromMetadata(old)
			attachment.SourceValue, attachment.LocalPath = sourceValue, localPath
		}
	}
}

func attachmentIdentity(messageIndex, attachmentIndex int) string {
	return fmt.Sprintf("%d/%d", messageIndex, attachmentIndex)
}

func attachmentFromMetadata(value attachmentMetadata) Attachment {
	return Attachment{
		MessageIndex: value.MessageIndex, AttachmentIndex: value.AttachmentIndex,
		Name: value.Name, MIMEType: value.MIMEType, SourceKind: value.SourceKind,
		Status: value.Status, ArchivePath: value.ArchivePath,
		RepositoryPath: value.RepositoryPath, GitCommit: value.GitCommit,
		Size: value.Size, ContentHash: value.ContentHash,
	}
}

func verifyOwnedAttachments(directory string, metadata, desired sessionMetadata) error {
	desiredByPath := make(map[string]attachmentMetadata, len(desired.Attachments))
	for _, attachment := range desired.Attachments {
		if attachment.Status == attachmentStatusArchived {
			desiredByPath[attachment.ArchivePath] = attachment
		}
	}
	for _, attachment := range metadata.Attachments {
		if attachment.Status != attachmentStatusArchived {
			continue
		}
		path := filepath.Join(directory, filepath.FromSlash(attachment.ArchivePath))
		data, err := readRegularFileNoSymlink(path)
		if err != nil {
			return fmt.Errorf("read archived attachment %q: %w", attachment.Name, err)
		}
		actualHash := digestBytes(data)
		matchesCurrent := int64(len(data)) == attachment.Size && actualHash == attachment.ContentHash
		wanted, hasDesired := desiredByPath[attachment.ArchivePath]
		matchesDesired := hasDesired && int64(len(data)) == wanted.Size && actualHash == wanted.ContentHash
		if !matchesCurrent && !matchesDesired {
			return fmt.Errorf("refusing to overwrite a modified archived attachment: %s", path)
		}
	}
	return nil
}

func publishAttachmentFiles(directory string, session Session, current *sessionMetadata) (bool, error) {
	owned := make(map[string]attachmentMetadata)
	if current != nil {
		for _, attachment := range current.Attachments {
			if attachment.Status == attachmentStatusArchived {
				owned[attachment.ArchivePath] = attachment
			}
		}
	}
	changed := false
	attachmentDirectoryReady := false
	for _, message := range session.Messages {
		for _, attachment := range message.Attachments {
			if attachment.Status != attachmentStatusArchived {
				continue
			}
			if !attachmentDirectoryReady {
				if err := ensureAttachmentDirectory(directory); err != nil {
					return false, err
				}
				attachmentDirectoryReady = true
			}
			path := filepath.Join(directory, filepath.FromSlash(attachment.ArchivePath))
			if len(attachment.Data) == 0 && attachment.Size != 0 {
				data, err := readRegularFileNoSymlink(path)
				if err != nil || digestBytes(data) != attachment.ContentHash {
					return false, fmt.Errorf("archived attachment %q is missing and its source is unavailable", attachment.Name)
				}
				continue
			}
			existing, err := readRegularFileNoSymlink(path)
			if err == nil {
				if bytes.Equal(existing, attachment.Data) {
					continue
				}
				previous, isOwned := owned[attachment.ArchivePath]
				if !isOwned || digestBytes(existing) != previous.ContentHash {
					return false, fmt.Errorf("refusing to overwrite conflicting attachment: %s", path)
				}
				if err := replaceOwnedFile(path, attachment.Data); err != nil {
					return false, err
				}
				changed = true
				continue
			}
			if !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			created, err := publishImmutable(path, attachment.Data)
			if err != nil {
				return false, err
			}
			changed = changed || created
		}
	}
	return changed, nil
}

func ensureAttachmentDirectory(sessionDirectory string) error {
	directory := filepath.Join(sessionDirectory, "attachments")
	if info, err := os.Lstat(directory); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("attachment path is not an owned directory: %s", directory)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(directory, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("attachment path is not an owned directory: %s", directory)
	}
	return nil
}

func verifyClaimableSessionDirectory(directory string, document []byte, session Session) error {
	desired := map[string][]byte{conversationName: document}
	for _, message := range session.Messages {
		for _, attachment := range message.Attachments {
			if attachment.Status == attachmentStatusArchived {
				desired[attachment.ArchivePath] = attachment.Data
			}
		}
	}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == directory {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative == "attachments" {
				return nil
			}
			return fmt.Errorf("refusing to claim existing semantic session directory: %s", directory)
		}
		want, ok := desired[relative]
		if !ok {
			return fmt.Errorf("refusing to claim existing semantic session directory: %s", directory)
		}
		actual, err := readRegularFileNoSymlink(path)
		if err != nil || !bytes.Equal(actual, want) {
			return fmt.Errorf("refusing to claim conflicting session file: %s", path)
		}
		return nil
	})
	return err
}

func entryFromMetadata(value sessionMetadata, path string) Entry {
	return Entry{
		RepositoryKey: value.RepositoryKey, RepositoryName: value.RepositoryName,
		DeviceID: value.DeviceID, DeviceName: value.DeviceName,
		SessionID: value.SessionID, SessionKey: value.SessionKey, Title: value.Title,
		DocumentHash: value.DocumentHash, SourceHash: value.SourceHash,
		UpdatedAt: value.UpdatedAt, Versions: 1, Path: path,
	}
}

func readOwnedDocument(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("session document is not a regular file: %s", path)
	}
	return os.ReadFile(path)
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
	if latest.SourceHash == snapshot.Session.RawHash &&
		(latest.Title != snapshot.Session.Title || latest.DeviceName != snapshot.DeviceName) {
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
	if existing, err := readRegularFileNoSymlink(path); err == nil {
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
			existing, readErr := readRegularFileNoSymlink(path)
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

func readRegularFileNoSymlink(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular file: %s", path)
	}
	return os.ReadFile(path)
}
