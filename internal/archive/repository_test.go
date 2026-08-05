package archive

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRemoteIgnoresTransportAndCredentials(t *testing.T) {
	want := "github.com/Example/project"
	remotes := []string{
		"git@github.com:Example/project.git",
		"ssh://git@github.com/Example/project.git",
		"https://user:token@github.com/Example/project.git?ignored=yes#fragment",
		"git://github.com/Example/project.git",
	}
	for _, remote := range remotes {
		got, portable := NormalizeRemote(remote)
		if !portable {
			t.Fatalf("%q was not recognized as hosted", remote)
		}
		if got != want {
			t.Fatalf("NormalizeRemote(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestNormalizeRemoteRejectsLocalPaths(t *testing.T) {
	for _, remote := range []string{"../remote.git", "/tmp/remote.git", `C:\\repo.git`, "C:/repo.git", "file:///tmp/remote.git", "https://github.com", ""} {
		if got, portable := NormalizeRemote(remote); portable || got != "" {
			t.Fatalf("NormalizeRemote(%q) = %q, %v; want local", remote, got, portable)
		}
	}
}

func TestRepositoryNameUsesRemotePathOnEveryPlatform(t *testing.T) {
	repository := repositoryFromRemote("github.com/example/project")
	if repository.Name != "project" {
		t.Fatalf("repository name = %q, want project", repository.Name)
	}
}

func TestSemanticRepositoryDirectoryFlattensHostAndNamespace(t *testing.T) {
	tests := map[string]string{
		"github.com/qiulinfan/sessionmgr":        filepath.Join("github.com-qiulinfan", "sessionmgr"),
		"gitlab.com/example/platform/sessionmgr": filepath.Join("gitlab.com-example-platform", "sessionmgr"),
	}
	for remote, want := range tests {
		if got := semanticRepositoryDirectory(repositoryFromRemote(remote)); got != want {
			t.Fatalf("semanticRepositoryDirectory(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestSemanticComponentIsLimitedForUnicodePathComponents(t *testing.T) {
	result := semanticComponent(strings.Repeat("归档", 100), "repository")
	if len([]byte(result)) > 80 {
		t.Fatalf("slug is %d bytes, want at most 80", len([]byte(result)))
	}
	if result == "" {
		t.Fatal("unicode slug became empty")
	}
}

func TestSemanticComponentIsReadableAndWindowsPortable(t *testing.T) {
	if got := semanticComponent("My useful session", "session"); got != "my-useful-session" {
		t.Fatalf("semantic component = %q", got)
	}
	if got := semanticComponent("CON.txt", "session"); got != "_con.txt" {
		t.Fatalf("Windows reserved component = %q", got)
	}
	if got := semanticComponent("../../", "session"); got != "session" {
		t.Fatalf("path traversal component = %q", got)
	}
}

func TestRepositoryRequiresHostedRemote(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	gitForTest(t, root, "init", source)
	if _, err := RepositoryFromPath(context.Background(), source); err == nil {
		t.Fatal("repository without a remote was accepted")
	}
	gitForTest(t, source, "remote", "add", "origin", filepath.Join(root, "local.git"))
	if _, err := RepositoryFromPath(context.Background(), source); err == nil {
		t.Fatal("repository with a local remote was accepted")
	}
	gitForTest(t, source, "remote", "set-url", "origin", "git@github.com:example/project.git")
	repository, err := RepositoryFromPath(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if repository.CanonicalRemote != "github.com/example/project" {
		t.Fatalf("unexpected hosted repository: %+v", repository)
	}
}

func gitForTest(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
