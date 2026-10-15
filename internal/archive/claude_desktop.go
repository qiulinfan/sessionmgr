package archive

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// The physical transcript ID owns membership. Copied fork ancestry cannot
// establish desktop identity for a CLI/SDK child. Unknown entrypoints fail closed.
func claudeDesktopSessionID(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	seen := false
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record claudeRecord
		if json.Unmarshal(line, &record) != nil {
			return "", nil
		}
		if record.Type == "teleported-from" {
			return "", nil
		}
		if record.SessionID != id {
			continue
		}
		if record.IsSidechain || record.AgentID != "" {
			return "", nil
		}
		if record.Entrypoint != "" {
			if record.Entrypoint != "claude-desktop" {
				return "", nil
			}
			seen = true
		}
	}
	if seen {
		return id, nil
	}
	return "", nil
}
