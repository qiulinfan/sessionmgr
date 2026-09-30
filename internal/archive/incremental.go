package archive

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const exportStateSchema = 1
const exportOverlap = time.Hour

// GUI exports share one process. Serialize checkpoint-backed exports so a
// later scan cannot replace a pending retry recorded by an earlier one.
var checkpointExportMu sync.Mutex

type exportState struct {
	SchemaVersion int                         `json:"schema_version"`
	Scopes        map[string]exportCheckpoint `json:"scopes"`
}

type exportCheckpoint struct {
	Output       string                       `json:"output"`
	LastExportAt time.Time                    `json:"last_export_at"`
	Sources      map[string]exportSourceState `json:"sources"`
}

type exportSourceState struct {
	Harness    string    `json:"harness"`
	SessionID  string    `json:"session_id,omitempty"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	TitleHash  string    `json:"title_hash,omitempty"`
	Pending    bool      `json:"pending"`
}

type incrementalExport struct {
	path      string
	key       string
	state     exportState
	previous  exportCheckpoint
	next      exportCheckpoint
	startedAt time.Time
	since     time.Time
	full      bool
	retry     map[string]bool
	settled   map[string]bool
	parsed    map[string]bool
	blocked   map[string]bool
}

func loadExportState(path string) (exportState, error) {
	state := exportState{SchemaVersion: exportStateSchema, Scopes: make(map[string]exportCheckpoint)}
	if path == "" {
		return state, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("read local export checkpoint: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return state, fmt.Errorf("local export checkpoint contains trailing data")
	}
	if state.SchemaVersion != exportStateSchema || state.Scopes == nil {
		return state, fmt.Errorf("unsupported local export checkpoint schema %d", state.SchemaVersion)
	}
	return state, nil
}

func beginIncrementalExport(opts Options, selected SourceSelection, target Repository, startedAt time.Time) (*incrementalExport, error) {
	state, err := loadExportState(opts.CheckpointPath)
	if err != nil {
		return nil, err
	}
	// Every selection that can change which sessions are eligible has its own
	// checkpoint. Enabling a source, archived sessions, or non-Git inclusion
	// therefore starts a full discovery instead of hiding older conversations.
	scope, err := json.Marshal(struct {
		Generation                                                     int
		Output, DeviceID, DeviceName, Target, SessionID                string
		CodexHome, ClaudeHome, DeepSeekHome, OMPSessionDir, OpenCodeDB string
		Sources                                                        SourceSelection
		AllRepos, IncludeArchived, IncludeNonGit                       bool
	}{
		Generation: 1, Output: opts.Output, DeviceID: opts.DeviceID, DeviceName: opts.DeviceName,
		Target: target.Key, SessionID: opts.SessionID, CodexHome: opts.CodexHome,
		ClaudeHome: opts.ClaudeHome, DeepSeekHome: opts.DeepSeekHome,
		OMPSessionDir: opts.OMPSessionDir, OpenCodeDB: opts.OpenCodeDB, Sources: selected,
		AllRepos: opts.AllRepos, IncludeArchived: opts.IncludeArchived, IncludeNonGit: opts.IncludeNonGit,
	})
	if err != nil {
		return nil, err
	}
	key := digestBytes(scope)
	previous := state.Scopes[key]
	run := &incrementalExport{
		path: opts.CheckpointPath, key: key, state: state, previous: previous,
		startedAt: startedAt, full: opts.FullScan || opts.CheckpointPath == "",
		next:  exportCheckpoint{Output: opts.Output, Sources: make(map[string]exportSourceState)},
		retry: make(map[string]bool), settled: make(map[string]bool),
		parsed: make(map[string]bool), blocked: make(map[string]bool),
	}
	if !run.full && !previous.LastExportAt.IsZero() && !previous.LastExportAt.After(startedAt) {
		run.since = previous.LastExportAt.Add(-exportOverlap)
	}
	return run, nil
}

func sourceStateKey(harness, path string) string {
	return digest(harness + "\x00" + filepath.Clean(path))
}

func logicalSourceKey(harness, id string) string { return harness + "\x00" + id }

func titleStateHash(title titleRecord) string {
	return digest(title.Title + "\x00" + formatTime(title.UpdatedAt))
}

func (run *incrementalExport) filterSources(sources []nativeSessionSource, titles map[string]titleRecord) []nativeSessionSource {
	eligible := make(map[string]bool)
	groups := make(map[string]bool)
	for _, source := range sources {
		key := sourceStateKey(source.harness, source.path)
		old, known := run.previous.Sources[key]
		next := old
		next.Harness = source.harness
		info, err := os.Lstat(source.path)
		if err == nil {
			next.Size, next.ModifiedAt = info.Size(), info.ModTime().UTC()
		}
		if source.harness == harnessCodex && next.SessionID != "" {
			next.TitleHash = titleStateHash(titles[next.SessionID])
		}
		selected := run.since.IsZero() || !known || old.Pending || err != nil ||
			!next.ModifiedAt.Before(run.since) || next.Size != old.Size ||
			!next.ModifiedAt.Equal(old.ModifiedAt) || next.TitleHash != old.TitleHash
		if selected {
			if source.harness == harnessCodex && err == nil && info.Mode().IsRegular() {
				if id := peekCodexSessionID(source.path); id != "" {
					next.SessionID = id
				}
			} else if source.harness == harnessClaudeCode {
				next.SessionID = strings.TrimSuffix(filepath.Base(source.path), filepath.Ext(source.path))
			}
			eligible[key] = true
			if next.SessionID != "" {
				groups[logicalSourceKey(source.harness, next.SessionID)] = true
			}
		}
		run.next.Sources[key] = next
	}
	filtered := make([]nativeSessionSource, 0, len(sources))
	for _, source := range sources {
		key := sourceStateKey(source.harness, source.path)
		next := run.next.Sources[key]
		// The archive hashes and renders logical Codex/Claude conversations.
		// Read every fragment/copy if any member changed, including old members
		// outside the overlap window.
		if eligible[key] || (next.SessionID != "" && groups[logicalSourceKey(source.harness, next.SessionID)]) {
			next.Pending = true
			run.next.Sources[key] = next
			filtered = append(filtered, source)
		}
	}
	return filtered
}

func peekCodexSessionID(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 1024*1024))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record.Type == "session_meta" {
			return firstString(record.Payload.ID, record.Payload.SessionID)
		}
	}
	return ""
}

func (run *incrementalExport) noteParsed(source nativeSessionSource, session Session, titles map[string]titleRecord) {
	key := sourceStateKey(source.harness, source.path)
	next := run.next.Sources[key]
	next.SessionID = session.ID
	if source.harness == harnessCodex {
		next.TitleHash = titleStateHash(titles[session.ID])
	}
	run.next.Sources[key] = next
	run.parsed[key] = true
}

func (run *incrementalExport) selectOpenCode(path string, header openCodeSessionRow) bool {
	key := sourceStateKey(harnessOpenCode, path+"\x00"+header.id)
	old, known := run.previous.Sources[key]
	metadata, _ := json.Marshal([]any{header.directory, header.title, header.version, header.parentID, header.created, header.updated})
	next := exportSourceState{
		Harness: harnessOpenCode, SessionID: header.id,
		ModifiedAt: time.UnixMilli(header.modified).UTC(), TitleHash: digestBytes(metadata),
	}
	selected := run.since.IsZero() || !known || old.Pending || !next.ModifiedAt.Before(run.since) ||
		!next.ModifiedAt.Equal(old.ModifiedAt) || next.TitleHash != old.TitleHash
	next.Pending = selected
	run.next.Sources[key] = next
	return selected
}

func (run *incrementalExport) settle(harness, id string) {
	run.settled[logicalSourceKey(harness, id)] = true
}

func (run *incrementalExport) failedSource(source nativeSessionSource) {
	state := run.next.Sources[sourceStateKey(source.harness, source.path)]
	if state.SessionID != "" {
		group := logicalSourceKey(source.harness, state.SessionID)
		run.retry[group] = true
		run.blocked[group] = true
	}
}

func (run *incrementalExport) ignoredSession(id string) bool {
	for _, source := range run.next.Sources {
		if source.SessionID == id && !source.Pending {
			return true
		}
	}
	return false
}

func (run *incrementalExport) save() error {
	if run.path == "" {
		return nil
	}
	for key, source := range run.next.Sources {
		group := logicalSourceKey(source.Harness, source.SessionID)
		if run.settled[group] && !run.retry[group] && run.parsed[key] {
			source.Pending = false
			run.next.Sources[key] = source
		}
	}
	// Save the scan's start, not its end. Updates arriving during a long export
	// remain eligible on the next invocation. Pending failures survive even
	// when the rest of this changeset was published successfully.
	run.next.LastExportAt = run.startedAt
	run.state.Scopes[run.key] = run.next
	data, err := json.MarshalIndent(run.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(run.path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(run.path), ".export-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), run.path)
}

// LastExportTime reports local scan history for the chosen archive directory.
func LastExportTime(path, output string) (time.Time, error) {
	state, err := loadExportState(path)
	if err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	for _, scope := range state.Scopes {
		if filepath.Clean(scope.Output) == filepath.Clean(output) && scope.LastExportAt.After(latest) {
			latest = scope.LastExportAt
		}
	}
	return latest, nil
}
