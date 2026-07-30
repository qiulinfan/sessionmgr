package catalog

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sessionmgr/sessionmgr/internal/domain"
)

type Catalog struct {
	db *sql.DB
}

type Filter struct {
	RepoID  string
	Agent   string
	Machine string
	Tag     string
}

type Summary struct {
	ID              string    `json:"run_id"`
	Title           string    `json:"title"`
	CreatedAt       time.Time `json:"created_at"`
	RepoID          string    `json:"repo_id"`
	AgentPlatform   string    `json:"agent_platform"`
	ParentRunID     string    `json:"parent_run_id,omitempty"`
	Relation        string    `json:"relation"`
	SourceMachineID string    `json:"source_machine_id"`
	IntegrityStatus string    `json:"integrity_status"`
}

func Open(path string) (*Catalog, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, err
	}
	schema := `
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  manifest_digest TEXT NOT NULL,
  canonical_title TEXT NOT NULL,
  created_at TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  agent_platform TEXT NOT NULL,
  parent_run_id TEXT,
  relation TEXT NOT NULL,
  source_machine_id TEXT NOT NULL,
  native_session_id TEXT,
  integrity_status TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_overlays (
  run_id TEXT PRIMARY KEY,
  display_title TEXT,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tags (
  run_id TEXT NOT NULL,
  tag TEXT NOT NULL,
  PRIMARY KEY (run_id, tag)
);
CREATE TABLE IF NOT EXISTS path_mappings (
  repo_id TEXT NOT NULL,
  machine_id TEXT NOT NULL,
  local_path TEXT NOT NULL,
  last_verified_at TEXT,
  PRIMARY KEY (repo_id, machine_id)
);
CREATE TABLE IF NOT EXISTS sync_state (
  store_name TEXT NOT NULL,
  run_id TEXT NOT NULL,
  status TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (store_name, run_id)
);
CREATE INDEX IF NOT EXISTS runs_created_at_idx ON runs(created_at DESC);
CREATE INDEX IF NOT EXISTS runs_repo_idx ON runs(repo_id, created_at DESC);
`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &Catalog{db: db}, nil
}

func (c *Catalog) Close() error {
	return c.db.Close()
}

func (c *Catalog) InsertRun(run domain.Run, manifestDigest string) error {
	repoID := ""
	if len(run.Workspaces) > 0 {
		repoID = run.Workspaces[0].Repository.ID
	}
	agent, nativeID := "none", ""
	if len(run.Sessions) > 0 {
		agent = run.Sessions[0].Platform
		nativeID = run.Sessions[0].NativeID
	}
	_, err := c.db.Exec(`
INSERT INTO runs (
  id, manifest_digest, canonical_title, created_at, repo_id, agent_platform,
  parent_run_id, relation, source_machine_id, native_session_id, integrity_status
) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), 'verified')
ON CONFLICT(id) DO UPDATE SET
  manifest_digest=excluded.manifest_digest,
  integrity_status=CASE
    WHEN runs.manifest_digest=excluded.manifest_digest THEN 'verified'
    ELSE 'conflict'
  END`,
		run.ID, manifestDigest, run.Title, run.CreatedAt.UTC().Format(time.RFC3339Nano),
		repoID, agent, run.ParentRunID, run.Relation, run.CreatedBy.MachineID, nativeID)
	return err
}

func (c *Catalog) UpdateIntegrity(id, status string) error {
	_, err := c.db.Exec(`UPDATE runs SET integrity_status=? WHERE id=?`, status, id)
	return err
}

func (c *Catalog) RecordPath(repoID, machineID, path string) error {
	_, err := c.db.Exec(`
INSERT INTO path_mappings(repo_id, machine_id, local_path, last_verified_at)
VALUES(?, ?, ?, ?)
ON CONFLICT(repo_id, machine_id) DO UPDATE SET
  local_path=excluded.local_path, last_verified_at=excluded.last_verified_at`,
		repoID, machineID, path, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (c *Catalog) RecordSync(storeName, runID, status string) error {
	_, err := c.db.Exec(`
INSERT INTO sync_state(store_name, run_id, status, updated_at)
VALUES(?, ?, ?, ?)
ON CONFLICT(store_name, run_id) DO UPDATE SET
  status=excluded.status, updated_at=excluded.updated_at`,
		storeName, runID, status, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (c *Catalog) List(filter Filter) ([]Summary, error) {
	query := `
SELECT r.id, COALESCE(o.display_title, r.canonical_title), r.created_at,
       r.repo_id, r.agent_platform, COALESCE(r.parent_run_id, ''),
       r.relation, r.source_machine_id, r.integrity_status
FROM runs r
LEFT JOIN run_overlays o ON o.run_id=r.id`
	var where []string
	var args []interface{}
	if filter.RepoID != "" {
		where = append(where, "r.repo_id LIKE ?")
		args = append(args, filter.RepoID+"%")
	}
	if filter.Agent != "" {
		where = append(where, "r.agent_platform=?")
		args = append(args, filter.Agent)
	}
	if filter.Machine != "" {
		where = append(where, "r.source_machine_id LIKE ?")
		args = append(args, filter.Machine+"%")
	}
	if filter.Tag != "" {
		where = append(where, "EXISTS (SELECT 1 FROM tags t WHERE t.run_id=r.id AND t.tag=?)")
		args = append(args, filter.Tag)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY r.created_at DESC"
	rows, err := c.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Summary
	for rows.Next() {
		var item Summary
		var created string
		if err := rows.Scan(&item.ID, &item.Title, &created, &item.RepoID,
			&item.AgentPlatform, &item.ParentRunID, &item.Relation,
			&item.SourceMachineID, &item.IntegrityStatus); err != nil {
			return nil, err
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("catalog created_at: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
