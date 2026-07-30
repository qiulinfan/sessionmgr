package gitx

import (
	"testing"

	"github.com/sessionmgr/sessionmgr/internal/domain"
)

func TestNormalizeRemote(t *testing.T) {
	tests := map[string]string{
		"git@GitHub.COM:owner/repo.git":             "ssh://github.com/owner/repo",
		"https://user:token@GitHub.COM/owner/repo/": "https://github.com/owner/repo",
		"ssh://git@Example.com/team/repo.git":       "ssh://example.com/team/repo",
	}
	for input, want := range tests {
		if got := NormalizeRemote(input); got != want {
			t.Errorf("NormalizeRemote(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestValidateRelativePath(t *testing.T) {
	valid := []string{"file.txt", "dir/file.txt", "unicode/文件.txt"}
	for _, path := range valid {
		if err := ValidateRelativePath(path); err != nil {
			t.Errorf("valid path %q rejected: %v", path, err)
		}
	}
	invalid := []string{"", "../secret", "/absolute", "dir/../file", ".git/config", "dir/.git/index"}
	for _, path := range invalid {
		if err := ValidateRelativePath(path); err == nil {
			t.Errorf("unsafe path %q accepted", path)
		}
	}
}

func TestUntrackedTreeDigestIsOrderIndependent(t *testing.T) {
	a := domain.UntrackedEntry{Path: "a", Mode: 0o644, Digest: "sha256:a"}
	b := domain.UntrackedEntry{Path: "b", Mode: 0o755, Digest: "sha256:b"}
	first := UntrackedTreeDigest(domain.UntrackedManifest{Entries: []domain.UntrackedEntry{a, b}})
	second := UntrackedTreeDigest(domain.UntrackedManifest{Entries: []domain.UntrackedEntry{b, a}})
	if first != second {
		t.Fatalf("tree digest depends on input order: %s != %s", first, second)
	}
}
