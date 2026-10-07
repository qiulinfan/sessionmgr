package archive

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errSourceIncomplete = errors.New("session source is incomplete")

// Age is a retry policy, never evidence that a process has exited.
func stableIncomplete(err error, modified, now time.Time) bool {
	return errors.Is(err, errSourceIncomplete) || (sourceErrorIsBusy(err) && !modified.IsZero() && now.Sub(modified) > time.Hour)
}

// Cross-CLI jobs lack OpenCode parent IDs. Limit detection to known generated
// engine job directories under the OS temporary root, not arbitrary repo names.
func automatedOpenCodeDirectory(directory string) bool {
	directory = filepath.Clean(directory)
	temp := filepath.Clean(os.TempDir())
	if strings.HasPrefix(directory, "/private/tmp/claude-") || strings.HasPrefix(directory, "/tmp/claude-") {
		return strings.Contains(filepath.ToSlash(directory), "/scratchpad/")
	}
	roots := []string{temp, strings.TrimPrefix(temp, "/private")}
	if strings.HasPrefix(temp, "/var/") || temp == "/tmp" {
		roots = append(roots, "/private"+temp)
	}
	for _, root := range roots {
		rel, err := filepath.Rel(root, directory)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		first := strings.Split(filepath.ToSlash(rel), "/")[0]
		for _, prefix := range []string{"pocket-eval-", "pocket-agent-", "pocket-play-", "pocket-debug-eval-"} {
			if strings.HasPrefix(first, prefix) {
				return true
			}
		}
	}
	return false
}
