package archive

import "time"

const (
	SchemaVersion   = 1
	LayoutVersion   = 3
	RendererVersion = 3
)

type Options struct {
	CodexHome  string
	Output     string
	Repo       string
	AllRepos   bool
	SessionID  string
	DeviceID   string
	DeviceName string

	// StabilityWindow is the shared quiet period used before reading discovered
	// session files. Zero selects the production default; a negative value
	// disables the delay for deterministic fixtures that cannot mutate.
	StabilityWindow time.Duration
}

type Result struct {
	SchemaVersion int      `json:"schema_version"`
	Sources       int      `json:"sources"`
	Matched       int      `json:"matched"`
	Created       int      `json:"created"`
	Unchanged     int      `json:"unchanged"`
	Busy          int      `json:"busy"`
	Skipped       int      `json:"skipped"`
	Output        string   `json:"output"`
	Changes       []Change `json:"changes"`
	Warnings      []string `json:"warnings,omitempty"`
}

type Change struct {
	Kind           string `json:"kind"`
	RepositoryKey  string `json:"repository_key"`
	RepositoryName string `json:"repository_name"`
	DeviceName     string `json:"device_name"`
	SessionID      string `json:"session_id"`
	SessionKey     string `json:"session_key"`
	Title          string `json:"title"`
	DocumentHash   string `json:"document_hash"`
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
	ID                string
	Title             string
	TitleUpdatedAt    time.Time
	CWD               string
	Remote            string
	Commit            string
	Branch            string
	CodexVersion      string
	CreatedAt         time.Time
	FirstMessageAt    time.Time
	LastMessageAt     time.Time
	LastEventAt       time.Time
	RawHash           string
	RecordCount       int
	MalformedCount    int
	OmittedCount      int
	ToolCallCount     int
	UserMessages      int
	AssistantMessages int
	Messages          []Message
}

type Snapshot struct {
	Repository   Repository
	Session      Session
	DeviceID     string
	DeviceName   string
	SessionKey   string
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
	DeviceID       string `json:"device_id,omitempty"`
	DeviceName     string `json:"device_name,omitempty"`
	SessionID      string `json:"session_id"`
	SessionKey     string `json:"session_key,omitempty"`
	Title          string `json:"title"`
	DocumentHash   string `json:"document_hash,omitempty"`
	SnapshotHash   string `json:"snapshot_hash,omitempty"`
	SourceHash     string `json:"source_hash"`
	UpdatedAt      string `json:"updated_at"`
	Versions       int    `json:"versions"`
	Legacy         bool   `json:"legacy,omitempty"`
	Path           string `json:"path"`
}
