package archive

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readCodexSidebarIDs reads Desktop root-thread membership, not title guesses.
// The latest native database must support this query; errors fail closed.
func readCodexSidebarIDs(ctx context.Context, home string) (map[string]bool, error) {
	paths, err := filepath.Glob(filepath.Join(home, "state_*.sqlite"))
	if err != nil {
		return nil, err
	}
	latest := ""
	generation := -1
	for _, path := range paths {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "state_"), ".sqlite")
		number, err := strconv.Atoi(name)
		if err == nil && number > generation {
			latest, generation = path, number
		}
	}
	if latest == "" {
		return nil, fmt.Errorf("Codex sidebar database is unavailable")
	}
	info, err := os.Lstat(latest)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Codex sidebar database is not a regular file")
	}
	absolute, err := filepath.Abs(latest)
	if err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT id FROM threads WHERE source = 'vscode' AND archived = 0")
	if err != nil {
		return nil, fmt.Errorf("read Codex sidebar inventory: %w", err)
	}
	defer rows.Close()
	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}
