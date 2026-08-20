package archive

import "time"

const (
	SchemaVersion             = 1
	ExportResultSchemaVersion = 3
	LocalRepositorySchema     = 2
	SessionMetadataSchema     = 2
	LayoutVersion             = 5
	RendererVersion           = 8
	MaxAttachmentBytes        = int64(50 * 1024 * 1024)
)

// SourceSelection identifies the native harnesses selected for one export.
// A nil *SourceSelection in Options retains the historical library default of
// Codex plus the deprecated IncludeDeepSeek field. Product entrypoints always
// pass an explicit selection so a missing native state directory is optional.
type SourceSelection struct {
	Codex      bool
	ClaudeCode bool
	DeepSeek   bool
}

type Options struct {
	CodexHome    string
	ClaudeHome   string
	DeepSeekHome string
	Output       string
	Repo         string
	AllRepos     bool
	SessionID    string
	Sources      *SourceSelection

	// IncludeArchived adds Codex archived_sessions/ to discovery. Ordinary
	// exports inspect only active sessions/ so users can archive a conversation
	// before its first export to leave it out of the archive.
	IncludeArchived bool

	// IncludeDeepSeek is retained for callers compiled against the v0.6/v0.7
	// library surface. CLI and GUI entrypoints use Sources instead.
	IncludeDeepSeek bool

	// IncludeNonGit adds sessions whose CWD cannot be assigned to a hosted Git
	// remote. Those directories use a device-local identity and are fully
	// republished on every selected export rather than treated incrementally.
	IncludeNonGit bool
	DeviceID      string
	DeviceName    string

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
	FilteredNonGit      int      `json:"filtered_non_git"`
	FullExported        int      `json:"full_exported"`
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
	Harness        string `json:"harness"`
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
	Kind            string
	DirectoryName   string
	DirectoryID     string
	DeviceID        string
	DeviceName      string
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

	// SourceValue, Data, and Expected* exist only while one export is being
	// prepared. They are never written to Markdown or metadata because they may
	// contain a machine-local absolute path, embedded bytes, or redundant source
	// integrity facts.
	SourceValue  string
	LocalPath    string
	Data         []byte
	ExpectedHash string
	ExpectedSize int64
}

type Session struct {
	ID                string
	Harness           string
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
	ClaudeVersion     string
	CreatedAt         time.Time
	FirstMessageAt    time.Time
	LastMessageAt     time.Time
	LastEventAt       time.Time
	RawHash           string
	RecordCount       int
	MalformedCount    int
	OmittedCount      int
	ToolCallCount     int
	AlternateBranches int
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
	Harness        string `json:"harness,omitempty"`
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
