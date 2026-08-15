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

const version = "0.6.0-dev"

type commandError struct {
	exitCode int
	message  string
}

func (e *commandError) Error() string { return e.message }

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		args = []string{"gui"}
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
	case "cleanup-internal":
		err = commandCleanupInternal(ctx, args[1:], stdout, stderr)
	case "gui":
		err = commandGUI(ctx, args[1:], stdout, stderr)
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
	includeDeepSeek := flags.Bool("include-deepseek", false, "also export DeepSeek Harness sessions")
	includeNonGit := flags.Bool("include-non-git", false, "also fully export sessions from directories without a hosted Git remote")
	source := flags.String("codex-home", "", "Codex state directory (default: CODEX_HOME or ~/.codex)")
	deepSeekSource := flags.String("deepseek-home", "", "DeepSeek Harness state directory (default: DSH_HOME or ~/.dsh)")
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
	result, exportErr := archive.Export(ctx, archive.Options{
		CodexHome: *source, DeepSeekHome: *deepSeekSource,
		Output: resolvedDirectory, Repo: *repo,
		AllRepos: !repoWasSet || *all, SessionID: *sessionID,
		IncludeArchived: *includeArchived,
		IncludeDeepSeek: *includeDeepSeek,
		IncludeNonGit:   *includeNonGit,
		DeviceID:        device.DeviceID, DeviceName: device.DeviceName,
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
	deepSeekSource := flags.String("deepseek-home", "", "DeepSeek Harness state directory")
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
		Listen: *listen, CodexHome: *source, DeepSeekHome: *deepSeekSource, Repo: *repo,
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

func printHelp(output io.Writer) {
	fmt.Fprintln(output, `sessionmgr exports Codex and DeepSeek Harness conversations as readable Markdown files.

Usage:
  sessionmgr                         Open the GUI
  sessionmgr gui [--no-open] [--deepseek-home PATH]
  sessionmgr config set-directory PATH
  sessionmgr config show
  sessionmgr export [--all | --repo PATH] [--session ID] [--include-archived] [--include-deepseek] [--include-non-git] [--directory PATH]
  sessionmgr list [--history]
  sessionmgr cleanup-internal [--directory PATH] [--apply]
  sessionmgr version

The configured export directory persists across launches. Export output lists
only files changed by the current operation. "archive" remains an alias for
"export". cleanup-internal is a dry run unless --apply is provided.`)
}
