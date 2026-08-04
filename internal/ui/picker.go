package ui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func pickDirectory(ctx context.Context) (string, error) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "osascript", "-e", `POSIX path of (choose folder with prompt "Choose a Session Manager export directory")`)
	case "windows":
		command = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-STA", "-Command", `Add-Type -AssemblyName System.Windows.Forms; $dialog = New-Object System.Windows.Forms.FolderBrowserDialog; $dialog.Description = 'Choose a Session Manager export directory'; if ($dialog.ShowDialog() -eq 'OK') { Write-Output $dialog.SelectedPath } else { exit 1 }`)
	default:
		if path, err := exec.LookPath("zenity"); err == nil {
			command = exec.CommandContext(ctx, path, "--file-selection", "--directory", "--title=Choose a Session Manager export directory")
		} else if path, err := exec.LookPath("kdialog"); err == nil {
			command = exec.CommandContext(ctx, path, "--getexistingdirectory", ".", "--title", "Choose a Session Manager export directory")
		} else {
			return "", fmt.Errorf("no directory picker found; install zenity or kdialog, or enter the path manually")
		}
	}
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("directory selection canceled or failed: %w", err)
	}
	directory := strings.TrimSpace(string(output))
	if directory == "" {
		return "", fmt.Errorf("no directory selected")
	}
	return directory, nil
}
