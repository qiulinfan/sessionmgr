package app

import (
	"context"
	"debug/buildinfo"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const cliPackage = "github.com/sessionmgr/sessionmgr/cmd/sessionmgr"
const macAppIdentifier = "com.qiulinfan.sessionmgr"

func commandOpen(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		return argumentError("open does not accept arguments; use gui for server options")
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		for _, path := range []string{
			filepath.Join(home, "Applications", "Session Manager.app"),
			"/Applications/Session Manager.app",
		} {
			if !isSessionManagerApp(path) {
				continue
			}
			if err := exec.CommandContext(ctx, "open", "-a", path).Run(); err != nil {
				return fmt.Errorf("open Session Manager app: %w", err)
			}
			fmt.Fprintln(stdout, "Opened Session Manager.app.")
			return nil
		}
	}
	return commandGUI(ctx, nil, stdout, stderr)
}

func isSessionManagerApp(path string) bool {
	file, err := os.Open(filepath.Join(path, "Contents", "Info.plist"))
	if err != nil {
		return false
	}
	defer file.Close()
	decoder := xml.NewDecoder(io.LimitReader(file, 1024*1024))
	wantValue := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if element.Name.Local == "key" {
			var key string
			if decoder.DecodeElement(&key, &element) != nil {
				return false
			}
			wantValue = key == "CFBundleIdentifier"
		} else if wantValue && element.Name.Local == "string" {
			var value string
			if decoder.DecodeElement(&value, &element) != nil {
				return false
			}
			return value == macAppIdentifier
		}
	}
}

func commandInstallCLI(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("install-cli", stderr)
	directory := flags.String("directory", "", "directory for the smg executable (default: ~/.local/bin)")
	if err := flags.Parse(args); err != nil {
		return flagError(err)
	}
	if flags.NArg() != 0 {
		return argumentError("install-cli does not accept positional arguments")
	}
	if *directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		*directory = filepath.Join(home, ".local", "bin")
	}
	root, err := filepath.Abs(*directory)
	if err != nil {
		return err
	}
	name := "smg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	source, err := os.Executable()
	if err != nil {
		return err
	}
	target := filepath.Join(root, name)
	if err := installCLIExecutable(source, target); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Installed smg: %s\n", target)
	onPath := false
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(entry) == filepath.Clean(root) {
			onPath = true
			break
		}
	}
	if !onPath {
		fmt.Fprintf(stdout, "Add %s to your PATH to run smg from any directory.\n", root)
	}
	return nil
}

func isCLIExecutable(path string) bool {
	info, err := buildinfo.ReadFile(path)
	return err == nil && info.Path == cliPackage
}

// Installation copies the same Go program, independent of a development
// checkout, without changing shell profiles, daemons, or native session homes.
func installCLIExecutable(source, target string) error {
	if !isCLIExecutable(source) {
		return fmt.Errorf("installer source is not a Session Manager executable")
	}
	if existing, err := os.Lstat(target); err == nil {
		if !existing.Mode().IsRegular() || !isCLIExecutable(target) {
			return fmt.Errorf("refusing to replace an unrelated command: %s", target)
		}
		original, err := os.Stat(source)
		if err != nil {
			return err
		}
		if os.SameFile(original, existing) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(target), ".smg-install-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	defer output.Close()
	if err := output.Chmod(0o755); err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Rename(output.Name(), target); err != nil {
		return fmt.Errorf("install smg (close any running smg process before updating): %w", err)
	}
	return nil
}
