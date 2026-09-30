package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sessionmgr/sessionmgr/internal/archive"
	"github.com/sessionmgr/sessionmgr/internal/config"
)

func commandSearch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("search", stderr)
	directory := flags.String("directory", "", "archive to search (one-time; does not change config)")
	output := flags.String("output", "", "alias for --directory")
	query := flags.String("query", "", "case-insensitive query; terms are ANDed")
	repo := flags.String("repo", "", "repository name, remote, key, or local Git checkout")
	session := flags.String("session", "", "exact native session ID")
	harness := flags.String("harness", "", "exact harness name")
	device := flags.String("device", "", "exact device ID or name")
	since := flags.String("since", "", "activity overlap lower bound: RFC3339 or local YYYY-MM-DD")
	until := flags.String("until", "", "activity overlap upper bound: RFC3339 or inclusive local YYYY-MM-DD")
	zone := flags.String("timezone", "", "time zone for date-only bounds (default: system local)")
	content := flags.Bool("content", false, "search verified document content within a bounded metadata scope")
	unknown := flags.Bool("include-unknown-times", false, "include unknown activity intervals when filtering dates")
	limit := flags.Int("limit", 20, "maximum results (1..1000)")
	offset := flags.Int("offset", 0, "result offset")
	jsonOutput := flags.Bool("json", false, "emit structured metadata without conversation text")
	if err := parseQueryFlags(flags, args); err != nil {
		return flagError(err)
	}
	if flags.NArg() > 0 {
		if *query != "" {
			return argumentError("use positional query or --query, not both")
		}
		*query = strings.Join(flags.Args(), " ")
	}
	root, err := queryDirectory(*directory, *output)
	if err != nil {
		return err
	}
	repository := *repo
	if repository == "." || repository == ".." || filepath.IsAbs(repository) || strings.HasPrefix(repository, "./") || strings.HasPrefix(repository, "../") || strings.HasPrefix(repository, "~") {
		path, err := config.ResolvePath(repository)
		if err != nil {
			return err
		}
		identity, err := archive.RepositoryFromPath(ctx, path)
		if err != nil {
			return err
		}
		repository = identity.Key
	}
	location := time.Local
	if *zone != "" {
		location, err = time.LoadLocation(*zone)
		if err != nil {
			return argumentError("invalid timezone: " + *zone)
		}
	}
	start, err := parseSearchTime(*since, false, location)
	if err != nil {
		return argumentError(err.Error())
	}
	end, err := parseSearchTime(*until, true, location)
	if err != nil {
		return argumentError(err.Error())
	}
	result, err := archive.Search(ctx, archive.SearchOptions{
		Output: root, Query: *query, Repository: repository, SessionID: *session, Harness: *harness, Device: *device,
		Since: start, Until: end, Content: *content, IncludeUnknownTimes: *unknown, Limit: *limit, Offset: *offset,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, result)
	}
	if result.Total == 0 {
		fmt.Fprintln(stdout, "No matching exported sessions.")
	} else {
		table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "REPOSITORY\tHARNESS\tUPDATED\tTITLE\tDEVICE\tSESSION\tPATH")
		for _, match := range result.Matches {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", match.RepositoryName, match.Harness, match.UpdatedAt, oneLine(match.Title), oneLine(match.DeviceName), match.SessionID, match.Path)
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	if result.HasMore {
		fmt.Fprintf(stderr, "Showing %d of %d matches; continue with --offset %d.\n", len(result.Matches), result.Total, result.Offset+len(result.Matches))
	}
	if result.UnknownTimeSkipped > 0 {
		fmt.Fprintf(stderr, "Excluded %d unknown activity interval(s); --include-unknown-times includes them explicitly.\n", result.UnknownTimeSkipped)
	}
	return nil
}

func commandShow(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("show", stderr)
	directory := flags.String("directory", "", "archive directory (one-time)")
	output := flags.String("output", "", "alias for --directory")
	session := flags.String("session", "", "exact native session ID")
	key := flags.String("key", "", "exact archive session key")
	path := flags.String("path", "", "selected conversation.md path, absolute or archive-relative")
	harness := flags.String("harness", "", "exact harness name")
	device := flags.String("device", "", "exact device ID or name")
	from := flags.Int("from-line", 1, "first line to return")
	to := flags.Int("to-line", 0, "last line (default: first line plus 199; max 500 lines)")
	jsonOutput := flags.Bool("json", false, "emit verified metadata and numbered lines")
	if err := parseQueryFlags(flags, args); err != nil {
		return flagError(err)
	}
	if flags.NArg() != 0 {
		return argumentError("show does not accept positional arguments")
	}
	root, err := queryDirectory(*directory, *output)
	if err != nil {
		return err
	}
	selectedPath := *path
	if strings.HasPrefix(selectedPath, "~") {
		selectedPath, err = config.ResolvePath(selectedPath)
		if err != nil {
			return err
		}
	}
	result, err := archive.Show(ctx, archive.ShowOptions{Output: root, SessionID: *session, SessionKey: *key, Path: selectedPath, Harness: *harness, Device: *device, FromLine: *from, ToLine: *to})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Verified %s · %s · %s\n", result.Session.Harness, oneLine(result.Session.Title), result.Session.Path)
	for _, line := range result.Lines {
		fmt.Fprintf(stdout, "%d\t%s\n", line.Number, line.Text)
	}
	if result.Truncated {
		fmt.Fprintf(stderr, "Excerpt truncated; continue with --from-line %d.\n", result.ToLine+1)
	}
	return nil
}

func queryDirectory(directory, output string) (string, error) {
	if directory != "" && output != "" {
		return "", argumentError("--directory and --output are mutually exclusive")
	}
	if directory == "" {
		directory = output
	}
	store, err := config.DefaultStore()
	if err != nil {
		return "", err
	}
	return store.ResolveDirectory(directory, false)
}

func parseSearchTime(value string, upper bool, location *time.Location) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", value, location); err == nil {
		if upper {
			parsed = parsed.AddDate(0, 0, 1).Add(-time.Nanosecond)
		}
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q; use RFC3339 with offset or YYYY-MM-DD", value)
}

// The query can precede or follow flags (smg search topic --directory PATH).
// Values remain separate arguments and never pass through shell interpolation.
func parseQueryFlags(flags *flag.FlagSet, args []string) error {
	options, positionals := []string{}, []string{}
	for index := 0; index < len(args); index++ {
		value := args[index]
		if value == "--" {
			positionals = append(positionals, args[index+1:]...)
			break
		}
		if value == "-" || !strings.HasPrefix(value, "-") {
			positionals = append(positionals, value)
			continue
		}
		options = append(options, value)
		name, _, hasValue := strings.Cut(strings.TrimLeft(value, "-"), "=")
		definition := flags.Lookup(name)
		if hasValue || definition == nil {
			continue
		}
		boolean, ok := definition.Value.(interface{ IsBoolFlag() bool })
		if ok && boolean.IsBoolFlag() {
			continue
		}
		if index+1 < len(args) {
			index++
			options = append(options, args[index])
		}
	}
	return flags.Parse(append(append(options, "--"), positionals...))
}
