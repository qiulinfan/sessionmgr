package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// zedClaudeAgentID is the agent_id Zed records for threads of its Claude Agent
// (the registry agent claude-acp, which runs the Claude Agent SDK); zedOMPAgentID
// is the name of the custom agent server that starts OMP over ACP.
const (
	zedClaudeAgentID = "claude-acp"
	zedOMPAgentID    = "OMP"
)

func resolveZedDB(value string) (string, error) {
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
		root = filepath.Join(home, "Library", "Application Support", "Zed")
	case "windows":
		root = filepath.Join(os.Getenv("LOCALAPPDATA"), "Zed")
	default:
		root = os.Getenv("XDG_DATA_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			root = filepath.Join(home, ".local", "share")
		}
		root = filepath.Join(root, "zed")
	}
	return filepath.Join(root, "db", "0-stable", "db.sqlite"), nil
}

// readZedSidebarIDs reads the sessions of one agent that Zed's thread sidebar
// lists and has not archived. Claude Agent transcripts carry the SDK entrypoint
// and OMP sessions stay on disk after Zed deletes their thread, so the sidebar,
// not the session file, establishes that a person holds them in the editor. A
// missing database means Zed is not installed; an unreadable one fails closed.
func readZedSidebarIDs(ctx context.Context, path, agentID string) (map[string]bool, error) {
	ids := make(map[string]bool)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Zed thread database is not a regular file")
	}
	db, err := sql.Open("sqlite", readOnlySQLiteURI(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT session_id FROM sidebar_threads WHERE agent_id = ? AND session_id IS NOT NULL AND session_id <> '' AND archived = 0", agentID)
	if err != nil {
		return nil, fmt.Errorf("read Zed thread sidebar: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}
