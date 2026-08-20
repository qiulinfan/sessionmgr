package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sessionmgr/sessionmgr/internal/archive"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "sessionmgr-app-test-sources-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	_ = os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	_ = os.Setenv("DSH_HOME", filepath.Join(root, "dsh"))
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

func TestVersionCommandUsesBuildVersion(t *testing.T) {
	previous := version
	version = "1.2.3"
	t.Cleanup(func() { version = previous })

	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{"version"}, &stdout, &stderr)
	if err != nil || code != 0 || stdout.String() != "sessionmgr 1.2.3\n" || stderr.Len() != 0 {
		t.Fatalf("version command did not use the build version: code=%d err=%v stdout=%q stderr=%q", code, err, stdout.String(), stderr.String())
	}
}

func TestConfiguredExportShowsOnlyCurrentChanges(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	output := filepath.Join(root, "sessions")
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"cli-session","cwd":"/missing","git":{"repository_url":"git@github.com:example/cli.git"}}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"archive me","attachments":[{"filename":"cli-note.txt","mime_type":"text/plain","file_data":"Y2xpIG5vdGUK"}]}}
{"timestamp":"2026-08-05T01:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"archived"}}
`
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONMGR_CONFIG", filepath.Join(root, "config.json"))

	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{"config", "set-directory", output}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("config failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code, err = Run(context.Background(), []string{"export", "--all", "--codex-home", codexHome, "--json"}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var exported struct {
		Created             int `json:"created"`
		Attachments         int `json:"attachments"`
		ArchivedAttachments int `json:"archived_attachments"`
		Changes             []struct {
			Kind string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &exported); err != nil || exported.Created != 1 || exported.Attachments != 1 || exported.ArchivedAttachments != 1 || len(exported.Changes) != 1 || exported.Changes[0].Kind != "new" {
		t.Fatalf("unexpected export JSON: %s (%v)", stdout.String(), err)
	}
	attachmentMatches, err := filepath.Glob(filepath.Join(output, "github.com-example", "cli", "*", "*", "attachments", "001-cli-note.txt"))
	if err != nil || len(attachmentMatches) != 1 {
		t.Fatalf("CLI attachment path missing: %v, %v", attachmentMatches, err)
	}

	stdout.Reset()
	stderr.Reset()
	code, err = Run(context.Background(), []string{"export", "--all", "--codex-home", codexHome}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("repeat export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	if stdout.String() != "No changes.\n" {
		t.Fatalf("repeat export showed old records: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code, err = Run(context.Background(), []string{"list", "--json"}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("list failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var listed struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != "cli-session" {
		t.Fatalf("unexpected list JSON: %s", stdout.String())
	}
}

func TestCLIArchivedSessionsRequireIncludeFlag(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	source := filepath.Join(codexHome, "archived_sessions", "fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"archived-cli","cwd":"/missing","git":{"repository_url":"https://github.com/example/cli.git"}}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"keep this archived"}}
`
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONMGR_CONFIG", filepath.Join(root, "config.json"))

	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{
		"export", "--all", "--directory", filepath.Join(root, "exports"),
		"--codex-home", codexHome, "--json",
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("default export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var defaultResult struct {
		Sources int `json:"sources"`
		Created int `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &defaultResult); err != nil || defaultResult.Sources != 0 || defaultResult.Created != 0 {
		t.Fatalf("default CLI export included archived session: %s (%v)", stdout.String(), err)
	}

	stdout.Reset()
	stderr.Reset()
	code, err = Run(context.Background(), []string{
		"export", "--all", "--include-archived", "--codex-home", codexHome, "--json",
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("archived export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var included struct {
		Sources int `json:"sources"`
		Created int `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &included); err != nil || included.Sources != 1 || included.Created != 1 {
		t.Fatalf("--include-archived did not export session: %s (%v)", stdout.String(), err)
	}
}

func TestCLINonGitDirectoriesRequireIncludeFlagAndRepeatFully(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	localDirectory := filepath.Join(root, "loose-work")
	if err := os.MkdirAll(localDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "non-git.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"non-git-cli","cwd":%q}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"export loose work"}}
`, localDirectory)
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONMGR_CONFIG", filepath.Join(root, "config.json"))
	output := filepath.Join(root, "exports")

	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{
		"export", "--all", "--directory", output, "--codex-home", codexHome, "--json",
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("default export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var excluded struct {
		FilteredNonGit int `json:"filtered_non_git"`
		Created        int `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &excluded); err != nil || excluded.FilteredNonGit != 1 || excluded.Created != 0 {
		t.Fatalf("default CLI export included non-Git data: %s (%v)", stdout.String(), err)
	}

	stdout.Reset()
	stderr.Reset()
	code, err = Run(context.Background(), []string{
		"export", "--all", "--include-non-git", "--codex-home", codexHome, "--json",
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("included export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var first struct {
		Created int `json:"created"`
		Changes []struct {
			Kind string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &first); err != nil || first.Created != 1 || len(first.Changes) != 1 || first.Changes[0].Kind != "new" {
		t.Fatalf("--include-non-git did not export the session: %s (%v)", stdout.String(), err)
	}

	stdout.Reset()
	stderr.Reset()
	code, err = Run(context.Background(), []string{
		"export", "--all", "--include-non-git", "--codex-home", codexHome, "--json",
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("repeat full export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var repeated struct {
		FullExported int `json:"full_exported"`
		Changes      []struct {
			Kind string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &repeated); err != nil || repeated.FullExported != 1 ||
		len(repeated.Changes) != 1 || repeated.Changes[0].Kind != "full" {
		t.Fatalf("repeat CLI export was not full: %s (%v)", stdout.String(), err)
	}
}

func TestHumanChangesUseSemanticFieldsWithoutHashes(t *testing.T) {
	var output bytes.Buffer
	err := printChanges(&output, []archive.Change{{
		Kind: "new", RepositoryName: "project", Title: "Readable title", DeviceName: "workstation",
		SessionID: "native-session", SessionKey: "sha256:session-key", DocumentHash: "sha256:document",
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, hidden := range []string{"HASH", "sha256", "native-session"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("human output exposed %q: %s", hidden, text)
		}
	}
	for _, visible := range []string{"Readable title", "workstation", "project"} {
		if !strings.Contains(text, visible) {
			t.Fatalf("human output omitted %q: %s", visible, text)
		}
	}
}

func TestCleanupHumanOutputIsExplicitlyDryRun(t *testing.T) {
	var output bytes.Buffer
	result := archive.CleanupResult{
		DryRun: true, Candidates: 1,
		Changes: []archive.CleanupChange{{
			Kind: "remove", Reason: "subagent", RepositoryName: "project",
			DeviceName: "workstation", SessionID: "native-session",
			SessionKey: "sha256:hidden", Title: "Internal review",
		}},
	}
	if err := printCleanupChanges(&output, result); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{"REMOVE", "Internal review", "subagent", "Dry run", "--apply"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("cleanup output omitted %q: %s", expected, text)
		}
	}
	for _, hidden := range []string{"native-session", "sha256:hidden"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("cleanup output exposed %q: %s", hidden, text)
		}
	}
}

func TestCLIReportsBusySessionWithoutFailing(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex")
	source := filepath.Join(codexHome, "sessions", "2026", "08", "05", "active.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"timestamp":"2026-08-05T01:00:00Z"`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONMGR_CONFIG", filepath.Join(root, "config.json"))
	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{
		"export", "--directory", filepath.Join(root, "exports"),
		"--codex-home", codexHome, "--json",
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("busy export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var result struct {
		Busy    int `json:"busy"`
		Skipped int `json:"skipped"`
		Created int `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Busy != 1 || result.Skipped != 0 || result.Created != 0 {
		t.Fatalf("unexpected busy JSON: %s", stdout.String())
	}
}

func TestCLIAutoDetectsDeepSeekWithoutIncludeFlag(t *testing.T) {
	root := t.TempDir()
	deepSeekHome := filepath.Join(root, "dsh")
	localDirectory := filepath.Join(root, "workspace")
	if err := os.MkdirAll(localDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(deepSeekHome, "sessions", "--workspace--", "session-cli-deepseek", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{"type":"session","version":0,"id":"session-cli-deepseek","createdAt":1786762800000,"cwd":%q,"delegationDepth":0,"agentPreset":"standard"}
{"type":"user/message","seq":0,"time":1786762801000,"data":{"id":"user","role":"user","content":[{"type":"text","text":"export dsh"}],"source":{"kind":"user"}},"surfaceOp":"append"}
{"type":"assistant/message","seq":1,"time":1786762802000,"data":{"turn":0,"step":0,"message":{"id":"assistant","role":"assistant","content":[{"type":"text","text":"done"}],"source":{"kind":"model","provider":"deepseek","model":"fixture"}}},"surfaceOp":"append"}
`, localDirectory)
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONMGR_CONFIG", filepath.Join(root, "config.json"))
	output := filepath.Join(root, "exports")
	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{
		"export", "--all", "--directory", output, "--codex-home", filepath.Join(root, "codex"),
		"--deepseek-home", deepSeekHome, "--include-non-git", "--json",
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("default export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var defaultResult struct {
		Sources int `json:"sources"`
		Created int `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &defaultResult); err != nil || defaultResult.Sources != 1 || defaultResult.Created != 1 {
		t.Fatalf("CLI did not auto-detect DeepSeek: %s (%v)", stdout.String(), err)
	}
	var included struct {
		Created int `json:"created"`
		Changes []struct {
			Harness string `json:"harness"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &included); err != nil || included.Created != 1 || len(included.Changes) != 1 || included.Changes[0].Harness != "deepseek" {
		t.Fatalf("auto-detected DeepSeek change is invalid: %s (%v)", stdout.String(), err)
	}
}

func TestCLIAutoDetectsClaudeWithoutIncludeFlag(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "claude")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "66666666-6666-6666-6666-666666666666"
	source := filepath.Join(claudeHome, "projects", "project", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{"type":"user","uuid":"user","parentUuid":null,"sessionId":%q,"timestamp":"2026-08-20T01:00:00Z","cwd":%q,"version":"2.1.235","userType":"external","origin":{"kind":"human"},"promptSource":"typed","message":{"role":"user","content":"CLI Claude export"}}
`, id, workspace)
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONMGR_CONFIG", filepath.Join(root, "config.json"))
	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{
		"export", "--all", "--directory", filepath.Join(root, "exports"),
		"--codex-home", filepath.Join(root, "codex"), "--claude-home", claudeHome,
		"--deepseek-home", filepath.Join(root, "dsh"), "--include-non-git", "--json",
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("Claude export failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	var result struct {
		Sources int `json:"sources"`
		Created int `json:"created"`
		Changes []struct {
			Harness string `json:"harness"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Sources != 1 || result.Created != 1 || len(result.Changes) != 1 || result.Changes[0].Harness != "claude-code" {
		t.Fatalf("CLI did not auto-detect Claude: %s (%v)", stdout.String(), err)
	}
}
