package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/sessionmgr/sessionmgr/internal/archive"
	"github.com/sessionmgr/sessionmgr/internal/config"
	"github.com/sessionmgr/sessionmgr/internal/ui"
)

// version is a variable so release builds can stamp the reviewed tag version
// with -ldflags -X. Development builds keep an explicit prerelease suffix.
var version = "1.4.0"

type commandError struct {
	exitCode int
	message  string
}

func (e *commandError) Error() string { return e.message }

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		args = []string{"open"}
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintf(stdout, "sessionmgr %s\n", version)
		return 0, nil
	}
	var err error
	switch args[0] {
	case "help", "-h", "--help":
		printHelp(stdout)
	case "export", "archive":
		err = commandExport(ctx, args[1:], stdout, stderr)
	case "config":
		err = commandConfig(args[1:], stdout, stderr)
	case "list":
		err = commandList(args[1:], stdout, stderr)
	case "search":
		err = commandSearch(ctx, args[1:], stdout, stderr)
	case "show":
		err = commandShow(ctx, args[1:], stdout, stderr)
	case "cleanup-internal":
		err = commandCleanupInternal(ctx, args[1:], stdout, stderr)
	case "gui":
		err = commandGUI(ctx, args[1:], stdout, stderr)
	case "open":
		err = commandOpen(ctx, args[1:], stdout, stderr)
	case "install-cli":
		err = commandInstallCLI(args[1:], stdout, stderr)
	default:
		printHelp(stderr)
		err = &commandError{exitCode: 2, message: "unknown command " + args[0]}
	}
	if err == nil {
		return 0, nil
	}
	var typed *commandError
	if errors.As(err, &typed) {
		return typed.exitCode, typed
	}
	return 1, err
}

func commandCleanupInternal(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("cleanup-internal", stderr)
	directory := flags.String("directory", "", "archive directory (default: configured directory)")
	output := flags.String("output", "", "compatibility alias for --directory")
	source := flags.String("codex-home", "", "Codex state directory (default: CODEX_HOME or ~/.codex)")
	apply := flags.Bool("apply", false, "remove verified internal session documents (default: dry run)")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return flagError(err)
	}
	if flags.NArg() != 0 {
		return argumentError("cleanup-internal does not accept positional arguments")
	}
	if *directory != "" && *output != "" {
		return argumentError("--directory and --output are mutually exclusive")
	}
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	override := *directory
	if override == "" {
		override = *output
	}
	resolvedDirectory, err := store.ResolveDirectory(override, false)
	if err != nil {
		return err
	}
	device, err := store.EnsureDevice()
	if err != nil {
		return err
	}
	result, cleanupErr := archive.CleanupInternal(ctx, archive.CleanupOptions{
		CodexHome: *source, Output: resolvedDirectory, DeviceID: device.DeviceID, Apply: *apply,
	})
	if *jsonOutput {
		if err := writeJSON(stdout, result); err != nil {
			return err
		}
	} else {
		if err := printCleanupChanges(stdout, result); err != nil {
			return err
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(stderr, "warning: %s\n", warning)
		}
	}
	return cleanupErr
}

func commandExport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("export", stderr)
	repo := flags.String("repo", ".", "export only sessions for this Git repository or included local directory")
	all := flags.Bool("all", false, "export sessions for every eligible directory (hosted Git by default)")
	sessionID := flags.String("session", "", "export one native session ID")
	includeArchived := flags.Bool("include-archived", false, "also export Codex archived sessions")
	includeNonGit := flags.Bool("include-non-git", false, "also fully export sessions from directories without a hosted Git remote")
	fullScan := flags.Bool("full-scan", false, "rescan all sessions instead of using the local export checkpoint")
	source := flags.String("codex-home", "", "Codex state directory (default: CODEX_HOME or ~/.codex)")
	claudeSource := flags.String("claude-home", "", "Claude Code state directory (default: CLAUDE_CONFIG_DIR or ~/.claude)")
	deepSeekSource := flags.String("deepseek-home", "", "DeepSeek Harness state directory (default: DSH_HOME or ~/.dsh)")
	ompSource := flags.String("omp-home", "", "Oh My Pi agent directory (default: PI_CODING_AGENT_DIR or ~/.omp/agent)")
	ompSessionDir := flags.String("omp-session-dir", "", "Oh My Pi session directory (default: PI_CODING_AGENT_SESSION_DIR or <omp-home>/sessions)")
	openCodeDB := flags.String("opencode-db", "", "OpenCode session database (default: XDG_DATA_HOME/opencode/opencode.db)")
	sourceNames := flags.String("sources", "", "comma-separated harnesses (default: saved selection or all supported sources)")
	directory := flags.String("directory", "", "export directory to use and remember")
	output := flags.String("output", "", "one-time export directory (compatibility alias)")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return flagError(err)
	}
	if flags.NArg() != 0 {
		return argumentError("export does not accept positional arguments")
	}
	repoWasSet := flagWasSet(flags, "repo")
	if *all && repoWasSet {
		return argumentError("--all and --repo are mutually exclusive")
	}
	for _, path := range []*string{source, claudeSource, deepSeekSource, ompSource, ompSessionDir, openCodeDB} {
		if *path != "" {
			resolved, err := config.ResolvePath(*path)
			if err != nil {
				return err
			}
			*path = resolved
		}
	}
	if repoWasSet {
		resolved, err := config.ResolvePath(*repo)
		if err != nil {
			return err
		}
		*repo = resolved
	}
	if *directory != "" && *output != "" {
		return argumentError("--directory and --output are mutually exclusive")
	}
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	resolvedDirectory := ""
	switch {
	case *directory != "":
		resolvedDirectory, err = store.ResolveDirectory(*directory, true)
	case *output != "":
		resolvedDirectory, err = store.ResolveDirectory(*output, false)
	default:
		resolvedDirectory, err = store.ResolveDirectory("", false)
	}
	if err != nil {
		return err
	}
	device, err := store.EnsureDevice()
	if err != nil {
		return err
	}
	selection := archive.SourceSelection{Codex: true, ClaudeCode: true, DeepSeek: true, OMP: true, OpenCode: true}
	if device.SourcePreferences != nil {
		selection = archive.SourceSelection{
			Codex: device.SourcePreferences.Codex, ClaudeCode: device.SourcePreferences.ClaudeCode,
			DeepSeek: device.SourcePreferences.DeepSeek, OMP: device.SourcePreferences.OMP,
			OpenCode: device.SourcePreferences.OpenCode,
		}
	}
	if *sourceNames != "" {
		selection, err = parseSourceNames(*sourceNames)
		if err != nil {
			return argumentError(err.Error())
		}
	}
	result, exportErr := archive.Export(ctx, archive.Options{
		CodexHome: *source, ClaudeHome: *claudeSource, DeepSeekHome: *deepSeekSource,
		OMPHome: *ompSource, OMPSessionDir: *ompSessionDir, OpenCodeDB: *openCodeDB,
		Output: resolvedDirectory, Repo: *repo,
		AllRepos: !repoWasSet || *all, SessionID: *sessionID,
		IncludeArchived: *includeArchived,
		IncludeNonGit:   *includeNonGit,
		Sources:         &selection,
		CheckpointPath:  store.ExportStatePath(), FullScan: *fullScan,
		DeviceID: device.DeviceID, DeviceName: device.DeviceName,
	})
	if *jsonOutput {
		if err := writeJSON(stdout, result); err != nil {
			return err
		}
	} else {
		if err := printChanges(stdout, result.Changes); err != nil {
			return err
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(stderr, "warning: %s\n", warning)
		}
	}
	return exportErr
}

func commandConfig(args []string, stdout, stderr io.Writer) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "show" {
		flags := newFlagSet("config show", stderr)
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if len(args) > 0 {
			args = args[1:]
		}
		if err := flags.Parse(args); err != nil {
			return flagError(err)
		}
		if flags.NArg() != 0 {
			return argumentError("config show does not accept positional arguments")
		}
		value, err := store.Load()
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeJSON(stdout, map[string]interface{}{
				"schema_version": value.SchemaVersion,
				"config_path":    store.Path,
				"directory":      value.ExportDirectory,
			})
		}
		if value.ExportDirectory == "" {
			fmt.Fprintln(stdout, "Export directory is not configured.")
		} else {
			fmt.Fprintln(stdout, value.ExportDirectory)
		}
		return nil
	}
	if args[0] != "set-directory" {
		return argumentError("usage: sessionmgr config set-directory PATH")
	}
	flags := newFlagSet("config set-directory", stderr)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return flagError(err)
	}
	if flags.NArg() != 1 {
		return argumentError("config set-directory requires exactly one path")
	}
	value, err := store.SetExportDirectory(flags.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, map[string]interface{}{
			"schema_version": value.SchemaVersion,
			"config_path":    store.Path,
			"directory":      value.ExportDirectory,
		})
	}
	fmt.Fprintln(stdout, value.ExportDirectory)
	return nil
}

func commandList(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("list", stderr)
	directory := flags.String("directory", "", "archive directory (default: configured directory)")
	output := flags.String("output", "", "compatibility alias for --directory")
	history := flags.Bool("history", false, "also show legacy immutable snapshots")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return flagError(err)
	}
	if flags.NArg() != 0 {
		return argumentError("list does not accept positional arguments")
	}
	if *directory != "" && *output != "" {
		return argumentError("--directory and --output are mutually exclusive")
	}
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	override := *directory
	if override == "" {
		override = *output
	}
	resolvedDirectory, err := store.ResolveDirectory(override, false)
	if err != nil {
		return err
	}
	entries, err := archive.List(archive.ListOptions{Output: resolvedDirectory, History: *history})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, map[string]interface{}{
			"schema_version": archive.SchemaVersion,
			"sessions":       entries,
		})
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No exported sessions.")
		return nil
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "REPOSITORY\tHARNESS\tUPDATED\tTITLE\tDEVICE\tSESSION")
	for _, entry := range entries {
		device := entry.DeviceName
		harness := entry.Harness
		if entry.Legacy {
			device = "legacy"
			harness = "legacy"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", entry.RepositoryName, harness, entry.UpdatedAt,
			oneLine(entry.Title), oneLine(device), short(entry.SessionID))
	}
	return table.Flush()
}

func commandGUI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("gui", stderr)
	listen := flags.String("listen", "127.0.0.1:0", "loopback address for the local GUI")
	noOpen := flags.Bool("no-open", false, "do not open the default browser")
	source := flags.String("codex-home", "", "Codex state directory")
	claudeSource := flags.String("claude-home", "", "Claude Code state directory")
	deepSeekSource := flags.String("deepseek-home", "", "DeepSeek Harness state directory")
	ompSource := flags.String("omp-home", "", "Oh My Pi agent directory")
	ompSessionDir := flags.String("omp-session-dir", "", "Oh My Pi session directory")
	openCodeDB := flags.String("opencode-db", "", "OpenCode session database")
	repo := flags.String("repo", ".", "current Git repository for the GUI scope")
	if err := flags.Parse(args); err != nil {
		return flagError(err)
	}
	if flags.NArg() != 0 {
		return argumentError("gui does not accept positional arguments")
	}
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	return ui.Run(ctx, ui.Options{
		Listen: *listen, CodexHome: *source, ClaudeHome: *claudeSource, DeepSeekHome: *deepSeekSource,
		OMPHome: *ompSource, OMPSessionDir: *ompSessionDir, OpenCodeDB: *openCodeDB, Repo: *repo,
		OpenBrowser: !*noOpen, ConfigStore: store, Log: stderr,
		Ready: func(url string) {
			fmt.Fprintf(stdout, "Session Manager GUI: %s\n", url)
			fmt.Fprintln(stdout, "Press Ctrl-C to stop.")
		},
	})
}

func printChanges(output io.Writer, changes []archive.Change) error {
	if len(changes) == 0 {
		_, err := fmt.Fprintln(output, "No changes.")
		return err
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "CHANGE\tREPOSITORY\tTITLE\tDEVICE")
	for _, change := range changes {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", strings.ToUpper(change.Kind),
			change.RepositoryName, oneLine(change.Title), oneLine(change.DeviceName))
	}
	return table.Flush()
}

func printCleanupChanges(output io.Writer, result archive.CleanupResult) error {
	if len(result.Changes) == 0 {
		_, err := fmt.Fprintln(output, "No internal sessions eligible for cleanup.")
		return err
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ACTION\tREPOSITORY\tTITLE\tDEVICE\tREASON")
	for _, change := range result.Changes {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", strings.ToUpper(change.Kind),
			change.RepositoryName, oneLine(change.Title), oneLine(change.DeviceName), change.Reason)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if result.DryRun {
		_, err := fmt.Fprintf(output, "Dry run: %d internal session(s) would be removed. Re-run with --apply to remove them.\n", result.Candidates)
		return err
	}
	_, err := fmt.Fprintf(output, "Removed %d internal session(s).\n", result.Removed)
	return err
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	result := flag.NewFlagSet(name, flag.ContinueOnError)
	result.SetOutput(stderr)
	return result
}

func flagWasSet(flags *flag.FlagSet, name string) bool {
	result := false
	flags.Visit(func(item *flag.Flag) {
		if item.Name == name {
			result = true
		}
	})
	return result
}

func flagError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return &commandError{exitCode: 2, message: err.Error()}
}

func argumentError(message string) error {
	return &commandError{exitCode: 2, message: message}
}

func writeJSON(output io.Writer, value interface{}) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func short(value string) string {
	value = filepath.Base(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func parseSourceNames(value string) (archive.SourceSelection, error) {
	var result archive.SourceSelection
	seen := make(map[string]bool)
	for _, item := range strings.Split(value, ",") {
		name := strings.TrimSpace(item)
		if name == "" || seen[name] {
			return result, fmt.Errorf("invalid or repeated source %q", name)
		}
		seen[name] = true
		switch name {
		case "codex":
			result.Codex = true
		case "claude-code":
			result.ClaudeCode = true
		case "deepseek":
			result.DeepSeek = true
		case "omp":
			result.OMP = true
		case "opencode":
			result.OpenCode = true
		default:
			return result, fmt.Errorf("unsupported source %q", name)
		}
	}
	return result, nil
}

func printHelp(output io.Writer) {
	fmt.Fprintln(output, `sessionmgr exports Codex, Claude Code, DeepSeek Harness, Oh My Pi, and OpenCode conversations as readable Markdown files.

Usage:
  smg                                Open the macOS app when installed, otherwise the browser UI
  smg --version
  smg install-cli [--directory PATH]  Install the smg command (default: ~/.local/bin)
  smg gui [--no-open] [--claude-home PATH] [--deepseek-home PATH]
                 [--omp-home PATH] [--omp-session-dir PATH] [--opencode-db PATH]
  smg config set-directory PATH
  smg config show
  smg export [--all | --repo PATH] [--session ID] [--sources codex,claude-code,deepseek,omp,opencode]
                    [--omp-home PATH] [--omp-session-dir PATH] [--opencode-db PATH]
                    [--include-archived] [--include-non-git] [--full-scan] [--directory PATH]
  smg list [--history]
  smg search [QUERY] [--directory PATH] [--repo NAME|REMOTE|PATH] [--session ID]
                    [--harness NAME] [--device ID|NAME] [--since TIME] [--until TIME]
                    [--timezone ZONE] [--content] [--limit N] [--offset N] [--json]
  smg show (--session ID | --key KEY | --path PATH) [--directory PATH]
           [--harness NAME] [--device ID|NAME] [--from-line N] [--to-line N] [--json]
  smg cleanup-internal [--directory PATH] [--apply]
  smg version

smg and sessionmgr use the same commands, configuration, and export checkpoints.
Available session stores for all five supported harnesses are scanned automatically.
The configured export directory persists across launches. Output lists
only files changed by the current operation. "archive" remains an alias for
"export". Later exports use local scan history with a one-hour overlap;
--full-scan checks every session again. cleanup-internal is a dry run unless
--apply is provided. search is read-only metadata retrieval; --content requires
a narrow scope. show verifies document hashes before returning bounded lines.
For one-time export paths use --output; --directory remembers the export path.
Search/show directory overrides are always one-time and never save config.`)
}
