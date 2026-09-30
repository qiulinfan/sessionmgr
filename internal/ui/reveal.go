package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func validateRevealPath(root, path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != "conversation.md" {
		return "", fmt.Errorf("invalid exported session path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("exported session is no longer available: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("exported session must be a regular file")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("exported session is outside the export directory")
	}
	return resolvedPath, nil
}

func revealCommand(platform, path string) *exec.Cmd {
	switch platform {
	case "darwin":
		return exec.Command("open", "-R", path)
	case "windows":
		return exec.Command("explorer.exe", "/select,"+path)
	default:
		return exec.Command("xdg-open", filepath.Dir(path))
	}
}

func revealDocument(path string) error {
	command := revealCommand(runtime.GOOS, path)
	if err := command.Run(); err != nil {
		return fmt.Errorf("could not open the session in the file manager: %w", err)
	}
	return nil
}
