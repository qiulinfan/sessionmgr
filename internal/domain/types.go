package domain

import "time"

const SchemaVersion = 1

type Run struct {
	SchemaVersion int                   `json:"schema_version"`
	ID            string                `json:"run_id"`
	Title         string                `json:"title"`
	CreatedAt     time.Time             `json:"created_at"`
	CreatedBy     MachineIdentity       `json:"created_by"`
	Runtime       RuntimeContext        `json:"runtime"`
	ParentRunID   string                `json:"parent_run_id,omitempty"`
	Relation      string                `json:"relation"`
	Workspaces    []WorkspaceSnapshot   `json:"workspaces"`
	Sessions      []AgentSession        `json:"sessions"`
	Checkpoints   []Checkpoint          `json:"checkpoints"`
	Objects       []ObjectDescriptor    `json:"objects"`
	Security      SecurityReportSummary `json:"security"`
	Capabilities  []string              `json:"capabilities"`
}

type MachineIdentity struct {
	MachineID string `json:"machine_id"`
}

type RuntimeContext struct {
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	ShellName     string            `json:"shell_name,omitempty"`
	GitVersion    string            `json:"git_version"`
	AgentVersions map[string]string `json:"agent_versions,omitempty"`
}

type WorkspaceSnapshot struct {
	ID               string             `json:"id"`
	VCSType          string             `json:"vcs_type"`
	Repository       RepositoryIdentity `json:"repository"`
	SourcePathHint   string             `json:"source_path_hint"`
	GitCommonDirHint string             `json:"git_common_dir_hint"`
	Branch           string             `json:"branch,omitempty"`
	HeadSHA          string             `json:"head_sha"`
	UpstreamRef      string             `json:"upstream_ref,omitempty"`
	BaseSHA          string             `json:"base_sha"`
	IsDetached       bool               `json:"is_detached"`
	IsShallow        bool               `json:"is_shallow"`
	IsPartialClone   bool               `json:"is_partial_clone"`
	IsSparseCheckout bool               `json:"is_sparse_checkout"`
	OperationState   string             `json:"operation_state,omitempty"`
	Submodules       []SubmoduleState   `json:"submodules,omitempty"`
	Payload          WorkspacePayload   `json:"payload"`
	Digest           WorkspaceDigest    `json:"digest"`
	UntrackedCount   int                `json:"untracked_count"`
	UntrackedBytes   int64              `json:"untracked_bytes"`
	Warnings         []string           `json:"warnings,omitempty"`
}

type RepositoryIdentity struct {
	ID              string `json:"id"`
	CanonicalRemote string `json:"canonical_remote,omitempty"`
	RootCommit      string `json:"root_commit"`
}

type WorkspacePayload struct {
	CommitBundleObject      string `json:"commit_bundle_object,omitempty"`
	StagedPatchObject       string `json:"staged_patch_object"`
	UnstagedPatchObject     string `json:"unstaged_patch_object"`
	UntrackedManifestObject string `json:"untracked_manifest_object"`
}

type WorkspaceDigest struct {
	HeadSHA          string `json:"head_sha"`
	StagedPatchSHA   string `json:"staged_patch_sha"`
	UnstagedPatchSHA string `json:"unstaged_patch_sha"`
	UntrackedTreeSHA string `json:"untracked_tree_sha"`
}

type SubmoduleState struct {
	Path string `json:"path"`
	SHA  string `json:"sha"`
}

type UntrackedManifest struct {
	SchemaVersion int              `json:"schema_version"`
	Entries       []UntrackedEntry `json:"entries"`
}

type UntrackedEntry struct {
	Path       string `json:"path"`
	Mode       uint32 `json:"mode"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
	LinkTarget string `json:"link_target,omitempty"`
}

type AgentSession struct {
	ID               string              `json:"id"`
	Platform         string              `json:"platform"`
	NativeID         string              `json:"native_id"`
	NativeVersion    string              `json:"native_version,omitempty"`
	AdapterVersion   string              `json:"adapter_version"`
	SourceCWDHint    string              `json:"source_cwd_hint,omitempty"`
	StartedAt        time.Time           `json:"started_at"`
	EndedAt          *time.Time          `json:"ended_at,omitempty"`
	RawObjects       []string            `json:"raw_objects"`
	NormalizedObject string              `json:"normalized_object"`
	Capabilities     AdapterCapabilities `json:"capabilities"`
}

type AdapterCapabilities struct {
	Archive       bool   `json:"archive"`
	Normalize     bool   `json:"normalize"`
	NativeRestore string `json:"native_restore"`
	Handoff       bool   `json:"handoff"`
}

type NormalizedEvent struct {
	SchemaVersion int                    `json:"schema_version"`
	EventID       string                 `json:"event_id"`
	SessionID     string                 `json:"session_id"`
	Sequence      int64                  `json:"sequence"`
	Timestamp     time.Time              `json:"timestamp"`
	Actor         string                 `json:"actor"`
	Kind          string                 `json:"kind"`
	Summary       string                 `json:"summary"`
	Payload       map[string]interface{} `json:"payload,omitempty"`
	CheckpointID  string                 `json:"checkpoint_id,omitempty"`
	Source        EventSource            `json:"source"`
}

type EventSource struct {
	RawObject string `json:"raw_object"`
	Record    int64  `json:"record"`
}

type Checkpoint struct {
	ID               string           `json:"id"`
	CreatedAt        time.Time        `json:"created_at"`
	Label            string           `json:"label"`
	WorkspaceID      string           `json:"workspace_id"`
	WorkspaceDigest  WorkspaceDigest  `json:"workspace_digest"`
	SessionPositions map[string]int64 `json:"session_positions"`
}

type ObjectDescriptor struct {
	Digest    string `json:"digest"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Encoding  string `json:"encoding"`
	Required  bool   `json:"required"`
}

type SecurityReportSummary struct {
	ScannerVersion string            `json:"scanner_version"`
	Blocked        int               `json:"blocked"`
	Warnings       int               `json:"warnings"`
	Info           int               `json:"info"`
	Findings       []SecurityFinding `json:"findings,omitempty"`
}

type SecurityFinding struct {
	ID       string `json:"id"`
	RuleID   string `json:"rule_id"`
	Source   string `json:"source"`
	Line     int    `json:"line,omitempty"`
	Preview  string `json:"masked_preview"`
	Severity string `json:"severity"`
}

type OperationReport struct {
	SchemaVersion  int               `json:"schema_version"`
	OperationID    string            `json:"operation_id"`
	Operation      string            `json:"operation"`
	Status         string            `json:"status"`
	RunID          string            `json:"run_id,omitempty"`
	StartedAt      time.Time         `json:"started_at"`
	FinishedAt     time.Time         `json:"finished_at"`
	StateModified  bool              `json:"state_modified"`
	Target         string            `json:"target,omitempty"`
	CreatedBranch  string            `json:"created_branch,omitempty"`
	DigestExpected *WorkspaceDigest  `json:"digest_expected,omitempty"`
	DigestActual   *WorkspaceDigest  `json:"digest_actual,omitempty"`
	NativeRestore  string            `json:"native_restore,omitempty"`
	HandoffPath    string            `json:"handoff_path,omitempty"`
	Warnings       []string          `json:"warnings,omitempty"`
	ErrorCode      string            `json:"error_code,omitempty"`
	Error          string            `json:"error,omitempty"`
	NextCommand    string            `json:"next_command,omitempty"`
	Details        map[string]string `json:"details,omitempty"`
}

type Result struct {
	SchemaVersion int      `json:"schema_version"`
	OperationID   string   `json:"operation_id,omitempty"`
	Status        string   `json:"status"`
	RunID         string   `json:"run_id,omitempty"`
	Warnings      []string `json:"warnings"`
	ReportPath    string   `json:"report_path,omitempty"`
}
