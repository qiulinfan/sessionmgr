package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os/exec"
	pathpkg "path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

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

func repositoryDirectory(repo Repository) string {
	hexKey := strings.TrimPrefix(repo.Key, "sha256:")
	return slug(repo.Name) + "--" + hexKey
}

func slug(value string) string {
	value = strings.TrimSpace(value)
	var result []rune
	dash := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result = append(result, unicode.ToLower(r))
			dash = false
			continue
		}
		if !dash && len(result) > 0 {
			result = append(result, '-')
			dash = true
		}
	}
	result = []rune(strings.Trim(string(result), "-"))
	if len(result) == 0 {
		return "repository"
	}
	bytes := 0
	limit := len(result)
	for index, r := range result {
		width := utf8.RuneLen(r)
		if bytes+width > 80 {
			limit = index
			break
		}
		bytes += width
	}
	return strings.TrimRight(string(result[:limit]), "-")
}
