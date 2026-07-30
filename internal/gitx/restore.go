package gitx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/store"
)

type RestoreOptions struct {
	Repo     string
	Worktree string
	RunID    string
}

type RestoreResult struct {
	Target       string
	Branch       string
	ActualDigest domain.WorkspaceDigest
}

func Restore(ctx context.Context, objectStore *store.Store, capsule domain.Run, opts RestoreOptions) (RestoreResult, error) {
	if len(capsule.Workspaces) != 1 {
		return RestoreResult{}, fmt.Errorf("MVP restore requires exactly one workspace")
	}
	workspace := capsule.Workspaces[0]
	repo, err := TopLevel(ctx, opts.Repo)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("locate target repository: %w", err)
	}
	target := opts.Worktree
	short := capsule.ID
	if len(short) > 12 {
		short = short[:12]
	}
	if target == "" {
		target = filepath.Join(filepath.Dir(repo), ".sessionmgr-worktrees", filepath.Base(repo), short)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return RestoreResult{}, err
	}
	if target == repo || isInside(target, repo) {
		return RestoreResult{}, fmt.Errorf("restore target must not be inside the source worktree: %s", target)
	}
	if info, statErr := os.Stat(target); statErr == nil {
		if !info.IsDir() {
			return RestoreResult{}, fmt.Errorf("restore target exists and is not a directory: %s", target)
		}
		entries, readErr := os.ReadDir(target)
		if readErr != nil {
			return RestoreResult{}, readErr
		}
		if len(entries) > 0 {
			return RestoreResult{}, fmt.Errorf("restore target is not empty: %s", target)
		}
		if removeErr := os.Remove(target); removeErr != nil {
			return RestoreResult{}, removeErr
		}
	} else if !os.IsNotExist(statErr) {
		return RestoreResult{}, statErr
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return RestoreResult{}, err
	}

	if err := runGitQuiet(ctx, repo, "cat-file", "-e", workspace.HeadSHA+"^{commit}"); err != nil {
		if workspace.Payload.CommitBundleObject == "" {
			return RestoreResult{}, fmt.Errorf("HEAD %s is unavailable and Capsule has no commit bundle", workspace.HeadSHA)
		}
		bundle, openErr := objectStore.Open(workspace.Payload.CommitBundleObject)
		if openErr != nil {
			return RestoreResult{}, openErr
		}
		bundlePath := bundle.Name()
		bundle.Close()
		captureRef := "refs/sessionmgr/capture/" + capsule.ID
		importRef := "refs/sessionmgr/import/" + capsule.ID
		if fetchErr := run(ctx, repo, nil, "fetch", bundlePath, captureRef+":"+importRef); fetchErr != nil {
			return RestoreResult{}, fmt.Errorf("import commit bundle: %w", fetchErr)
		}
		if err := runGitQuiet(ctx, repo, "cat-file", "-e", workspace.HeadSHA+"^{commit}"); err != nil {
			return RestoreResult{}, fmt.Errorf("bundle did not provide captured HEAD %s", workspace.HeadSHA)
		}
	}

	branch := "sessionmgr/restore/" + short
	if err := runGitQuiet(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return RestoreResult{}, fmt.Errorf("restore branch already exists: %s", branch)
	}
	if err := run(ctx, repo, nil, "worktree", "add", "-b", branch, target, workspace.HeadSHA); err != nil {
		return RestoreResult{}, fmt.Errorf("create restore worktree: %w", err)
	}

	staged, err := objectStore.Get(workspace.Payload.StagedPatchObject)
	if err != nil {
		return RestoreResult{Target: target, Branch: branch}, err
	}
	if len(staged) > 0 {
		if err := run(ctx, target, staged, "apply", "--index", "--binary", "-"); err != nil {
			return RestoreResult{Target: target, Branch: branch}, fmt.Errorf("apply staged patch: %w", err)
		}
	}
	unstaged, err := objectStore.Get(workspace.Payload.UnstagedPatchObject)
	if err != nil {
		return RestoreResult{Target: target, Branch: branch}, err
	}
	if len(unstaged) > 0 {
		if err := run(ctx, target, unstaged, "apply", "--binary", "-"); err != nil {
			return RestoreResult{Target: target, Branch: branch}, fmt.Errorf("apply unstaged patch: %w", err)
		}
	}
	untrackedManifest, err := restoreUntracked(objectStore, target, workspace.Payload.UntrackedManifestObject)
	if err != nil {
		return RestoreResult{Target: target, Branch: branch}, err
	}
	actual, err := digestWorkspaceWithManifest(ctx, target, untrackedManifest)
	if err != nil {
		return RestoreResult{Target: target, Branch: branch}, err
	}
	if actual != workspace.Digest {
		return RestoreResult{Target: target, Branch: branch, ActualDigest: actual},
			fmt.Errorf("restored workspace digest mismatch")
	}
	return RestoreResult{Target: target, Branch: branch, ActualDigest: actual}, nil
}

func restoreUntracked(objectStore *store.Store, target, manifestDigest string) (domain.UntrackedManifest, error) {
	raw, err := objectStore.Get(manifestDigest)
	if err != nil {
		return domain.UntrackedManifest{}, err
	}
	var manifest domain.UntrackedManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return domain.UntrackedManifest{}, fmt.Errorf("decode untracked manifest: %w", err)
	}
	if manifest.SchemaVersion != domain.SchemaVersion {
		return domain.UntrackedManifest{}, fmt.Errorf("unsupported untracked manifest schema %d", manifest.SchemaVersion)
	}
	folded := make(map[string]string)
	for _, entry := range manifest.Entries {
		if err := ValidateRelativePath(entry.Path); err != nil {
			return domain.UntrackedManifest{}, err
		}
		key := strings.ToLower(entry.Path)
		if other, ok := folded[key]; ok && other != entry.Path {
			return domain.UntrackedManifest{}, fmt.Errorf("case-folding path collision: %s and %s", other, entry.Path)
		}
		folded[key] = entry.Path
		full := filepath.Join(target, filepath.FromSlash(entry.Path))
		if !isInside(full, target) {
			return domain.UntrackedManifest{}, fmt.Errorf("untracked path escapes target: %s", entry.Path)
		}
		if _, err := os.Lstat(full); err == nil {
			return domain.UntrackedManifest{}, fmt.Errorf("restore conflict: target file exists: %s", entry.Path)
		} else if !os.IsNotExist(err) {
			return domain.UntrackedManifest{}, err
		}
		if entry.LinkTarget != "" {
			if err := validateLinkTarget(entry.Path, entry.LinkTarget); err != nil {
				return domain.UntrackedManifest{}, err
			}
		}
	}
	for _, entry := range manifest.Entries {
		full := filepath.Join(target, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return domain.UntrackedManifest{}, err
		}
		data, err := objectStore.Get(entry.Digest)
		if err != nil {
			return domain.UntrackedManifest{}, err
		}
		if store.Digest(data) != entry.Digest {
			return domain.UntrackedManifest{}, fmt.Errorf("untracked object checksum mismatch: %s", entry.Digest)
		}
		if entry.LinkTarget != "" {
			if string(data) != entry.LinkTarget {
				return domain.UntrackedManifest{}, fmt.Errorf("symlink object does not match manifest: %s", entry.Path)
			}
			if err := os.Symlink(entry.LinkTarget, full); err != nil {
				return domain.UntrackedManifest{}, err
			}
			continue
		}
		mode := os.FileMode(entry.Mode).Perm()
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(full, data, mode); err != nil {
			return domain.UntrackedManifest{}, err
		}
	}
	return manifest, nil
}

func digestWorkspaceWithManifest(ctx context.Context, repo string, expected domain.UntrackedManifest) (domain.WorkspaceDigest, error) {
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
	actual := domain.UntrackedManifest{SchemaVersion: domain.SchemaVersion}
	for _, expectedEntry := range expected.Entries {
		full := filepath.Join(repo, filepath.FromSlash(expectedEntry.Path))
		info, err := os.Lstat(full)
		if err != nil {
			return domain.WorkspaceDigest{}, err
		}
		entry := domain.UntrackedEntry{Path: expectedEntry.Path, Mode: uint32(info.Mode()), Size: info.Size()}
		var data []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return domain.WorkspaceDigest{}, err
			}
			data = []byte(target)
			entry.LinkTarget = target
			entry.Size = int64(len(data))
		} else if info.Mode().IsRegular() {
			data, err = os.ReadFile(full)
			if err != nil {
				return domain.WorkspaceDigest{}, err
			}
		} else {
			return domain.WorkspaceDigest{}, fmt.Errorf("unsupported restored file type: %s", entry.Path)
		}
		entry.Digest = store.Digest(data)
		actual.Entries = append(actual.Entries, entry)
	}
	return domain.WorkspaceDigest{
		HeadSHA: head, StagedPatchSHA: store.Digest(staged),
		UnstagedPatchSHA: store.Digest(unstaged),
		UntrackedTreeSHA: UntrackedTreeDigest(actual),
	}, nil
}

func validateLinkTarget(path, target string) error {
	if filepath.IsAbs(target) {
		return fmt.Errorf("symlink %s has absolute target", path)
	}
	resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(path)), target)))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("symlink %s escapes restored worktree", path)
	}
	return nil
}

func isInside(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func runGitQuiet(ctx context.Context, repo string, args ...string) error {
	cmd := append([]string{"-C", repo}, args...)
	process := exec.CommandContext(ctx, "git", cmd...)
	process.Env = append(os.Environ(), "LC_ALL=C")
	return process.Run()
}
