package syncer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"filippo.io/age"

	"github.com/sessionmgr/sessionmgr/internal/capsule"
	"github.com/sessionmgr/sessionmgr/internal/config"
	"github.com/sessionmgr/sessionmgr/internal/cryptox"
	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/home"
	"github.com/sessionmgr/sessionmgr/internal/store"
)

func PushFile(localLayout home.Layout, localStore *store.Store, run domain.Run, destination string) error {
	root, err := localPath(destination)
	if err != nil {
		return err
	}
	remoteLayout, err := home.ForRoot(root)
	if err != nil {
		return err
	}
	if remoteLayout.Root == localLayout.Root {
		return nil
	}
	if err := home.Ensure(remoteLayout); err != nil {
		return err
	}
	remoteStore := store.New(remoteLayout)
	for _, desc := range run.Objects {
		source, err := localStore.Open(desc.Digest)
		if err != nil {
			return err
		}
		imported, err := remoteStore.PutFile(source.Name(), desc.MediaType, desc.Required)
		source.Close()
		if err != nil {
			return err
		}
		if imported.Digest != desc.Digest || imported.Size != desc.Size {
			return fmt.Errorf("copied object %s failed verification", desc.Digest)
		}
	}
	_, err = remoteStore.PublishRun(run)
	if err != nil {
		return err
	}
	return remoteStore.Verify(run, true)
}

func PullFile(localLayout home.Layout, localStore *store.Store, source string) ([]domain.Run, []string, error) {
	root, err := localPath(source)
	if err != nil {
		return nil, nil, err
	}
	remoteLayout, err := home.ForRoot(root)
	if err != nil {
		return nil, nil, err
	}
	if remoteLayout.Root == localLayout.Root {
		return nil, nil, nil
	}
	remoteStore := store.New(remoteLayout)
	runIDs, err := listRefs(remoteLayout)
	if err != nil {
		return nil, nil, err
	}
	var runs []domain.Run
	var digests []string
	for _, runID := range runIDs {
		run, err := remoteStore.LoadRun(runID)
		if err != nil {
			return runs, digests, err
		}
		if err := remoteStore.Verify(run, true); err != nil {
			return runs, digests, err
		}
		for _, desc := range run.Objects {
			sourceFile, err := remoteStore.Open(desc.Digest)
			if err != nil {
				return runs, digests, err
			}
			imported, err := localStore.PutFile(sourceFile.Name(), desc.MediaType, desc.Required)
			sourceFile.Close()
			if err != nil {
				return runs, digests, err
			}
			if imported.Digest != desc.Digest || imported.Size != desc.Size {
				return runs, digests, fmt.Errorf("pulled object %s failed verification", desc.Digest)
			}
		}
		manifestDigest, err := localStore.PublishRun(run)
		if err != nil {
			return runs, digests, err
		}
		runs = append(runs, run)
		digests = append(digests, manifestDigest)
	}
	return runs, digests, nil
}

func PushSSH(
	ctx context.Context,
	layout home.Layout,
	objectStore *store.Store,
	run domain.Run,
	storeConfig config.StoreConfig,
) error {
	if run.Security.Blocked > 0 {
		return fmt.Errorf("security policy blocks remote push: Run has %d blocking finding(s)", run.Security.Blocked)
	}
	target, err := parseSSHTarget(storeConfig.URL)
	if err != nil {
		return err
	}
	recipients, err := cryptox.ParseRecipients(storeConfig.AgeRecipients)
	if err != nil {
		return err
	}
	cachePath, digest, err := cachedEncryptedCapsule(layout, objectStore, run, storeConfig, recipients)
	if err != nil {
		return err
	}
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	filename := run.ID + "." + hexDigest + ".smcap.age"
	capsulesDir := target.remotePath("capsules")
	refsDir := target.remotePath("refs/runs")
	refPath := target.remotePath("refs/runs/" + run.ID)
	existing, err := sshOutput(ctx, target, "if test -f "+shellQuote(refPath)+"; then cat "+shellQuote(refPath)+"; fi")
	if err != nil {
		return err
	}
	existing = strings.TrimSpace(existing)
	if existing != "" {
		if existing == filename {
			return nil
		}
		return fmt.Errorf("remote Run ID conflict: %s points to %s", run.ID, existing)
	}
	if err := sshRun(ctx, target,
		"mkdir -p -- "+shellQuote(capsulesDir)+" "+shellQuote(refsDir)); err != nil {
		return err
	}
	remoteFinal := target.remotePath("capsules/" + filename)
	present, err := sshOutput(ctx, target,
		"if test -f "+shellQuote(remoteFinal)+"; then printf present; fi")
	if err != nil {
		return err
	}
	if strings.TrimSpace(present) == "" {
		if err := uploadCapsule(ctx, target, cachePath, remoteFinal); err != nil {
			return err
		}
	}
	if err := verifyRemoteCapsule(ctx, layout, target, remoteFinal, digest); err != nil {
		// A stale or corrupted unpublished object is safe to replace because the
		// content-derived filename identifies the expected ciphertext.
		if err := uploadCapsule(ctx, target, cachePath, remoteFinal); err != nil {
			return err
		}
		if err := verifyRemoteCapsule(ctx, layout, target, remoteFinal, digest); err != nil {
			return err
		}
	}
	refTemp, err := os.CreateTemp(layout.Tmp, ".ref-*")
	if err != nil {
		return err
	}
	refTempPath := refTemp.Name()
	defer os.Remove(refTempPath)
	if err := refTemp.Chmod(0o600); err != nil {
		refTemp.Close()
		return err
	}
	if _, err := io.WriteString(refTemp, filename+"\n"); err != nil {
		refTemp.Close()
		return err
	}
	if err := refTemp.Close(); err != nil {
		return err
	}
	remoteRefTemp := refPath + ".tmp"
	if err := scpUpload(ctx, refTempPath, target, remoteRefTemp); err != nil {
		return err
	}
	return sshRun(ctx, target,
		"mv -- "+shellQuote(remoteRefTemp)+" "+shellQuote(refPath))
}

func PullSSH(
	ctx context.Context,
	layout home.Layout,
	objectStore *store.Store,
	storeConfig config.StoreConfig,
) ([]domain.Run, []string, error) {
	target, err := parseSSHTarget(storeConfig.URL)
	if err != nil {
		return nil, nil, err
	}
	identity, err := cryptox.LoadIdentity(filepath.Join(layout.Keys, "identity.txt"))
	if err != nil {
		return nil, nil, err
	}
	refsDir := target.remotePath("refs/runs")
	listing, err := sshOutput(ctx, target,
		"if test -d "+shellQuote(refsDir)+"; then ls -1 "+shellQuote(refsDir)+"; fi")
	if err != nil {
		return nil, nil, err
	}
	runIDs := strings.Fields(listing)
	sort.Strings(runIDs)
	var runs []domain.Run
	var digests []string
	for _, runID := range runIDs {
		if strings.HasSuffix(runID, ".tmp") {
			continue
		}
		if !safeRunID.MatchString(runID) {
			return runs, digests, fmt.Errorf("unsafe remote ref name %q", runID)
		}
		filename, err := sshOutput(ctx, target,
			"cat "+shellQuote(target.remotePath("refs/runs/"+runID)))
		if err != nil {
			return runs, digests, err
		}
		filename = strings.TrimSpace(filename)
		if !safeName.MatchString(filename) || !strings.HasSuffix(filename, ".smcap.age") {
			return runs, digests, fmt.Errorf("unsafe remote Capsule name %q", filename)
		}
		temp, err := os.CreateTemp(layout.Tmp, ".pull-*.smcap.age")
		if err != nil {
			return runs, digests, err
		}
		tempPath := temp.Name()
		temp.Close()
		os.Remove(tempPath)
		if err := scpDownload(ctx, target, target.remotePath("capsules/"+filename), tempPath); err != nil {
			os.Remove(tempPath)
			return runs, digests, err
		}
		expected := capsuleDigestFromFilename(runID, filename)
		if expected == "" {
			os.Remove(tempPath)
			return runs, digests, fmt.Errorf("Capsule filename does not match Run %s", runID)
		}
		actual, err := capsule.FileDigest(tempPath)
		if err != nil || actual != expected {
			os.Remove(tempPath)
			if err != nil {
				return runs, digests, err
			}
			return runs, digests, fmt.Errorf("encrypted Capsule checksum mismatch for %s", runID)
		}
		run, manifestDigest, err := capsule.ImportEncrypted(objectStore, tempPath, []age.Identity{identity}...)
		os.Remove(tempPath)
		if err != nil {
			return runs, digests, err
		}
		if run.ID != runID {
			return runs, digests, fmt.Errorf("remote ref %s contains Run %s", runID, run.ID)
		}
		runs = append(runs, run)
		digests = append(digests, manifestDigest)
	}
	return runs, digests, nil
}

func listRefs(layout home.Layout) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(layout.Refs, "runs"))
	if err != nil {
		return nil, err
	}
	var result []string
	for _, entry := range entries {
		if !entry.IsDir() {
			result = append(result, entry.Name())
		}
	}
	sort.Strings(result)
	return result, nil
}

func localPath(value string) (string, error) {
	if strings.HasPrefix(value, "file://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", err
		}
		value = parsed.Path
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(userHome, strings.TrimPrefix(value, "~/"))
	}
	return filepath.Abs(value)
}

type sshTarget struct {
	destination string
	port        string
	basePath    string
}

func (t sshTarget) remotePath(suffix string) string {
	return strings.TrimRight(t.basePath, "/") + "/" + suffix
}

var (
	safeHost  = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	safeUser  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	safePath  = regexp.MustCompile(`^[A-Za-z0-9_./~+-]+$`)
	safeName  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	safeRunID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func parseSSHTarget(value string) (sshTarget, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "ssh" {
		return sshTarget{}, fmt.Errorf("invalid SSH Store URL %q", value)
	}
	host := parsed.Hostname()
	if !safeHost.MatchString(host) {
		return sshTarget{}, fmt.Errorf("unsafe SSH host %q", host)
	}
	destination := host
	if user := parsed.User.Username(); user != "" {
		if !safeUser.MatchString(user) {
			return sshTarget{}, fmt.Errorf("unsafe SSH user %q", user)
		}
		destination = user + "@" + destination
	}
	port := parsed.Port()
	if port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return sshTarget{}, fmt.Errorf("invalid SSH port")
		}
	}
	path := parsed.Path
	if strings.HasPrefix(path, "/~/") {
		path = "~/" + strings.TrimPrefix(path, "/~/")
	}
	if path == "" || !safePath.MatchString(path) || containsParent(path) {
		return sshTarget{}, fmt.Errorf("unsafe SSH Store path %q", path)
	}
	return sshTarget{destination: destination, port: port, basePath: strings.TrimRight(path, "/")}, nil
}

func containsParent(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func shellQuote(value string) string {
	if strings.HasPrefix(value, "~/") {
		return `"$HOME/` + strings.ReplaceAll(strings.TrimPrefix(value, "~/"), `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func sshRun(ctx context.Context, target sshTarget, command string) error {
	args := target.sshArgs(command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = os.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh %s: %s", target.destination, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func sshOutput(ctx context.Context, target sshTarget, command string) (string, error) {
	args := target.sshArgs(command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh %s: %s", target.destination, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func scpUpload(ctx context.Context, local string, target sshTarget, remote string) error {
	args := target.scpArgs(local, target.destination+":"+remote)
	cmd := exec.CommandContext(ctx, "scp", args...)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp upload: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func scpDownload(ctx context.Context, target sshTarget, remote, local string) error {
	args := target.scpArgs(target.destination+":"+remote, local)
	cmd := exec.CommandContext(ctx, "scp", args...)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp download: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (t sshTarget) sshArgs(command string) []string {
	var args []string
	if t.port != "" {
		args = append(args, "-p", t.port)
	}
	return append(args, t.destination, command)
}

func (t sshTarget) scpArgs(source, destination string) []string {
	var args []string
	if t.port != "" {
		args = append(args, "-P", t.port)
	}
	return append(args, "--", source, destination)
}

func capsuleDigestFromFilename(runID, filename string) string {
	prefix := runID + "."
	suffix := ".smcap.age"
	if !strings.HasPrefix(filename, prefix) || !strings.HasSuffix(filename, suffix) {
		return ""
	}
	hexDigest := strings.TrimSuffix(strings.TrimPrefix(filename, prefix), suffix)
	if len(hexDigest) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(hexDigest); err != nil {
		return ""
	}
	return "sha256:" + hexDigest
}

func cachedEncryptedCapsule(
	layout home.Layout,
	objectStore *store.Store,
	run domain.Run,
	storeConfig config.StoreConfig,
	recipients []age.Recipient,
) (string, string, error) {
	manifestDigest, err := objectStore.ManifestDigest(run.ID)
	if err != nil {
		return "", "", err
	}
	keyInput := manifestDigest + "\x00" + strings.Join(storeConfig.AgeRecipients, "\x00")
	key := sha256.Sum256([]byte(keyInput))
	cacheDir := filepath.Join(layout.Cache, "capsules")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", "", err
	}
	cachePath := filepath.Join(cacheDir, run.ID+"-"+hex.EncodeToString(key[:16])+".smcap.age")
	if digest, err := capsule.FileDigest(cachePath); err == nil {
		return cachePath, digest, nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	digest, err := capsule.ExportEncrypted(objectStore, run, recipients, cachePath)
	if err != nil {
		if os.IsExist(err) {
			digest, digestErr := capsule.FileDigest(cachePath)
			return cachePath, digest, digestErr
		}
		return "", "", err
	}
	return cachePath, digest, nil
}

func uploadCapsule(ctx context.Context, target sshTarget, local, remoteFinal string) error {
	remoteTemp := remoteFinal + ".tmp"
	if err := scpUpload(ctx, local, target, remoteTemp); err != nil {
		return err
	}
	return sshRun(ctx, target,
		"mv -- "+shellQuote(remoteTemp)+" "+shellQuote(remoteFinal))
}

func verifyRemoteCapsule(
	ctx context.Context,
	layout home.Layout,
	target sshTarget,
	remotePath, expectedDigest string,
) error {
	temp, err := os.CreateTemp(layout.Tmp, ".verify-remote-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	temp.Close()
	defer os.Remove(tempPath)
	if err := scpDownload(ctx, target, remotePath, tempPath); err != nil {
		return err
	}
	actual, err := capsule.FileDigest(tempPath)
	if err != nil {
		return err
	}
	if actual != expectedDigest {
		return fmt.Errorf("remote encrypted Capsule checksum mismatch: got %s, want %s", actual, expectedDigest)
	}
	return nil
}
