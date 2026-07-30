package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sessionmgr/sessionmgr/internal/domain"
)

func TestCaptureVerifyRestoreEndToEnd(t *testing.T) {
	temp := t.TempDir()
	remote := filepath.Join(temp, "remote.git")
	source := filepath.Join(temp, "source")
	targetRepo := filepath.Join(temp, "target-repo")
	restored := filepath.Join(temp, "restored")
	sessionHome := filepath.Join(temp, "codex")
	managerHome := filepath.Join(temp, "sessionmgr")
	managerHomeB := filepath.Join(temp, "sessionmgr-b")
	transferStore := filepath.Join(temp, "transfer-store")

	git(t, temp, "init", "--bare", remote)
	git(t, temp, "clone", remote, source)
	git(t, source, "config", "user.email", "test@example.com")
	git(t, source, "config", "user.name", "Session Manager Test")
	writeFile(t, filepath.Join(source, "tracked.txt"), "base\n")
	git(t, source, "add", "tracked.txt")
	git(t, source, "commit", "-m", "initial")
	git(t, source, "push", "-u", "origin", "HEAD")

	writeFile(t, filepath.Join(source, "local-commit.txt"), "not pushed\n")
	writeFile(t, filepath.Join(source, ".gitignore"), "ignored.log\n")
	git(t, source, "add", "local-commit.txt")
	git(t, source, "add", ".gitignore")
	git(t, source, "commit", "-m", "local commit")
	writeFile(t, filepath.Join(source, "tracked.txt"), "base\nstaged\n")
	git(t, source, "add", "tracked.txt")
	writeFile(t, filepath.Join(source, "tracked.txt"), "base\nstaged\nunstaged\n")
	writeFile(t, filepath.Join(source, "untracked.txt"), "portable payload\n")
	writeFile(t, filepath.Join(source, "ignored.log"), "explicit ignored payload\n")

	git(t, temp, "clone", remote, targetRepo)
	git(t, targetRepo, "config", "user.email", "test@example.com")
	git(t, targetRepo, "config", "user.name", "Session Manager Test")

	sessionPath := filepath.Join(sessionHome, "sessions", "2026", "07", "30", "fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	session := strings.Join([]string{
		`{"timestamp":"2026-07-30T06:00:00Z","type":"session_meta","payload":{"id":"fixture-session","cwd":` + quote(source) + `,"cli_version":"1.0.0"}}`,
		`{"timestamp":"2026-07-30T06:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"Implement the portable change"}}`,
		`{"timestamp":"2026-07-30T06:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"Implemented the requested change"}}`,
	}, "\n") + "\n"
	writeFile(t, sessionPath, session)
	t.Setenv("SESSIONMGR_HOME", managerHome)
	t.Setenv("CODEX_HOME", sessionHome)

	var captureOut, captureErr bytes.Buffer
	code, err := Run(context.Background(), []string{
		"capture", "--repo", source, "--latest", "--title", "E2E Run",
		"--include-ignored", "ignored.log", "--json",
	}, &captureOut, &captureErr)
	if code != 0 || err != nil {
		t.Fatalf("capture failed code=%d err=%v stderr=%s stdout=%s", code, err, captureErr.String(), captureOut.String())
	}
	var result domain.Result
	if err := json.Unmarshal(captureOut.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RunID == "" {
		t.Fatal("capture returned no run ID")
	}

	var verifyOut bytes.Buffer
	code, err = Run(context.Background(), []string{"verify", result.RunID, "--deep", "--json"}, &verifyOut, &captureErr)
	if code != 0 || err != nil {
		t.Fatalf("verify failed code=%d err=%v", code, err)
	}
	writeFile(t, filepath.Join(managerHome, "config.toml"), fileStoreConfig(transferStore))
	var pushOut bytes.Buffer
	code, err = Run(context.Background(), []string{"push", result.RunID, "--store", "transfer", "--json"}, &pushOut, &captureErr)
	if code != 0 || err != nil {
		t.Fatalf("push failed code=%d err=%v stdout=%s", code, err, pushOut.String())
	}

	t.Setenv("SESSIONMGR_HOME", managerHomeB)
	var initOut bytes.Buffer
	code, err = Run(context.Background(), []string{"init"}, &initOut, &captureErr)
	if code != 0 || err != nil {
		t.Fatalf("second machine init failed code=%d err=%v", code, err)
	}
	writeFile(t, filepath.Join(managerHomeB, "config.toml"), fileStoreConfig(transferStore))
	var pullOut bytes.Buffer
	code, err = Run(context.Background(), []string{"pull", "--store", "transfer", "--json"}, &pullOut, &captureErr)
	if code != 0 || err != nil {
		t.Fatalf("pull failed code=%d err=%v stdout=%s", code, err, pullOut.String())
	}
	verifyOut.Reset()
	code, err = Run(context.Background(), []string{"verify", result.RunID, "--deep", "--json"}, &verifyOut, &captureErr)
	if code != 0 || err != nil {
		t.Fatalf("pulled Run verify failed code=%d err=%v", code, err)
	}

	var restoreOut, restoreErr bytes.Buffer
	code, err = Run(context.Background(), []string{
		"restore", result.RunID, "--repo", targetRepo, "--worktree", restored, "--json",
	}, &restoreOut, &restoreErr)
	if code != 0 || err != nil {
		t.Fatalf("restore failed code=%d err=%v stderr=%s stdout=%s", code, err, restoreErr.String(), restoreOut.String())
	}
	if got := readFile(t, filepath.Join(restored, "local-commit.txt")); got != "not pushed\n" {
		t.Fatalf("local commit file: got %q", got)
	}
	if got := readFile(t, filepath.Join(restored, "tracked.txt")); got != "base\nstaged\nunstaged\n" {
		t.Fatalf("tracked file: got %q", got)
	}
	if got := readFile(t, filepath.Join(restored, "untracked.txt")); got != "portable payload\n" {
		t.Fatalf("untracked file: got %q", got)
	}
	if got := readFile(t, filepath.Join(restored, "ignored.log")); got != "explicit ignored payload\n" {
		t.Fatalf("explicit ignored file: got %q", got)
	}
	status := gitOutput(t, restored, "status", "--porcelain=v1")
	if !strings.Contains(status, "MM tracked.txt") {
		t.Fatalf("staged/unstaged split not restored:\n%s", status)
	}
	if !strings.Contains(status, "?? untracked.txt") {
		t.Fatalf("untracked state not restored:\n%s", status)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func quote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func fileStoreConfig(path string) string {
	return `schema_version = 1
default_store = "transfer"
telemetry = false

[capture]
include_untracked = true
include_ignored = false
max_file_bytes = 268435456
max_total_bytes = 1073741824

[security]
block_private_keys = true
block_high_confidence_tokens = true

[[stores]]
name = "transfer"
type = "file"
url = ` + quote(path) + "\n"
}
