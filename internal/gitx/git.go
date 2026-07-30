package gitx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sessionmgr/sessionmgr/internal/canonical"
	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/ids"
	"github.com/sessionmgr/sessionmgr/internal/secretscan"
	"github.com/sessionmgr/sessionmgr/internal/store"
)

type CaptureOptions struct {
	Repo             string
	RunID            string
	IncludeUntracked bool
	IncludeIgnored   []string
	MaxFileBytes     int64
	MaxTotalBytes    int64
	TmpDir           string
}

type CaptureResult struct {
	Workspace domain.WorkspaceSnapshot
	Objects   []domain.ObjectDescriptor
	Findings  []domain.SecurityFinding
}

func Capture(ctx context.Context, objectStore *store.Store, opts CaptureOptions) (CaptureResult, error) {
	repo, err := TopLevel(ctx, opts.Repo)
	if err != nil {
		return CaptureResult{}, err
	}
	head, err := output(ctx, repo, nil, "rev-parse", "HEAD")
	if err != nil {
		return CaptureResult{}, fmt.Errorf("repository must have an initial commit: %w", err)
	}
	rootCommit, err := rootCommit(ctx, repo)
	if err != nil {
		return CaptureResult{}, err
	}
	commonDir, err := output(ctx, repo, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return CaptureResult{}, err
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Clean(filepath.Join(repo, commonDir))
	}
	if _, err := os.Stat(filepath.Join(commonDir, "index.lock")); err == nil {
		return CaptureResult{}, fmt.Errorf("git index is locked: %s", filepath.Join(commonDir, "index.lock"))
	}

	branch, branchErr := output(ctx, repo, nil, "symbolic-ref", "--short", "-q", "HEAD")
	detached := branchErr != nil || branch == ""
	upstream, _ := output(ctx, repo, nil, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	base := rootCommit
	if upstream != "" {
		if mergeBase, mergeErr := output(ctx, repo, nil, "merge-base", head, upstream); mergeErr == nil {
			base = mergeBase
		}
	}
	remote, _ := output(ctx, repo, nil, "config", "--get", "remote.origin.url")
	canonicalRemote := NormalizeRemote(remote)
	repoID := RepositoryID(canonicalRemote, rootCommit)
	gitVersion, _ := output(ctx, repo, nil, "--version")

	staged, err := outputBytes(ctx, repo, nil, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff")
	if err != nil {
		return CaptureResult{}, fmt.Errorf("capture staged diff: %w", err)
	}
	unstaged, err := outputBytes(ctx, repo, nil, "diff", "--binary", "--full-index", "--no-ext-diff")
	if err != nil {
		return CaptureResult{}, fmt.Errorf("capture unstaged diff: %w", err)
	}
	stagedDesc, err := objectStore.PutBytes(staged, "application/vnd.sessionmgr.git-patch.staged", true)
	if err != nil {
		return CaptureResult{}, err
	}
	unstagedDesc, err := objectStore.PutBytes(unstaged, "application/vnd.sessionmgr.git-patch.unstaged", true)
	if err != nil {
		return CaptureResult{}, err
	}
	objects := []domain.ObjectDescriptor{stagedDesc, unstagedDesc}
	findings := append(secretscan.Scan("git:staged.patch", staged), secretscan.Scan("git:unstaged.patch", unstaged)...)
	findings = append(findings, secretscan.Scan("git:remote.origin.url", []byte(remote))...)

	bundleDigest := ""
	needsBundle := upstream == ""
	if upstream != "" {
		if err := run(ctx, repo, nil, "merge-base", "--is-ancestor", head, upstream); err != nil {
			needsBundle = true
		}
	}
	if needsBundle {
		bundlePath := filepath.Join(opts.TmpDir, opts.RunID+".bundle")
		ref := "refs/sessionmgr/capture/" + opts.RunID
		if err := run(ctx, repo, nil, "update-ref", ref, head); err != nil {
			return CaptureResult{}, fmt.Errorf("create temporary capture ref: %w", err)
		}
		defer run(context.Background(), repo, nil, "update-ref", "-d", ref)
		if err := run(ctx, repo, nil, "bundle", "create", bundlePath, ref); err != nil {
			return CaptureResult{}, fmt.Errorf("create git bundle: %w", err)
		}
		bundleDesc, err := objectStore.PutFile(bundlePath, "application/vnd.git.bundle", true)
		os.Remove(bundlePath)
		if err != nil {
			return CaptureResult{}, err
		}
		bundleDigest = bundleDesc.Digest
		objects = append(objects, bundleDesc)
	}

	untrackedManifest, untrackedObjects, untrackedFindings, totalBytes, err :=
		captureUntracked(ctx, repo, objectStore, opts)
	if err != nil {
		return CaptureResult{}, err
	}
	objects = append(objects, untrackedObjects...)
	findings = append(findings, untrackedFindings...)
	manifestBytes, err := canonical.Marshal(untrackedManifest)
	if err != nil {
		return CaptureResult{}, err
	}
	manifestDesc, err := objectStore.PutBytes(manifestBytes, "application/vnd.sessionmgr.untracked-manifest.v1+json", true)
	if err != nil {
		return CaptureResult{}, err
	}
	objects = append(objects, manifestDesc)
	if opts.MaxTotalBytes > 0 {
		var payloadBytes int64
		for _, desc := range dedupeDescriptors(objects) {
			payloadBytes += desc.Size
		}
		if payloadBytes > opts.MaxTotalBytes {
			return CaptureResult{}, fmt.Errorf("workspace payload is %d bytes; limit is %d", payloadBytes, opts.MaxTotalBytes)
		}
	}

	workspaceID, err := ids.NewPrefixed("ws")
	if err != nil {
		return CaptureResult{}, err
	}
	operationState := detectOperationState(commonDir)
	warnings := repositoryWarnings(ctx, repo)
	workspace := domain.WorkspaceSnapshot{
		ID:               workspaceID,
		VCSType:          "git",
		Repository:       domain.RepositoryIdentity{ID: repoID, CanonicalRemote: canonicalRemote, RootCommit: rootCommit},
		SourcePathHint:   repo,
		GitCommonDirHint: commonDir,
		Branch:           branch,
		HeadSHA:          head,
		UpstreamRef:      upstream,
		BaseSHA:          base,
		IsDetached:       detached,
		IsShallow:        boolOutput(ctx, repo, "rev-parse", "--is-shallow-repository"),
		IsPartialClone:   boolConfig(ctx, repo, "remote.origin.promisor"),
		IsSparseCheckout: boolConfig(ctx, repo, "core.sparseCheckout"),
		OperationState:   operationState,
		Payload: domain.WorkspacePayload{
			CommitBundleObject:      bundleDigest,
			StagedPatchObject:       stagedDesc.Digest,
			UnstagedPatchObject:     unstagedDesc.Digest,
			UntrackedManifestObject: manifestDesc.Digest,
		},
		Digest: domain.WorkspaceDigest{
			HeadSHA:          head,
			StagedPatchSHA:   store.Digest(staged),
			UnstagedPatchSHA: store.Digest(unstaged),
			UntrackedTreeSHA: UntrackedTreeDigest(untrackedManifest),
		},
		UntrackedCount: len(untrackedManifest.Entries),
		UntrackedBytes: totalBytes,
		Warnings:       warnings,
	}
	_ = gitVersion
	return CaptureResult{Workspace: workspace, Objects: dedupeDescriptors(objects), Findings: findings}, nil
}

func TopLevel(ctx context.Context, path string) (string, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	top, err := output(ctx, abs, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not a Git worktree", abs)
	}
	return filepath.Clean(top), nil
}

func GitVersion(ctx context.Context, repo string) string {
	version, _ := output(ctx, repo, nil, "--version")
	return version
}

func NormalizeRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	if !strings.Contains(remote, "://") {
		if at := strings.LastIndex(remote, "@"); at >= 0 {
			if colon := strings.Index(remote[at:], ":"); colon >= 0 {
				host := remote[at+1 : at+colon]
				path := remote[at+colon+1:]
				remote = "ssh://" + host + "/" + path
			}
		}
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Host == "" {
		return strings.TrimSuffix(strings.TrimRight(remote, "/"), ".git")
	}
	parsed.User = nil
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = "/" + strings.TrimLeft(parsed.Path, "/")
	parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), ".git")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func RepositoryID(canonicalRemote, rootCommit string) string {
	prefix := "git-local\x00"
	if canonicalRemote != "" {
		prefix = "git\x00" + canonicalRemote + "\x00"
	}
	sum := sha256.Sum256([]byte(prefix + rootCommit))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func UntrackedTreeDigest(manifest domain.UntrackedManifest) string {
	entries := append([]domain.UntrackedEntry(nil), manifest.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	hash := sha256.New()
	for _, entry := range entries {
		fmt.Fprintf(hash, "%s\x00%d\x00%s\x00", entry.Path, entry.Mode, entry.Digest)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func DigestWorkspace(ctx context.Context, repo string) (domain.WorkspaceDigest, error) {
	head, err := output(ctx, repo, nil, "rev-parse", "HEAD")
	if err != nil {
		return domain.WorkspaceDigest{}, err
	}
	staged, err := outputBytes(ctx, repo, nil, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff")
	if err != nil {
		return domain.WorkspaceDigest{}, err
	}
	unstaged, err := outputBytes(ctx, repo, nil, "diff", "--binary", "--full-index", "--no-ext-diff")
	if err != nil {
		return domain.WorkspaceDigest{}, err
	}
	manifest, _, err := inspectUntracked(ctx, repo)
	if err != nil {
		return domain.WorkspaceDigest{}, err
	}
	return domain.WorkspaceDigest{
		HeadSHA: head, StagedPatchSHA: store.Digest(staged),
		UnstagedPatchSHA: store.Digest(unstaged),
		UntrackedTreeSHA: UntrackedTreeDigest(manifest),
	}, nil
}

func captureUntracked(ctx context.Context, repo string, objectStore *store.Store, opts CaptureOptions) (
	domain.UntrackedManifest, []domain.ObjectDescriptor, []domain.SecurityFinding, int64, error,
) {
	manifest := domain.UntrackedManifest{SchemaVersion: domain.SchemaVersion, Entries: []domain.UntrackedEntry{}}
	if !opts.IncludeUntracked && len(opts.IncludeIgnored) == 0 {
		return manifest, nil, nil, 0, nil
	}
	paths, err := untrackedPaths(ctx, repo, opts.IncludeUntracked, opts.IncludeIgnored)
	if err != nil {
		return manifest, nil, nil, 0, err
	}
	var objects []domain.ObjectDescriptor
	var findings []domain.SecurityFinding
	var total int64
	for _, relative := range paths {
		if err := ValidateRelativePath(relative); err != nil {
			return manifest, nil, nil, total, err
		}
		full := filepath.Join(repo, filepath.FromSlash(relative))
		info, err := os.Lstat(full)
		if err != nil {
			return manifest, nil, nil, total, err
		}
		var data []byte
		entry := domain.UntrackedEntry{Path: relative, Mode: uint32(info.Mode()), Size: info.Size()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(full)
			if err != nil {
				return manifest, nil, nil, total, err
			}
			data = []byte(target)
			entry.LinkTarget = target
			entry.Size = int64(len(data))
		case info.Mode().IsRegular():
			if opts.MaxFileBytes > 0 && info.Size() > opts.MaxFileBytes {
				return manifest, nil, nil, total, fmt.Errorf("untracked file %s is %d bytes; limit is %d", relative, info.Size(), opts.MaxFileBytes)
			}
			data, err = os.ReadFile(full)
			if err != nil {
				return manifest, nil, nil, total, err
			}
		default:
			return manifest, nil, nil, total, fmt.Errorf("unsupported untracked file type: %s", relative)
		}
		total += int64(len(data))
		if opts.MaxTotalBytes > 0 && total > opts.MaxTotalBytes {
			return manifest, nil, nil, total, fmt.Errorf("untracked payload is %d bytes; limit is %d", total, opts.MaxTotalBytes)
		}
		desc, err := objectStore.PutBytes(data, "application/octet-stream", true)
		if err != nil {
			return manifest, nil, nil, total, err
		}
		entry.Digest = desc.Digest
		manifest.Entries = append(manifest.Entries, entry)
		objects = append(objects, desc)
		findings = append(findings, secretscan.Scan("untracked:"+relative, data)...)
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	return manifest, objects, findings, total, nil
}

func inspectUntracked(ctx context.Context, repo string) (domain.UntrackedManifest, int64, error) {
	manifest := domain.UntrackedManifest{SchemaVersion: domain.SchemaVersion, Entries: []domain.UntrackedEntry{}}
	paths, err := untrackedPaths(ctx, repo, true, nil)
	if err != nil {
		return manifest, 0, err
	}
	var total int64
	for _, relative := range paths {
		if err := ValidateRelativePath(relative); err != nil {
			return manifest, total, err
		}
		full := filepath.Join(repo, filepath.FromSlash(relative))
		info, err := os.Lstat(full)
		if err != nil {
			return manifest, total, err
		}
		var data []byte
		entry := domain.UntrackedEntry{Path: relative, Mode: uint32(info.Mode()), Size: info.Size()}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return manifest, total, err
			}
			data = []byte(target)
			entry.LinkTarget = target
			entry.Size = int64(len(data))
		} else if info.Mode().IsRegular() {
			data, err = os.ReadFile(full)
			if err != nil {
				return manifest, total, err
			}
		} else {
			return manifest, total, fmt.Errorf("unsupported untracked file type: %s", relative)
		}
		entry.Digest = store.Digest(data)
		total += int64(len(data))
		manifest.Entries = append(manifest.Entries, entry)
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	return manifest, total, nil
}

func untrackedPaths(ctx context.Context, repo string, includeUntracked bool, includeIgnored []string) ([]string, error) {
	var raw []byte
	if includeUntracked {
		var err error
		raw, err = outputBytes(ctx, repo, nil, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, err
		}
	}
	for _, pattern := range includeIgnored {
		if pattern == "" {
			return nil, fmt.Errorf("ignored include pattern must not be empty")
		}
		extra, err := outputBytes(ctx, repo, nil,
			"ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--", pattern)
		if err != nil {
			return nil, err
		}
		raw = append(raw, extra...)
	}
	parts := bytes.Split(raw, []byte{0})
	pathSet := make(map[string]bool)
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		if !utf8.Valid(part) {
			return nil, fmt.Errorf("non-UTF-8 Git path is not supported in MVP")
		}
		pathSet[filepath.ToSlash(string(part))] = true
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func ValidateRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "/") {
		return fmt.Errorf("unsafe path %q", path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("unsafe path %q", path)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".git" {
			return fmt.Errorf("path conflicts with .git: %q", path)
		}
	}
	return nil
}

func rootCommit(ctx context.Context, repo string) (string, error) {
	raw, err := output(ctx, repo, nil, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return "", err
	}
	roots := strings.Fields(raw)
	if len(roots) == 0 {
		return "", fmt.Errorf("repository has no root commit")
	}
	sort.Strings(roots)
	return roots[0], nil
}

func detectOperationState(commonDir string) string {
	candidates := []struct {
		path, state string
	}{
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(commonDir, candidate.path)); err == nil {
			return candidate.state
		}
	}
	return ""
}

func repositoryWarnings(ctx context.Context, repo string) []string {
	var warnings []string
	if raw, err := output(ctx, repo, nil, "submodule", "status", "--recursive"); err == nil && raw != "" {
		warnings = append(warnings, "repository contains submodules; dirty submodule contents are not captured")
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitattributes")); err == nil {
		if raw, _ := os.ReadFile(filepath.Join(repo, ".gitattributes")); bytes.Contains(raw, []byte("filter=lfs")) {
			warnings = append(warnings, "repository may use Git LFS; local LFS object availability is not guaranteed")
		}
	}
	return warnings
}

func boolOutput(ctx context.Context, repo string, args ...string) bool {
	value, err := output(ctx, repo, nil, args...)
	return err == nil && strings.EqualFold(value, "true")
}

func boolConfig(ctx context.Context, repo, key string) bool {
	value, err := output(ctx, repo, nil, "config", "--bool", "--get", key)
	return err == nil && strings.EqualFold(value, "true")
}

func dedupeDescriptors(input []domain.ObjectDescriptor) []domain.ObjectDescriptor {
	seen := make(map[string]bool)
	result := make([]domain.ObjectDescriptor, 0, len(input))
	for _, item := range input {
		if seen[item.Digest] {
			continue
		}
		seen[item.Digest] = true
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Digest < result[j].Digest })
	return result
}

func run(ctx context.Context, repo string, stdin []byte, args ...string) error {
	_, err := outputBytes(ctx, repo, stdin, args...)
	return err
}

func output(ctx context.Context, repo string, stdin []byte, args ...string) (string, error) {
	raw, err := outputBytes(ctx, repo, stdin, args...)
	return strings.TrimSpace(string(raw)), err
}

func outputBytes(ctx context.Context, repo string, stdin []byte, args ...string) ([]byte, error) {
	allArgs := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", allArgs...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=0")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return stdout.Bytes(), nil
}

func parseMode(raw string) uint32 {
	value, _ := strconv.ParseUint(raw, 8, 32)
	return uint32(value)
}
