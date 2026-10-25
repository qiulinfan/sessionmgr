package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// vscodeClaudeStateKey is the global-state entry of the Claude Code extension in
// VS Code's state database; its hiddenSessionIds are the side-panel sessions the
// person archived. A Remote-WSL window keeps the extension's global state in the
// Windows database too.
const vscodeClaudeStateKey = "Anthropic.claude-code"

func resolveVSCodeState(value string) (string, error) {
	if value != "" {
		return filepath.Abs(value)
	}
	var root string
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, "Library", "Application Support")
	case "windows":
		root = os.Getenv("APPDATA")
	default:
		root = os.Getenv("XDG_CONFIG_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			root = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(root, "Code", "User", "globalStorage", "state.vscdb"), nil
}

// readVSCodeArchivedIDs reads the Claude Code side-panel sessions archived in
// VS Code. A missing database or entry archives nothing; an unreadable one fails
// closed.
func readVSCodeArchivedIDs(ctx context.Context, path string) (map[string]bool, error) {
	ids := make(map[string]bool)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("VS Code state database is not a regular file")
	}
	db, err := sql.Open("sqlite", readOnlySQLiteURI(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var raw string
	err = db.QueryRowContext(ctx, "SELECT value FROM ItemTable WHERE key = ?", vscodeClaudeStateKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ids, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read VS Code state: %w", err)
	}
	var state struct {
		HiddenSessionIDs []string `json:"hiddenSessionIds"`
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("read VS Code Claude Code state: %w", err)
	}
	for _, id := range state.HiddenSessionIDs {
		ids[id] = true
	}
	return ids, nil
}
