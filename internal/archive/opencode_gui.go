package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func resolveOpenCodeGUIHome(value string) (string, error) {
	if value != "" {
		return filepath.Abs(value)
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" {
		root = os.Getenv("XDG_DATA_HOME")
		if root == "" {
			home, e := os.UserHomeDir()
			if e != nil {
				return "", e
			}
			root = filepath.Join(home, ".local", "share")
		}
	}
	return filepath.Join(root, "ai.opencode.desktop"), nil
}

// ReadOpenCodeGUIIDs mirrors Desktop 1.18.34's local project registry and
// isRootVisibleSession: exact directory, no parent, no archived time. It does
// not infer creation origin or mistake closed-tab history for list membership.
func ReadOpenCodeGUIIDs(ctx context.Context, database, home string) (map[string]bool, error) {
	path := filepath.Join(home, "opencode.global.dat")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("OpenCode GUI project inventory unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 8*1024*1024 {
		return nil, fmt.Errorf("invalid OpenCode GUI inventory file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var global map[string]json.RawMessage
	if err := json.Unmarshal(raw, &global); err != nil {
		return nil, fmt.Errorf("parse OpenCode GUI inventory: %w", err)
	}
	server := global["server"]
	var encoded string
	if json.Unmarshal(server, &encoded) == nil {
		server = []byte(encoded)
	}
	var state struct {
		Projects map[string][]struct {
			Worktree string `json:"worktree"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(server, &state); err != nil || state.Projects == nil {
		return nil, fmt.Errorf("unsupported OpenCode GUI project inventory")
	}
	projects, ok := state.Projects["local"]
	if !ok {
		return nil, fmt.Errorf("OpenCode GUI local project registry unavailable")
	}
	directories := map[string]bool{}
	key := func(value string) string {
		value = filepath.Clean(value)
		if runtime.GOOS == "windows" {
			value = strings.ToLower(value)
		}
		return value
	}
	for _, project := range projects {
		if !filepath.IsAbs(project.Worktree) {
			return nil, fmt.Errorf("invalid OpenCode GUI project directory")
		}
		directories[key(project.Worktree)] = true
	}
	ids := map[string]bool{}
	if len(directories) == 0 {
		return ids, nil
	}
	info, err = os.Lstat(database)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("OpenCode GUI session database unavailable or not regular")
	}
	abs, err := filepath.Abs(database)
	if err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, directory FROM session WHERE (parent_id IS NULL OR parent_id = '') AND (time_archived IS NULL OR time_archived = 0)`)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode GUI sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, directory string
		if err := rows.Scan(&id, &directory); err != nil {
			return nil, err
		}
		if directories[key(directory)] {
			ids[id] = true
		}
	}
	return ids, rows.Err()
}
