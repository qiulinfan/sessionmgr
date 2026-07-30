package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sessionmgr/sessionmgr/internal/agent/codex"
	"github.com/sessionmgr/sessionmgr/internal/catalog"
	"github.com/sessionmgr/sessionmgr/internal/config"
	"github.com/sessionmgr/sessionmgr/internal/cryptox"
	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/gitx"
	"github.com/sessionmgr/sessionmgr/internal/handoff"
	"github.com/sessionmgr/sessionmgr/internal/home"
	"github.com/sessionmgr/sessionmgr/internal/ids"
	"github.com/sessionmgr/sessionmgr/internal/operation"
	"github.com/sessionmgr/sessionmgr/internal/secretscan"
	"github.com/sessionmgr/sessionmgr/internal/store"
	"github.com/sessionmgr/sessionmgr/internal/syncer"
)

const version = "0.1.0-dev"

type commandError struct {
	exitCode int
	code     string
	message  string
}

func (e *commandError) Error() string {
	if e.code == "" {
		return e.message
	}
	return e.code + ": " + e.message
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		printHelp(stdout)
		return 2, nil
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintf(stdout, "sessionmgr %s\n", version)
		return 0, nil
	}
	layout, err := home.Resolve()
	if err != nil {
		return 3, err
	}
	if err := home.Ensure(layout); err != nil {
		return 3, err
	}
	cat, err := catalog.Open(layout.Catalog)
	if err != nil {
		return 3, fmt.Errorf("open catalog: %w", err)
	}
	defer cat.Close()
	objectStore := store.New(layout)
	rebuildCatalog(cat, objectStore)

	var runErr error
	switch args[0] {
	case "help", "-h", "--help":
		printHelp(stdout)
		return 0, nil
	case "init":
		runErr = commandInit(layout, stdout)
	case "doctor":
		runErr = commandDoctor(args[1:], layout, stdout, stderr)
	case "capture":
		runErr = commandCapture(ctx, args[1:], layout, cat, objectStore, stdout, stderr)
	case "list":
		runErr = commandList(args[1:], cat, stdout, stderr)
	case "show":
		runErr = commandShow(args[1:], objectStore, stdout, stderr)
	case "verify":
		runErr = commandVerify(args[1:], cat, objectStore, stdout, stderr)
	case "restore":
		runErr = commandRestore(ctx, args[1:], layout, cat, objectStore, stdout, stderr)
	case "handoff":
		runErr = commandHandoff(args[1:], layout, objectStore, stdout, stderr)
	case "push":
		runErr = commandPush(ctx, args[1:], layout, cat, objectStore, stdout, stderr)
	case "pull":
		runErr = commandPull(ctx, args[1:], layout, cat, objectStore, stdout, stderr)
	case "reconcile":
		runErr = commandReconcile(args[1:], cat, objectStore, stdout, stderr)
	default:
		printHelp(stderr)
		runErr = &commandError{exitCode: 2, code: "E_ARGUMENT", message: "unknown command " + args[0]}
	}
	if runErr == nil {
		return 0, nil
	}
	var typed *commandError
	if errors.As(runErr, &typed) {
		return typed.exitCode, typed
	}
	return 1, runErr
}

func commandInit(layout home.Layout, stdout io.Writer) error {
	machineID, err := home.LoadOrCreateMachineID(layout)
	if err != nil {
		return err
	}
	recipient, err := cryptox.EnsureIdentity(filepath.Join(layout.Keys, "identity.txt"))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Initialized Session Manager at %s\nMachine ID: %s\nAge recipient: %s\n", layout.Root, machineID, recipient)
	return nil
}

func commandDoctor(args []string, layout home.Layout, stdout, stderr io.Writer) error {
	flags := newFlagSet("doctor", stderr)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "json"); err != nil {
		return argumentError(err)
	}
	type check struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Details string `json:"details,omitempty"`
	}
	checks := []check{{Name: "sessionmgr_home", Status: "ok", Details: layout.Root}}
	if path, err := exec.LookPath("git"); err == nil {
		checks = append(checks, check{Name: "git", Status: "ok", Details: path})
	} else {
		checks = append(checks, check{Name: "git", Status: "failed", Details: err.Error()})
	}
	if root, err := codex.StateRoot(); err == nil {
		status := "ok"
		if _, statErr := os.Stat(root); statErr != nil {
			status = "warning"
		}
		checks = append(checks, check{Name: "codex_state", Status: status, Details: root})
	}
	identityPath := filepath.Join(layout.Keys, "identity.txt")
	if identity, err := cryptox.LoadIdentity(identityPath); err == nil {
		checks = append(checks, check{Name: "age_identity", Status: "ok", Details: identity.Recipient().String()})
	} else {
		checks = append(checks, check{Name: "age_identity", Status: "warning", Details: "run sessionmgr init"})
	}
	failed := false
	for _, item := range checks {
		if item.Status == "failed" {
			failed = true
		}
	}
	if *jsonOutput {
		status := "ok"
		if failed {
			status = "failed"
		}
		return writeJSON(stdout, map[string]interface{}{"schema_version": 1, "status": status, "checks": checks})
	}
	for _, item := range checks {
		fmt.Fprintf(stdout, "%-18s %-8s %s\n", item.Name, item.Status, item.Details)
	}
	if failed {
		return &commandError{exitCode: 3, code: "E_DOCTOR", message: "one or more required checks failed"}
	}
	return nil
}

func commandCapture(
	ctx context.Context,
	args []string,
	layout home.Layout,
	cat *catalog.Catalog,
	objectStore *store.Store,
	stdout, stderr io.Writer,
) error {
	flags := newFlagSet("capture", stderr)
	repoFlag := flags.String("repo", ".", "Git worktree")
	agent := flags.String("agent", "codex", "agent adapter")
	sessionID := flags.String("session", "", "native session ID")
	latest := flags.Bool("latest", false, "select latest matching session")
	title := flags.String("title", "", "run title")
	untracked := flags.String("untracked", "include", "include or exclude untracked files")
	var includeIgnored stringListFlag
	flags.Var(&includeIgnored, "include-ignored", "include ignored files matching PATTERN (repeatable)")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "latest", "json"); err != nil {
		return argumentError(err)
	}
	if *sessionID != "" && *latest {
		return argumentError(fmt.Errorf("--session and --latest are mutually exclusive"))
	}
	if *agent != "codex" {
		return argumentError(fmt.Errorf("MVP supports only --agent codex"))
	}
	if *untracked != "include" && *untracked != "exclude" {
		return argumentError(fmt.Errorf("--untracked must be include or exclude"))
	}
	runID, err := ids.NewUUIDv7()
	if err != nil {
		return err
	}
	operationID, err := ids.NewPrefixed("op")
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	report := domain.OperationReport{
		SchemaVersion: domain.SchemaVersion, OperationID: operationID,
		Operation: "capture", Status: "failed", RunID: runID, StartedAt: started,
	}
	fail := func(code string, cause error) error {
		report.FinishedAt = time.Now().UTC()
		report.ErrorCode = code
		report.Error = cause.Error()
		path, _ := operation.Write(layout, report)
		if *jsonOutput {
			_ = writeJSON(stdout, domain.Result{
				SchemaVersion: 1, OperationID: operationID, Status: "failed",
				RunID: runID, Warnings: []string{cause.Error()}, ReportPath: path,
			})
		}
		return &commandError{exitCode: 4, code: code, message: cause.Error()}
	}
	repo, err := gitx.TopLevel(ctx, *repoFlag)
	if err != nil {
		return fail("E_GIT_REPOSITORY", err)
	}
	cfg, configErr := config.Load(layout.Config)
	if configErr != nil {
		return fail("E_CONFIG", configErr)
	}
	gitResult, err := gitx.Capture(ctx, objectStore, gitx.CaptureOptions{
		Repo: repo, RunID: runID, IncludeUntracked: *untracked == "include",
		IncludeIgnored: []string(includeIgnored),
		MaxFileBytes:   cfg.Capture.MaxFileBytes, MaxTotalBytes: cfg.Capture.MaxTotalBytes,
		TmpDir: layout.Tmp,
	})
	if err != nil {
		return fail("E_WORKSPACE_CAPTURE", err)
	}
	sessions := []domain.AgentSession{}
	objects := append([]domain.ObjectDescriptor(nil), gitResult.Objects...)
	findings := append([]domain.SecurityFinding(nil), gitResult.Findings...)
	positions := make(map[string]int64)
	agentVersions := make(map[string]string)
	selectedTitle := ""
	if *agent == "codex" {
		candidate, err := codex.Select(codex.Query{
			Repo: repo, SessionID: *sessionID, Latest: *latest,
		})
		if err != nil {
			return fail("E_SESSION_SELECTION", err)
		}
		captured, err := codex.Capture(ctx, objectStore, candidate)
		if err != nil {
			return fail("E_SESSION_CAPTURE", err)
		}
		sessions = append(sessions, captured.Session)
		objects = append(objects, captured.Objects...)
		findings = append(findings, captured.Findings...)
		positions[captured.Session.ID] = captured.Events
		agentVersions["codex"] = candidate.CodexVersion
		selectedTitle = candidate.Title
		if captured.Unknown > 0 {
			gitResult.Workspace.Warnings = append(gitResult.Workspace.Warnings,
				fmt.Sprintf("Codex normalization preserved %d unknown record(s) only in raw session", captured.Unknown))
		}
	}
	machineID, err := home.LoadOrCreateMachineID(layout)
	if err != nil {
		return fail("E_MACHINE_ID", err)
	}
	if *title == "" {
		*title = selectedTitle
		if *title == "" {
			*title = filepath.Base(repo) + " " + started.Local().Format("2006-01-02 15:04")
		}
	}
	checkpointID, err := ids.NewPrefixed("cp")
	if err != nil {
		return fail("E_ID", err)
	}
	capabilities := []string{"workspace.git.v1", "capsule.manifest.v1", "handoff.markdown.v1"}
	if len(sessions) > 0 {
		capabilities = append(capabilities, "session.codex.raw.v1", "session.normalized.v1")
	}
	run := domain.Run{
		SchemaVersion: domain.SchemaVersion,
		ID:            runID, Title: *title, CreatedAt: started,
		CreatedBy: domain.MachineIdentity{MachineID: machineID},
		Runtime: domain.RuntimeContext{
			OS: runtime.GOOS, Arch: runtime.GOARCH, ShellName: filepath.Base(os.Getenv("SHELL")),
			GitVersion: gitx.GitVersion(ctx, repo), AgentVersions: agentVersions,
		},
		Relation:   "capture",
		Workspaces: []domain.WorkspaceSnapshot{gitResult.Workspace},
		Sessions:   sessions,
		Checkpoints: []domain.Checkpoint{{
			ID: checkpointID, CreatedAt: time.Now().UTC(), Label: "final",
			WorkspaceID: gitResult.Workspace.ID, WorkspaceDigest: gitResult.Workspace.Digest,
			SessionPositions: positions,
		}},
		Objects:      dedupeObjects(objects),
		Security:     secretscan.Summarize(findings),
		Capabilities: capabilities,
	}
	manifestDigest, err := objectStore.PublishRun(run)
	if err != nil {
		return fail("E_PUBLISH", err)
	}
	if err := cat.InsertRun(run, manifestDigest); err != nil {
		return fail("E_CATALOG", err)
	}
	_ = cat.RecordPath(run.Workspaces[0].Repository.ID, machineID, repo)
	report.Status = "success"
	report.StateModified = true
	report.FinishedAt = time.Now().UTC()
	report.DigestExpected = &run.Workspaces[0].Digest
	report.Warnings = append(report.Warnings, run.Workspaces[0].Warnings...)
	if run.Security.Blocked > 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("security scan found %d blocking finding(s); remote push will be refused", run.Security.Blocked))
	}
	reportPath, err := operation.Write(layout, report)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, domain.Result{
			SchemaVersion: 1, OperationID: operationID, Status: "success",
			RunID: runID, Warnings: report.Warnings, ReportPath: reportPath,
		})
	}
	fmt.Fprintf(stdout, "Captured Run %s\n", runID)
	fmt.Fprintf(stdout, "Workspace: %s @ %s\n", repo, gitResult.Workspace.HeadSHA)
	fmt.Fprintf(stdout, "Untracked: %d file(s), %d bytes\n", gitResult.Workspace.UntrackedCount, gitResult.Workspace.UntrackedBytes)
	if len(sessions) > 0 {
		fmt.Fprintf(stdout, "Session: codex/%s\n", sessions[0].NativeID)
	}
	fmt.Fprintf(stdout, "Objects: %d, security: %d block / %d warn\n", len(run.Objects), run.Security.Blocked, run.Security.Warnings)
	fmt.Fprintf(stdout, "Report: %s\n", reportPath)
	return nil
}

func commandList(args []string, cat *catalog.Catalog, stdout, stderr io.Writer) error {
	flags := newFlagSet("list", stderr)
	repo := flags.String("repo", "", "repository ID prefix")
	agent := flags.String("agent", "", "agent platform")
	machine := flags.String("machine", "", "machine ID prefix")
	tag := flags.String("tag", "", "tag")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "json"); err != nil {
		return argumentError(err)
	}
	items, err := cat.List(catalog.Filter{RepoID: *repo, Agent: *agent, Machine: *machine, Tag: *tag})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, map[string]interface{}{"schema_version": 1, "runs": items})
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "RUN\tCREATED\tAGENT\tSTATUS\tTITLE")
	for _, item := range items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			shortID(item.ID), item.CreatedAt.Local().Format("2006-01-02 15:04"),
			item.AgentPlatform, item.IntegrityStatus, item.Title)
	}
	return writer.Flush()
}

func commandShow(args []string, objectStore *store.Store, stdout, stderr io.Writer) error {
	flags := newFlagSet("show", stderr)
	events := flags.Bool("events", false, "include normalized events")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "events", "json"); err != nil {
		return argumentError(err)
	}
	if flags.NArg() != 1 {
		return argumentError(fmt.Errorf("show requires RUN_ID"))
	}
	run, err := objectStore.LoadRun(flags.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOutput {
		result := map[string]interface{}{"schema_version": 1, "run": run}
		if *events {
			var lines []json.RawMessage
			for _, session := range run.Sessions {
				data, err := objectStore.Get(session.NormalizedObject)
				if err != nil {
					return err
				}
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					if line != "" {
						lines = append(lines, json.RawMessage(line))
					}
				}
			}
			result["events"] = lines
		}
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Run: %s\nTitle: %s\nCreated: %s\nRelation: %s\n",
		run.ID, run.Title, run.CreatedAt.Local().Format(time.RFC3339), run.Relation)
	if len(run.Workspaces) > 0 {
		ws := run.Workspaces[0]
		fmt.Fprintf(stdout, "Repository: %s\nHEAD: %s\nBranch: %s\nDirty payload: %d untracked file(s)\n",
			ws.Repository.ID, ws.HeadSHA, ws.Branch, ws.UntrackedCount)
	}
	for _, session := range run.Sessions {
		fmt.Fprintf(stdout, "Session: %s/%s (%s native restore)\n",
			session.Platform, session.NativeID, session.Capabilities.NativeRestore)
	}
	fmt.Fprintf(stdout, "Security: %d block / %d warn / %d info\nObjects: %d\n",
		run.Security.Blocked, run.Security.Warnings, run.Security.Info, len(run.Objects))
	if *events {
		for _, session := range run.Sessions {
			data, err := objectStore.Get(session.NormalizedObject)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, string(data))
		}
	}
	return nil
}

func commandVerify(args []string, cat *catalog.Catalog, objectStore *store.Store, stdout, stderr io.Writer) error {
	flags := newFlagSet("verify", stderr)
	deep := flags.Bool("deep", false, "rehash every object")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "deep", "json"); err != nil {
		return argumentError(err)
	}
	if flags.NArg() != 1 {
		return argumentError(fmt.Errorf("verify requires RUN_ID"))
	}
	run, err := objectStore.LoadRun(flags.Arg(0))
	if err != nil {
		return err
	}
	if err := objectStore.Verify(run, *deep); err != nil {
		_ = cat.UpdateIntegrity(run.ID, "failed")
		return &commandError{exitCode: 6, code: "E_INTEGRITY", message: err.Error()}
	}
	_ = cat.UpdateIntegrity(run.ID, "verified")
	if *jsonOutput {
		return writeJSON(stdout, map[string]interface{}{"schema_version": 1, "status": "verified", "run_id": run.ID, "deep": *deep})
	}
	fmt.Fprintf(stdout, "Run %s verified (%d objects, deep=%t)\n", run.ID, len(run.Objects), *deep)
	return nil
}

func commandRestore(
	ctx context.Context,
	args []string,
	layout home.Layout,
	cat *catalog.Catalog,
	objectStore *store.Store,
	stdout, stderr io.Writer,
) error {
	flags := newFlagSet("restore", stderr)
	repo := flags.String("repo", ".", "existing repository or worktree")
	worktree := flags.String("worktree", "", "new worktree path")
	native := flags.Bool("native-session", false, "attempt experimental native session restore")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "native-session", "json"); err != nil {
		return argumentError(err)
	}
	if flags.NArg() != 1 {
		return argumentError(fmt.Errorf("restore requires RUN_ID"))
	}
	run, err := objectStore.LoadRun(flags.Arg(0))
	if err != nil {
		return err
	}
	if err := objectStore.Verify(run, true); err != nil {
		return &commandError{exitCode: 6, code: "E_INTEGRITY", message: err.Error()}
	}
	operationID, _ := ids.NewPrefixed("op")
	started := time.Now().UTC()
	report := domain.OperationReport{
		SchemaVersion: 1, OperationID: operationID, Operation: "restore",
		Status: "failed", RunID: run.ID, StartedAt: started,
		DigestExpected: &run.Workspaces[0].Digest,
	}
	result, restoreErr := gitx.Restore(ctx, objectStore, run, gitx.RestoreOptions{
		Repo: *repo, Worktree: *worktree, RunID: run.ID,
	})
	report.Target = result.Target
	report.CreatedBranch = result.Branch
	if result.ActualDigest.HeadSHA != "" {
		report.DigestActual = &result.ActualDigest
	}
	if restoreErr != nil {
		report.FinishedAt = time.Now().UTC()
		report.ErrorCode = "E_RESTORE_CONFLICT"
		report.Error = restoreErr.Error()
		report.StateModified = result.Target != ""
		if result.Target != "" {
			report.NextCommand = fmt.Sprintf("git -C %q status", result.Target)
		}
		reportPath, _ := operation.Write(layout, report)
		if *jsonOutput {
			_ = writeJSON(stdout, domain.Result{
				SchemaVersion: 1, OperationID: operationID, Status: "failed", RunID: run.ID,
				Warnings: []string{restoreErr.Error()}, ReportPath: reportPath,
			})
		}
		return &commandError{exitCode: 7, code: "E_RESTORE_CONFLICT", message: restoreErr.Error()}
	}
	handoffPath := filepath.Join(layout.Handoffs, run.ID+".md")
	if _, err := handoff.Render(objectStore, run, result.Target, handoffPath); err != nil {
		report.Warnings = append(report.Warnings, "handoff generation failed: "+err.Error())
	} else {
		report.HandoffPath = handoffPath
	}
	nativeStatus := "unsupported"
	var nativeErr error
	if *native && len(run.Sessions) > 0 && run.Sessions[0].Platform == "codex" {
		nativeStatus, nativeErr = codex.RestoreNative(objectStore, run.Sessions[0], result.Target)
	}
	report.NativeRestore = nativeStatus
	report.Status = "success"
	if nativeErr != nil {
		report.Status = "degraded"
		report.Warnings = append(report.Warnings, nativeErr.Error())
	}
	report.StateModified = true
	report.FinishedAt = time.Now().UTC()
	report.DigestActual = &result.ActualDigest
	if len(run.Sessions) > 0 {
		report.NextCommand = fmt.Sprintf("codex -C %q resume %s", result.Target, run.Sessions[0].NativeID)
	}
	reportPath, err := operation.Write(layout, report)
	if err != nil {
		return err
	}
	machineID, _ := home.LoadOrCreateMachineID(layout)
	_ = cat.RecordPath(run.Workspaces[0].Repository.ID, machineID, result.Target)
	if *jsonOutput {
		_ = writeJSON(stdout, domain.Result{
			SchemaVersion: 1, OperationID: operationID, Status: report.Status,
			RunID: run.ID, Warnings: report.Warnings, ReportPath: reportPath,
		})
	} else {
		fmt.Fprintf(stdout, "Restored Run %s\nWorktree: %s\nBranch: %s\nDigest: verified\n",
			run.ID, result.Target, result.Branch)
		if report.HandoffPath != "" {
			fmt.Fprintf(stdout, "Handoff: %s\n", report.HandoffPath)
		}
		if *native {
			fmt.Fprintf(stdout, "Native session: %s\n", nativeStatus)
		}
		fmt.Fprintf(stdout, "Report: %s\n", reportPath)
	}
	if nativeErr != nil {
		return &commandError{exitCode: 8, code: "E_NATIVE_RESTORE", message: nativeErr.Error() + "; handoff was generated"}
	}
	return nil
}

func commandHandoff(args []string, layout home.Layout, objectStore *store.Store, stdout, stderr io.Writer) error {
	flags := newFlagSet("handoff", stderr)
	targetPlatform := flags.String("to", "generic", "target Agent platform")
	output := flags.String("output", "", "output Markdown path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "json"); err != nil {
		return argumentError(err)
	}
	if flags.NArg() != 1 {
		return argumentError(fmt.Errorf("handoff requires RUN_ID"))
	}
	run, err := objectStore.LoadRun(flags.Arg(0))
	if err != nil {
		return err
	}
	if *output == "" {
		*output = filepath.Join(layout.Handoffs, run.ID+"-"+*targetPlatform+".md")
	}
	path, err := handoff.Render(objectStore, run, "", *output)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, map[string]interface{}{"schema_version": 1, "status": "success", "run_id": run.ID, "output": path, "target_platform": *targetPlatform})
	}
	fmt.Fprintf(stdout, "Handoff written to %s\n", path)
	return nil
}

func commandPush(
	ctx context.Context,
	args []string,
	layout home.Layout,
	cat *catalog.Catalog,
	objectStore *store.Store,
	stdout, stderr io.Writer,
) error {
	flags := newFlagSet("push", stderr)
	storeName := flags.String("store", "", "configured Store name")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "json"); err != nil {
		return argumentError(err)
	}
	if flags.NArg() > 1 {
		return argumentError(fmt.Errorf("push accepts at most one RUN_ID"))
	}
	operationID, _ := ids.NewPrefixed("op")
	report := domain.OperationReport{
		SchemaVersion: 1, OperationID: operationID, Operation: "push",
		Status: "failed", StartedAt: time.Now().UTC(), Details: map[string]string{},
	}
	fail := func(exitCode int, code string, cause error) error {
		report.FinishedAt = time.Now().UTC()
		report.ErrorCode = code
		report.Error = cause.Error()
		reportPath, _ := operation.Write(layout, report)
		if *jsonOutput {
			_ = writeJSON(stdout, domain.Result{
				SchemaVersion: 1, OperationID: operationID, Status: "failed",
				Warnings: []string{cause.Error()}, ReportPath: reportPath,
			})
		}
		return &commandError{exitCode: exitCode, code: code, message: cause.Error()}
	}
	cfg, err := config.Load(layout.Config)
	if err != nil {
		return fail(2, "E_CONFIG", err)
	}
	target, err := cfg.Store(*storeName)
	if err != nil {
		return fail(2, "E_ARGUMENT", err)
	}
	report.Details["store"] = target.Name
	var runIDs []string
	if flags.NArg() == 1 {
		runIDs = []string{flags.Arg(0)}
	} else {
		items, err := cat.List(catalog.Filter{})
		if err != nil {
			return fail(3, "E_CATALOG", err)
		}
		for _, item := range items {
			runIDs = append(runIDs, item.ID)
		}
	}
	var pushed []string
	for _, runID := range runIDs {
		run, err := objectStore.LoadRun(runID)
		if err != nil {
			return fail(6, "E_INTEGRITY", err)
		}
		if err := objectStore.Verify(run, true); err != nil {
			return fail(6, "E_INTEGRITY", err)
		}
		switch target.Type {
		case "file":
			err = syncer.PushFile(layout, objectStore, run, target.URL)
		case "ssh":
			err = syncer.PushSSH(ctx, layout, objectStore, run, target)
			if err != nil && run.Security.Blocked > 0 {
				return fail(5, "E_SECURITY_BLOCK", err)
			}
		default:
			return fail(10, "E_STORE_CAPABILITY", fmt.Errorf("unsupported Store type %s", target.Type))
		}
		if err != nil {
			return fail(9, "E_STORE", err)
		}
		pushed = append(pushed, run.ID)
		_ = cat.RecordSync(target.Name, run.ID, "pushed")
	}
	report.Status = "success"
	report.StateModified = len(pushed) > 0
	report.FinishedAt = time.Now().UTC()
	report.Details["runs"] = strings.Join(pushed, ",")
	reportPath, err := operation.Write(layout, report)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, map[string]interface{}{
			"schema_version": 1, "operation_id": operationID, "status": "success",
			"store": target.Name, "run_ids": pushed, "report_path": reportPath,
		})
	}
	fmt.Fprintf(stdout, "Pushed %d Run(s) to %s\n", len(pushed), target.Name)
	for _, runID := range pushed {
		fmt.Fprintf(stdout, "- %s\n", runID)
	}
	fmt.Fprintf(stdout, "Report: %s\n", reportPath)
	return nil
}

func commandPull(
	ctx context.Context,
	args []string,
	layout home.Layout,
	cat *catalog.Catalog,
	objectStore *store.Store,
	stdout, stderr io.Writer,
) error {
	flags := newFlagSet("pull", stderr)
	storeName := flags.String("store", "", "configured Store name")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "json"); err != nil {
		return argumentError(err)
	}
	if flags.NArg() != 0 {
		return argumentError(fmt.Errorf("pull does not accept positional arguments"))
	}
	operationID, _ := ids.NewPrefixed("op")
	report := domain.OperationReport{
		SchemaVersion: 1, OperationID: operationID, Operation: "pull",
		Status: "failed", StartedAt: time.Now().UTC(), Details: map[string]string{},
	}
	fail := func(exitCode int, code string, cause error) error {
		report.FinishedAt = time.Now().UTC()
		report.ErrorCode = code
		report.Error = cause.Error()
		reportPath, _ := operation.Write(layout, report)
		if *jsonOutput {
			_ = writeJSON(stdout, domain.Result{
				SchemaVersion: 1, OperationID: operationID, Status: "failed",
				Warnings: []string{cause.Error()}, ReportPath: reportPath,
			})
		}
		return &commandError{exitCode: exitCode, code: code, message: cause.Error()}
	}
	cfg, err := config.Load(layout.Config)
	if err != nil {
		return fail(2, "E_CONFIG", err)
	}
	source, err := cfg.Store(*storeName)
	if err != nil {
		return fail(2, "E_ARGUMENT", err)
	}
	report.Details["store"] = source.Name
	var runs []domain.Run
	var digests []string
	switch source.Type {
	case "file":
		runs, digests, err = syncer.PullFile(layout, objectStore, source.URL)
	case "ssh":
		runs, digests, err = syncer.PullSSH(ctx, layout, objectStore, source)
	default:
		return fail(10, "E_STORE_CAPABILITY", fmt.Errorf("unsupported Store type %s", source.Type))
	}
	if err != nil {
		return fail(9, "E_STORE", err)
	}
	for i, run := range runs {
		if err := cat.InsertRun(run, digests[i]); err != nil {
			return fail(3, "E_CATALOG", err)
		}
		_ = cat.RecordSync(source.Name, run.ID, "pulled")
	}
	runIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
	}
	report.Status = "success"
	report.StateModified = len(runs) > 0
	report.FinishedAt = time.Now().UTC()
	report.Details["runs"] = strings.Join(runIDs, ",")
	reportPath, err := operation.Write(layout, report)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, map[string]interface{}{
			"schema_version": 1, "operation_id": operationID, "status": "success",
			"store": source.Name, "run_ids": runIDs, "report_path": reportPath,
		})
	}
	fmt.Fprintf(stdout, "Pulled %d Run(s) from %s\n", len(runs), source.Name)
	for _, runID := range runIDs {
		fmt.Fprintf(stdout, "- %s\n", runID)
	}
	fmt.Fprintf(stdout, "Report: %s\n", reportPath)
	return nil
}

func commandReconcile(args []string, cat *catalog.Catalog, objectStore *store.Store, stdout, stderr io.Writer) error {
	flags := newFlagSet("reconcile", stderr)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args, "json"); err != nil {
		return argumentError(err)
	}
	if flags.NArg() != 1 {
		return argumentError(fmt.Errorf("reconcile requires RUN_ID"))
	}
	run, err := objectStore.LoadRun(flags.Arg(0))
	if err != nil {
		return err
	}
	if err := objectStore.Verify(run, true); err != nil {
		_ = cat.UpdateIntegrity(run.ID, "conflict")
		return &commandError{exitCode: 6, code: "E_RECONCILE_REQUIRED", message: err.Error()}
	}
	_ = cat.UpdateIntegrity(run.ID, "verified")
	if *jsonOutput {
		return writeJSON(stdout, map[string]interface{}{"schema_version": 1, "status": "verified", "run_id": run.ID})
	}
	fmt.Fprintf(stdout, "Run %s has one internally consistent local manifest; no reconciliation is required.\n", run.ID)
	return nil
}

func newFlagSet(name string, output io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	return flags
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	if value == "" {
		return fmt.Errorf("value must not be empty")
	}
	*values = append(*values, value)
	return nil
}

// parseFlags accepts options before or after positional arguments, matching the
// command forms documented by the CLI. The standard flag package stops parsing
// at the first positional argument.
func parseFlags(flags *flag.FlagSet, args []string, boolNames ...string) error {
	boolean := make(map[string]bool, len(boolNames))
	for _, name := range boolNames {
		boolean[name] = true
	}
	var options, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		options = append(options, arg)
		name := strings.TrimLeft(arg, "-")
		if before, _, found := strings.Cut(name, "="); found {
			name = before
			continue
		}
		if boolean[name] {
			continue
		}
		if i+1 < len(args) {
			i++
			options = append(options, args[i])
		}
	}
	return flags.Parse(append(options, positional...))
}

func argumentError(err error) error {
	return &commandError{exitCode: 2, code: "E_ARGUMENT", message: err.Error()}
}

func writeJSON(writer io.Writer, value interface{}) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func dedupeObjects(input []domain.ObjectDescriptor) []domain.ObjectDescriptor {
	byDigest := make(map[string]domain.ObjectDescriptor)
	for _, object := range input {
		byDigest[object.Digest] = object
	}
	result := make([]domain.ObjectDescriptor, 0, len(byDigest))
	for _, object := range byDigest {
		result = append(result, object)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Digest < result[j].Digest })
	return result
}

func rebuildCatalog(cat *catalog.Catalog, objectStore *store.Store) {
	runIDs, err := objectStore.ListRunIDs()
	if err != nil {
		return
	}
	for _, runID := range runIDs {
		run, err := objectStore.LoadRun(runID)
		if err != nil {
			continue
		}
		digest, err := objectStore.ManifestDigest(runID)
		if err != nil {
			continue
		}
		_ = cat.InsertRun(run, digest)
	}
}

func printHelp(writer io.Writer) {
	fmt.Fprintln(writer, `sessionmgr - archive, move, and continue AI coding runs

Usage:
  sessionmgr init
  sessionmgr doctor [--json]
  sessionmgr capture [--repo PATH] [--agent codex]
                     [--session ID | --latest] [--title TITLE]
                     [--untracked include|exclude] [--include-ignored PATTERN] [--json]
  sessionmgr list [--repo ID] [--agent PLATFORM] [--machine ID] [--tag TAG] [--json]
  sessionmgr show RUN_ID [--events] [--json]
  sessionmgr verify RUN_ID [--deep] [--json]
  sessionmgr restore RUN_ID [--repo PATH] [--worktree PATH] [--native-session] [--json]
  sessionmgr handoff RUN_ID [--to PLATFORM] [--output PATH] [--json]
  sessionmgr push [RUN_ID] [--store NAME] [--json]
  sessionmgr pull [--store NAME] [--json]
  sessionmgr reconcile RUN_ID [--json]
  sessionmgr version`)
}
