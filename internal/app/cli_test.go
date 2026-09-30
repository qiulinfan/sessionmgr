package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIInstallationAndSubcommands(t *testing.T) {
	root := t.TempDir()
	name := "sessionmgr"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	source := filepath.Join(root, name)
	build := exec.Command("go", "build", "-trimpath", "-o", source, "../../cmd/sessionmgr")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI fixture: %s, %v", output, err)
	}
	target := filepath.Join(root, "installed", "smg")
	if runtime.GOOS == "windows" {
		target += ".exe"
	}
	if err := installCLIExecutable(source, target); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := installCLIExecutable(source, target); err != nil {
		t.Fatalf("repeat installation: %v", err)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("repeat install changed executable contents")
	}
	if err := installCLIExecutable(target, target); err != nil {
		t.Fatalf("self installation: %v", err)
	}
	result, err := exec.Command(target, "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(result), "sessionmgr ") {
		t.Fatalf("installed version: %q, %v", result, err)
	}
	result, err = exec.Command(target, "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(result), "smg export") || !strings.Contains(string(result), "install-cli") {
		t.Fatalf("installed help: %q, %v", result, err)
	}
	custom := filepath.Join(root, "custom")
	result, err = exec.Command(source, "install-cli", "--directory", custom).CombinedOutput()
	if err != nil || !strings.Contains(string(result), "Installed smg:") {
		t.Fatalf("installer command: %q, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(custom, filepath.Base(target))); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "unrelated")
	if err := os.WriteFile(unrelated, []byte("user-owned command\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installCLIExecutable(source, unrelated); err == nil {
		t.Fatal("overwrote an unrelated command")
	}
	data, err := os.ReadFile(unrelated)
	if err != nil || string(data) != "user-owned command\n" {
		t.Fatal("refused command was modified")
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(target, link); err == nil {
		if err := installCLIExecutable(source, link); err == nil {
			t.Fatal("replaced a symlinked command")
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".smg-install-*"))
	if len(leftovers) != 0 {
		t.Fatalf("installer scratch files remain: %v", leftovers)
	}
}

func TestNativeAppIdentityIsReadWithoutLaunching(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Session Manager.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(app, "Contents", "Info.plist")
	for _, value := range []struct {
		identifier string
		want       bool
	}{
		{macAppIdentifier, true}, {"other.app", false},
	} {
		data := `<?xml version="1.0"?><plist><dict><key>CFBundleIdentifier</key><string>` + value.identifier + `</string></dict></plist>`
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := isSessionManagerApp(app); got != value.want {
			t.Fatalf("bundle identity %q: %t", value.identifier, got)
		}
	}
	if err := os.WriteFile(file, []byte("invalid plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isSessionManagerApp(app) {
		t.Fatal("accepted malformed app identity")
	}
}

func TestOpenAndInstallerRejectUnexpectedArguments(t *testing.T) {
	for _, args := range [][]string{{"open", "extra"}, {"install-cli", "extra"}} {
		var stdout, stderr bytes.Buffer
		code, err := Run(context.Background(), args, &stdout, &stderr)
		if err == nil || code != 2 {
			t.Fatalf("unexpected arguments accepted: %v, %d, %v", args, code, err)
		}
	}
}
