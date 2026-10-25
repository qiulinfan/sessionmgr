package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// claudeDesktopSessionsDir is the folder of Claude Desktop's data folder that
// holds one local_*.json record per Claude Code session the app lists.
const claudeDesktopSessionsDir = "claude-code-sessions"

// claudeDesktopDataRoots are the places Claude Desktop names for its data folder:
// Claude-Data, Claude-3p or Claude, under %LOCALAPPDATA% or %APPDATA% on
// Windows, ~/Library/Application Support on macOS and ~/.config on Linux.
func claudeDesktopDataRoots() ([]string, error) {
	var parents []string
	switch runtime.GOOS {
	case "windows":
		for _, name := range []string{"LOCALAPPDATA", "APPDATA"} {
			if value := os.Getenv(name); value != "" {
				parents = append(parents, value)
			}
		}
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		parents = append(parents, filepath.Join(home, "Library", "Application Support"))
	default:
		root := os.Getenv("XDG_CONFIG_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			root = filepath.Join(home, ".config")
		}
		parents = append(parents, root)
	}
	var roots []string
	for _, parent := range parents {
		for _, name := range []string{"Claude-Data", "Claude-3p", "Claude"} {
			roots = append(roots, filepath.Join(parent, name))
		}
	}
	return roots, nil
}

// claudeDesktopSessionsDirs finds the session folders inside a data folder: at
// its top level, or one level down where the packaged Windows app keeps a
// claude_<package> folder.
func claudeDesktopSessionsDirs(root string) []string {
	var dirs []string
	if isDir(filepath.Join(root, claudeDesktopSessionsDir)) {
		dirs = append(dirs, filepath.Join(root, claudeDesktopSessionsDir))
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if entry.IsDir() && isDir(filepath.Join(root, entry.Name(), claudeDesktopSessionsDir)) {
			dirs = append(dirs, filepath.Join(root, entry.Name(), claudeDesktopSessionsDir))
		}
	}
	return dirs
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// readClaudeDesktopIDs reads the Claude Code transcript IDs of the sessions
// Claude Desktop lists and has not archived: each record's current CLI session
// and the ones it replaced. A CLI run started inside a Desktop session inherits
// its entrypoint, so this list, not the transcript, establishes that a person
// held the conversation in the app. value names the data folder; empty searches
// the folders the app uses. It also returns the session folders it read; none
// means Claude Desktop is not installed and no Desktop session is in scope.
func readClaudeDesktopIDs(value string) (map[string]bool, []string, error) {
	roots := []string{value}
	if value == "" {
		var err error
		if roots, err = claudeDesktopDataRoots(); err != nil {
			return nil, nil, err
		}
	}
	ids := make(map[string]bool)
	var dirs []string
	for _, root := range roots {
		for _, dir := range claudeDesktopSessionsDirs(root) {
			dirs = append(dirs, dir)
			err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				name := entry.Name()
				if entry.IsDir() || !strings.HasPrefix(name, "local_") || filepath.Ext(name) != ".json" {
					return nil
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				var record struct {
					CLISessionID       string   `json:"cliSessionId"`
					PriorCLISessionIDs []string `json:"priorCliSessionIds"`
					IsArchived         bool     `json:"isArchived"`
				}
				if err := json.Unmarshal(raw, &record); err != nil {
					return fmt.Errorf("read Claude Desktop session %s: %w", path, err)
				}
				if record.IsArchived {
					return nil
				}
				for _, id := range append([]string{record.CLISessionID}, record.PriorCLISessionIDs...) {
					if id != "" {
						ids[id] = true
					}
				}
				return nil
			})
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, nil, err
			}
		}
	}
	if value != "" && len(dirs) == 0 {
		return nil, nil, fmt.Errorf("no %s folder in Claude Desktop data folder %s", claudeDesktopSessionsDir, value)
	}
	return ids, dirs, nil
}
