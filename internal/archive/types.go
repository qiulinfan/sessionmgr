package archive

import "time"

const (
	SchemaVersion      = 1
	LayoutVersion      = 5
	RendererVersion    = 6
	MaxAttachmentBytes = int64(50 * 1024 * 1024)
)

type Options struct {
	CodexHome string
	Output    string
	Repo      string
	AllRepos  bool
	SessionID string

	// IncludeArchived adds Codex archived_sessions/ to discovery. Ordinary
	// exports inspect only active sessions/ so users can archive a conversation
	// before its first export to leave it out of the archive.
	IncludeArchived bool
	DeviceID        string
	DeviceName      string

	// StabilityWindow is the shared quiet period used before reading discovered
	// session files. Zero selects the production default; a negative value
	// disables the delay for deterministic fixtures that cannot mutate.
	StabilityWindow time.Duration
}

type Result struct {
	SchemaVersion       int      `json:"schema_version"`
	Sources             int      `json:"sources"`
	Matched             int      `json:"matched"`
	Created             int      `json:"created"`
	Unchanged           int      `json:"unchanged"`
	Busy                int      `json:"busy"`
	FilteredInternal    int      `json:"filtered_internal"`
	Skipped             int      `json:"skipped"`
	Attachments         int      `json:"attachments"`
	ArchivedAttachments int      `json:"archived_attachments"`
	Output              string   `json:"output"`
	Changes             []Change `json:"changes"`
	Warnings            []string `json:"warnings,omitempty"`
}

type CleanupOptions struct {
	CodexHome string
	Output    string
	DeviceID  string
	Apply     bool

	// StabilityWindow has the same meaning as Options.StabilityWindow.
	StabilityWindow time.Duration
}

type CleanupResult struct {
	SchemaVersion int             `json:"schema_version"`
	Sources       int             `json:"sources"`
	Candidates    int             `json:"candidates"`
	Removed       int             `json:"removed"`
	Busy          int             `json:"busy"`
	Skipped       int             `json:"skipped"`
	DryRun        bool            `json:"dry_run"`
	Output        string          `json:"output"`
	Changes       []CleanupChange `json:"changes"`
	Warnings      []string        `json:"warnings,omitempty"`
}

type CleanupChange struct {
	Kind           string `json:"kind"`
	Reason         string `json:"reason"`
	RepositoryName string `json:"repository_name"`
	DeviceName     string `json:"device_name"`
	SessionID      string `json:"session_id"`
	SessionKey     string `json:"session_key"`
	Title          string `json:"title"`
	Path           string `json:"path"`
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
	Attachments    int    `json:"attachments"`
	ArchivedFiles  int    `json:"archived_attachments"`
}

type Repository struct {
	Key             string
	Name            string
	CanonicalRemote string
}

type Message struct {
	Role        string
	Text        string
	Timestamp   time.Time
	Attachments []Attachment
}

type Attachment struct {
	MessageIndex    int
	AttachmentIndex int
	Name            string
	MIMEType        string
	SourceKind      string
	Status          string
	ArchivePath     string
	RepositoryPath  string
	GitCommit       string
	Size            int64
	ContentHash     string

	// SourceValue and Data exist only while one export is being prepared. They
	// are never written to Markdown or metadata because they may contain a
	// machine-local absolute path, a remote URL, or embedded file bytes.
	SourceValue string
	LocalPath   string
	Data        []byte
}

type Session struct {
	ID                string
	Title             string
	TitleUpdatedAt    time.Time
	Originator        string
	SourceKind        string
	ThreadSource      string
	ParentThreadID    string
	ExcludeReason     string
	FilteredUserInput int
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
