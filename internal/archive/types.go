package archive

import "time"

const (
	SchemaVersion   = 1
	RendererVersion = 1
)

type Options struct {
	CodexHome string
	Output    string
	Repo      string
	AllRepos  bool
	SessionID string
}

type Result struct {
	SchemaVersion int      `json:"schema_version"`
	Sources       int      `json:"sources"`
	Matched       int      `json:"matched"`
	Created       int      `json:"created"`
	Unchanged     int      `json:"unchanged"`
	Skipped       int      `json:"skipped"`
	Output        string   `json:"output"`
	Changes       []Change `json:"changes"`
	Warnings      []string `json:"warnings,omitempty"`
}

type Change struct {
	Kind           string `json:"kind"`
	RepositoryKey  string `json:"repository_key"`
	RepositoryName string `json:"repository_name"`
	SessionID      string `json:"session_id"`
	Title          string `json:"title"`
	SnapshotHash   string `json:"snapshot_hash"`
	SourceHash     string `json:"source_hash"`
	UpdatedAt      string `json:"updated_at"`
	Path           string `json:"path"`
}

type Repository struct {
	Key             string
	Name            string
	CanonicalRemote string
}

type Message struct {
	Role      string
	Text      string
	Timestamp time.Time
}

type Session struct {
	ID             string
	Title          string
	TitleUpdatedAt time.Time
	CWD            string
	Remote         string
	Commit         string
	Branch         string
	CodexVersion   string
	StartedAt      time.Time
	UpdatedAt      time.Time
	RawHash        string
	RecordCount    int
	MalformedCount int
	OmittedCount   int
	ToolCallCount  int
	Messages       []Message
}

type Snapshot struct {
	Repository   Repository
	Session      Session
	Hash         string
	Redactions   int
	SourceUpdate time.Time
}

type ListOptions struct {
	Output  string
	History bool
}

type Entry struct {
	RepositoryKey  string `json:"repository_key"`
	RepositoryName string `json:"repository_name"`
	SessionID      string `json:"session_id"`
	Title          string `json:"title"`
	SnapshotHash   string `json:"snapshot_hash"`
	SourceHash     string `json:"source_hash"`
	UpdatedAt      string `json:"updated_at"`
	Versions       int    `json:"versions"`
	Path           string `json:"path"`
}
