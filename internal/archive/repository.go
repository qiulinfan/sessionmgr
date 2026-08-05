package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const repositoryKindLocalDirectory = "local_directory"

var scpRemote = regexp.MustCompile(`^(?:[^@/]+@)?([^/:]+):(.+)$`)

func RepositoryFromPath(ctx context.Context, path string) (Repository, error) {
	root, err := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("%s is not a Git worktree", path)
	}
	remote, _ := gitOutput(ctx, root, "config", "--get", "remote.origin.url")
	if canonical, portable := NormalizeRemote(remote); portable {
		return repositoryFromRemote(canonical), nil
	}
	return Repository{}, fmt.Errorf("Git repository has no hosted remote: %s", root)
}

func repositoryForSession(ctx context.Context, session Session) (Repository, error) {
	if canonical, portable := NormalizeRemote(session.Remote); portable {
		return repositoryFromRemote(canonical), nil
	}
	if session.CWD == "" {
		return Repository{}, fmt.Errorf("session %s has no hosted Git remote", session.ID)
	}
	return RepositoryFromPath(ctx, session.CWD)
}

func repositoryFromRemote(canonical string) Repository {
	key := digest("git-remote-v1\x00" + canonical)
	name, _ := redact(pathpkg.Base(canonical))
	canonical, _ = redact(canonical)
	return Repository{
		Key:             key,
		Name:            name,
		CanonicalRemote: canonical,
	}
}

func localDirectoryRepositoryFromPath(path, deviceID, deviceName string) (Repository, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Repository{}, fmt.Errorf("local directory is missing")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Repository{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Repository{}, fmt.Errorf("inspect local directory: %w", err)
	}
	if !info.IsDir() {
		return Repository{}, fmt.Errorf("local path is not a directory: %s", absolute)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = resolved
	}
	absolute = filepath.Clean(absolute)
	directoryName := filepath.Base(absolute)
	if directoryName == "." || directoryName == string(filepath.Separator) || strings.TrimSpace(directoryName) == "" {
		directoryName = "directory"
	}
	displayName, _ := redact("non-git:" + directoryName)
	directoryName, _ = redact(directoryName)
	directoryID := digest("local-directory-path-v1\x00" + absolute)
	return Repository{
		Key:           digest("local-directory-v1\x00" + deviceID + "\x00" + directoryID),
		Name:          displayName,
		Kind:          repositoryKindLocalDirectory,
		DirectoryName: directoryName,
		DirectoryID:   directoryID,
		DeviceID:      deviceID,
		DeviceName:    deviceName,
	}, nil
}

// NormalizeRemote removes transport and credentials so SSH and HTTPS clones of
// the same hosted repository share one key. Local-path remotes return false.
func NormalizeRemote(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", false
	}
	if match := scpRemote.FindStringSubmatch(remote); len(match) == 3 && !strings.Contains(remote, "://") {
		if len(match[1]) == 1 && (strings.HasPrefix(match[2], "/") || strings.HasPrefix(match[2], `\`)) {
			return "", false
		}
		canonical := cleanHostedRemote(match[1], match[2])
		return canonical, canonical != ""
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Host == "" || parsed.Scheme == "file" {
		return "", false
	}
	canonical := cleanHostedRemote(parsed.Host, parsed.Path)
	return canonical, canonical != ""
}

func cleanHostedRemote(host, path string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	path = strings.Trim(strings.TrimSpace(path), "/")
	path = strings.TrimSuffix(path, ".git")
	if host == "" || path == "" {
		return ""
	}
	return host + "/" + path
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir)
	cmd.Args = append(cmd.Args, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func digest(value string) string {
	return digestBytes([]byte(value))
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// semanticComponent keeps the visible archive useful to humans while staying
// portable across macOS, Linux, and Windows. It is not an identity function;
// hidden metadata detects the rare case where two identities normalize to the
// same visible path.
func semanticComponent(value, fallback string) string {
	value = strings.TrimSpace(value)
	var result []rune
	dash := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			result = append(result, unicode.ToLower(r))
			dash = false
			continue
		}
		if !dash && len(result) > 0 {
			result = append(result, '-')
			dash = true
		}
	}
	clean := strings.Trim(string(result), " .-_")
	if clean == "" || clean == "." || clean == ".." {
		clean = fallback
	}
	reservedStem, _, _ := strings.Cut(strings.ToLower(clean), ".")
	if windowsReservedNames[reservedStem] {
		clean = "_" + clean
	}
	runes := []rune(clean)
	bytes := 0
	limit := len(runes)
	for index, r := range runes {
		width := utf8.RuneLen(r)
		if bytes+width > 80 {
			limit = index
			break
		}
		bytes += width
	}
	clean = strings.TrimRight(string(runes[:limit]), " .")
	if clean == "" {
		return fallback
	}
	return clean
}
