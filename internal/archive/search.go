package archive

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SearchOptions struct {
	Output, Query, Repository, SessionID, Harness, Device string
	Since, Until                                          time.Time
	Content, IncludeUnknownTimes                          bool
	Limit, Offset                                         int
}

type SearchMatch struct {
	Entry
	CreatedAt         string `json:"created_at,omitempty"`
	CanonicalRemote   string `json:"canonical_remote,omitempty"`
	ActivityStart     string `json:"activity_start,omitempty"`
	ActivityEnd       string `json:"activity_end,omitempty"`
	TimeKnown         bool   `json:"time_known"`
	MatchedIn         string `json:"matched_in"`
	MatchingLines     []int  `json:"matching_lines,omitempty"`
	IntegrityVerified bool   `json:"integrity_verified"`
}

type SearchResult struct {
	SchemaVersion      int           `json:"schema_version"`
	Directory          string        `json:"directory"`
	Query              string        `json:"query"`
	Total              int           `json:"total"`
	Offset             int           `json:"offset"`
	Limit              int           `json:"limit"`
	HasMore            bool          `json:"has_more"`
	UnknownTimeSkipped int           `json:"unknown_time_skipped"`
	Matches            []SearchMatch `json:"matches"`
}

// Search reads exported metadata first. Content search is explicit and refuses
// a broad archive scan or an oversized candidate set before reading bodies.
func Search(ctx context.Context, opts SearchOptions) (SearchResult, error) {
	result := SearchResult{SchemaVersion: 1, Query: opts.Query, Offset: opts.Offset, Limit: opts.Limit, Matches: []SearchMatch{}}
	if opts.Limit == 0 {
		opts.Limit = 20
		result.Limit = opts.Limit
	}
	if opts.Limit < 1 || opts.Limit > 1000 || opts.Offset < 0 {
		return result, fmt.Errorf("limit must be 1..1000 and offset nonnegative")
	}
	if !opts.Since.IsZero() && !opts.Until.IsZero() && opts.Until.Before(opts.Since) {
		return result, fmt.Errorf("until is earlier than since")
	}
	if opts.Content && (strings.TrimSpace(opts.Query) == "" || (opts.Repository == "" && opts.SessionID == "" && (opts.Since.IsZero() || opts.Until.IsZero()))) {
		return result, fmt.Errorf("content search needs a query and a repository, session, or bounded since/until window")
	}
	root, err := filepath.Abs(opts.Output)
	if err != nil {
		return result, err
	}
	result.Directory = root
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return result, fmt.Errorf("archive directory is unavailable: %s", root)
	}
	entries, err := List(ListOptions{Output: root})
	if err != nil {
		return result, err
	}
	remotes := map[string]string{}
	{
		directories, err := discoverRepositoryDirectories(root)
		if err != nil {
			return result, err
		}
		for _, directory := range directories {
			var record repositoryMetadata
			if err := readMetadata(filepath.Join(directory, repositoryMetadataName), &record); err != nil {
				return result, err
			}
			remotes[record.RepositoryKey] = record.CanonicalRemote
		}
	}
	candidates := []SearchMatch{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if opts.SessionID != "" && entry.SessionID != opts.SessionID {
			continue
		}
		if opts.Harness != "" && !strings.EqualFold(entry.Harness, opts.Harness) {
			continue
		}
		if opts.Device != "" && !strings.EqualFold(entry.DeviceID, opts.Device) && !strings.EqualFold(entry.DeviceName, opts.Device) {
			continue
		}
		remote := remotes[entry.RepositoryKey]
		if !repositoryMatches(entry, remote, opts.Repository) {
			continue
		}
		match := SearchMatch{Entry: entry, CanonicalRemote: remote, MatchedIn: "metadata"}
		if !entry.Legacy {
			var record sessionMetadata
			if err := readMetadata(filepath.Join(filepath.Dir(entry.Path), sessionMetadataName), &record); err != nil {
				return result, err
			}
			match.CreatedAt = record.CreatedAt
		}
		start, end := parseTimestamp(match.CreatedAt), parseTimestamp(entry.UpdatedAt)
		match.TimeKnown = !start.IsZero() && !end.IsZero() && !end.Before(start)
		if match.TimeKnown {
			match.ActivityStart, match.ActivityEnd = formatTime(start), formatTime(end)
		}
		if !opts.Since.IsZero() || !opts.Until.IsZero() {
			if !match.TimeKnown {
				if !opts.IncludeUnknownTimes {
					result.UnknownTimeSkipped++
					continue
				}
			} else if (!opts.Since.IsZero() && end.Before(opts.Since)) || (!opts.Until.IsZero() && start.After(opts.Until)) {
				continue
			}
		}
		candidates = append(candidates, match)
	}
	if opts.Content && len(candidates) > 200 {
		return result, fmt.Errorf("content scope has %d candidates; narrow it to at most 200", len(candidates))
	}
	matches := []SearchMatch{}
	for _, match := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		metadata := strings.Join([]string{match.Title, match.RepositoryName, match.CanonicalRemote, match.SessionID, match.SessionKey, match.Harness, match.DeviceID, match.DeviceName}, "\n")
		if containsQuery(metadata, opts.Query) {
			matches = append(matches, match)
			continue
		}
		if !opts.Content {
			continue
		}
		file, err := verifiedDocument(ctx, root, match.Entry)
		if err != nil {
			return result, err
		}
		lines, err := contentMatchLines(ctx, file, opts.Query)
		file.Close()
		if err != nil {
			return result, err
		}
		if len(lines) > 0 {
			match.MatchedIn = "content"
			match.IntegrityVerified = true
			match.MatchingLines = lines
			matches = append(matches, match)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := parseTimestamp(matches[i].UpdatedAt), parseTimestamp(matches[j].UpdatedAt)
		if !a.Equal(b) {
			return a.After(b)
		}
		return matches[i].Path < matches[j].Path
	})
	result.Total = len(matches)
	start := min(opts.Offset, len(matches))
	end := min(start+opts.Limit, len(matches))
	result.Matches = matches[start:end]
	result.HasMore = end < len(matches)
	return result, nil
}

func repositoryMatches(entry Entry, remote, filter string) bool {
	if filter == "" {
		return true
	}
	if strings.EqualFold(filter, entry.RepositoryKey) || strings.EqualFold(filter, entry.RepositoryName) || strings.EqualFold(filter, remote) {
		return true
	}
	if normalized, ok := NormalizeRemote(filter); ok {
		return strings.EqualFold(normalized, remote)
	}
	return remote != "" && strings.Contains(filter, "/") && strings.HasSuffix(strings.ToLower(remote), "/"+strings.ToLower(strings.Trim(filter, "/")))
}

func containsQuery(value, query string) bool {
	value = strings.ToLower(value)
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(value, term) {
			return false
		}
	}
	return true
}

func contentMatchLines(ctx context.Context, file *os.File, query string) ([]int, error) {
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(query))
	found := make([]bool, len(terms))
	lines := []int{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for number := 1; scanner.Scan(); number++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.ToLower(scanner.Text())
		hit := false
		for index, term := range terms {
			if strings.Contains(line, term) {
				found[index] = true
				hit = true
			}
		}
		if hit && len(lines) < 20 {
			lines = append(lines, number)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("archive document changed during content search")
	}
	current, err := os.Lstat(file.Name())
	if err != nil || !os.SameFile(before, current) {
		return nil, fmt.Errorf("archive document was replaced during content search")
	}
	for _, hit := range found {
		if !hit {
			return nil, nil
		}
	}
	return lines, nil
}

type ShowOptions struct {
	Output, SessionID, SessionKey, Harness, Device, Path string
	FromLine, ToLine                                     int
}
type EvidenceLine struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}
type EvidenceResult struct {
	SchemaVersion   int            `json:"schema_version"`
	Session         Entry          `json:"session"`
	Verified        bool           `json:"verified"`
	SnapshotThrough string         `json:"snapshot_through,omitempty"`
	FromLine        int            `json:"from_line"`
	ToLine          int            `json:"to_line"`
	Truncated       bool           `json:"truncated"`
	Lines           []EvidenceLine `json:"lines"`
}

// Show validates a selected archive document before returning bounded lines.
// It does not interpret user-authored Markdown headings as message boundaries.
func Show(ctx context.Context, opts ShowOptions) (EvidenceResult, error) {
	result := EvidenceResult{SchemaVersion: 1, Lines: []EvidenceLine{}}
	if opts.SessionID == "" && opts.SessionKey == "" && opts.Path == "" {
		return result, fmt.Errorf("show requires --session, --key, or --path")
	}
	if opts.FromLine == 0 {
		opts.FromLine = 1
	}
	if opts.ToLine == 0 {
		opts.ToLine = opts.FromLine + 199
	}
	if opts.FromLine < 1 || opts.ToLine < opts.FromLine || opts.ToLine-opts.FromLine >= 500 {
		return result, fmt.Errorf("show needs a positive range of at most 500 lines")
	}
	root, err := filepath.Abs(opts.Output)
	if err != nil {
		return result, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return result, fmt.Errorf("archive directory is unavailable: %s", root)
	}
	entries, err := List(ListOptions{Output: root})
	if err != nil {
		return result, err
	}
	selected := []Entry{}
	for _, entry := range entries {
		if opts.SessionID != "" && entry.SessionID != opts.SessionID {
			continue
		}
		if opts.SessionKey != "" && entry.SessionKey != opts.SessionKey {
			continue
		}
		if opts.Harness != "" && !strings.EqualFold(entry.Harness, opts.Harness) {
			continue
		}
		if opts.Device != "" && !strings.EqualFold(entry.DeviceID, opts.Device) && !strings.EqualFold(entry.DeviceName, opts.Device) {
			continue
		}
		if opts.Path != "" {
			path := opts.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
			if filepath.Clean(entry.Path) != filepath.Clean(path) {
				continue
			}
		}
		selected = append(selected, entry)
	}
	if len(selected) != 1 {
		return result, fmt.Errorf("show matched %d sessions; select one exact key/path or add harness/device", len(selected))
	}
	result.Session = selected[0]
	file, err := verifiedDocument(ctx, root, selected[0])
	if err != nil {
		return result, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return result, err
	}
	result.SnapshotThrough, err = readSnapshotThrough(file)
	if err != nil {
		return result, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	bytes := 0
	for number := 1; scanner.Scan(); number++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		line := scanner.Text()
		if number < opts.FromLine {
			continue
		}
		if len(line) > 64*1024 && len(result.Lines) == 0 {
			return result, fmt.Errorf("selected line %d exceeds 64 KiB; inspect the verified local document directly", number)
		}
		if number > opts.ToLine || bytes+len(line) > 64*1024 {
			result.Truncated = true
			break
		}
		result.Lines = append(result.Lines, EvidenceLine{Number: number, Text: line})
		bytes += len(line)
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return result, fmt.Errorf("archive document changed while reading")
	}
	current, err := os.Lstat(file.Name())
	if err != nil || !os.SameFile(before, current) {
		return result, fmt.Errorf("archive document was replaced while reading")
	}
	result.Verified = true
	if len(result.Lines) > 0 {
		result.FromLine = result.Lines[0].Number
		result.ToLine = result.Lines[len(result.Lines)-1].Number
	}
	return result, nil
}

func readSnapshotThrough(file *os.File) (string, error) {
	scanner := bufio.NewScanner(io.LimitReader(file, 64*1024))
	scanner.Buffer(make([]byte, 16*1024), 64*1024)
	if !scanner.Scan() || scanner.Text() != "---" {
		return "", scanner.Err()
	}
	value := ""
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			return value, nil
		}
		if strings.HasPrefix(line, "last_message_at:") {
			value = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "last_message_at:")), "\"")
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("archive frontmatter exceeds 64 KiB or is incomplete")
}

func verifiedDocument(ctx context.Context, root string, entry Entry) (*os.File, error) {
	if entry.DocumentHash == "" {
		return nil, fmt.Errorf("session has no verifiable document hash")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(entry.Path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("archive document is outside its directory")
	}
	info, err := os.Lstat(entry.Path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("archive document is not a regular file")
	}
	file, err := os.Open(entry.Path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) { file.Close(); return nil, err }
	opened, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !os.SameFile(info, opened) {
		return fail(fmt.Errorf("archive document was replaced"))
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fail(readErr)
		}
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != entry.DocumentHash {
		return fail(fmt.Errorf("document hash mismatch; refusing to use modified session: %s", entry.Path))
	}
	after, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return fail(fmt.Errorf("archive document changed during verification"))
	}
	current, err := os.Lstat(entry.Path)
	if err != nil || !os.SameFile(opened, current) {
		return fail(fmt.Errorf("archive document was replaced during verification"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return file, nil
}
