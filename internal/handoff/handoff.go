package handoff

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sessionmgr/sessionmgr/internal/domain"
	"github.com/sessionmgr/sessionmgr/internal/store"
)

func Render(objectStore *store.Store, run domain.Run, targetWorktree, output string) (string, error) {
	if len(run.Workspaces) != 1 {
		return "", fmt.Errorf("MVP handoff requires exactly one workspace")
	}
	workspace := run.Workspaces[0]
	events, err := loadEvents(objectStore, run.Sessions)
	if err != nil {
		return "", err
	}
	files, err := changedFiles(objectStore, workspace)
	if err != nil {
		return "", err
	}
	objective := "No user objective was found in the normalized event stream."
	for _, event := range events {
		if event.Actor == "user" && event.Kind == "message" && event.Summary != "" {
			objective = event.Summary + provenance(event)
			break
		}
	}
	var decisions, verifications []string
	for _, event := range events {
		switch event.Kind {
		case "decision":
			decisions = append(decisions, event.Summary+provenance(event))
		case "verification":
			verifications = append(verifications, event.Summary+provenance(event))
		}
	}
	var out strings.Builder
	out.WriteString("# Handoff\n\n")
	out.WriteString("## Objective\n\n")
	out.WriteString(objective + "\n\n")
	out.WriteString("## Current repository state\n\n")
	fmt.Fprintf(&out, "- Run: `%s`\n", run.ID)
	fmt.Fprintf(&out, "- Repository: `%s`\n", workspace.Repository.ID)
	fmt.Fprintf(&out, "- HEAD: `%s`\n", workspace.HeadSHA)
	if workspace.Branch != "" {
		fmt.Fprintf(&out, "- Captured branch: `%s`\n", workspace.Branch)
	}
	if targetWorktree != "" {
		fmt.Fprintf(&out, "- Restored worktree: `%s`\n", targetWorktree)
	}
	fmt.Fprintf(&out, "- Staged patch: `%s`\n", workspace.Digest.StagedPatchSHA)
	fmt.Fprintf(&out, "- Unstaged patch: `%s`\n", workspace.Digest.UnstagedPatchSHA)
	fmt.Fprintf(&out, "- Untracked tree: `%s`\n\n", workspace.Digest.UntrackedTreeSHA)

	out.WriteString("## Completed work\n\n")
	if len(files) == 0 {
		out.WriteString("No file changes are present in the captured workspace.\n\n")
	} else {
		fmt.Fprintf(&out, "The captured workspace contains changes to %d file(s). This is a Git-derived fact; the session is the source for intent.\n\n", len(files))
	}
	out.WriteString("## Key decisions\n\n")
	writeEvidenceList(&out, decisions, "No structured decision evidence was found. Consult the raw session before inferring intent.")
	out.WriteString("## Changed files\n\n")
	if len(files) == 0 {
		out.WriteString("None.\n\n")
	} else {
		for _, file := range files {
			fmt.Fprintf(&out, "- `%s`\n", file)
		}
		out.WriteString("\n")
	}
	out.WriteString("## Verification performed\n\n")
	writeEvidenceList(&out, verifications, "No structured verification evidence was found; this handoff does not claim that tests passed.")
	out.WriteString("## Known issues\n\n")
	if len(workspace.Warnings) == 0 && run.Security.Blocked == 0 && run.Security.Warnings == 0 {
		out.WriteString("No known issue is recorded in the Capsule. This is not proof that none exist.\n\n")
	} else {
		for _, warning := range workspace.Warnings {
			fmt.Fprintf(&out, "- %s\n", warning)
		}
		if run.Security.Blocked > 0 || run.Security.Warnings > 0 {
			fmt.Fprintf(&out, "- Security scan recorded %d blocking and %d warning finding(s); secret values are intentionally omitted.\n",
				run.Security.Blocked, run.Security.Warnings)
		}
		out.WriteString("\n")
	}
	out.WriteString("## Suggested next steps\n\n")
	out.WriteString("1. Inspect `git status` and the changed files in the restored worktree.\n")
	out.WriteString("2. Review the original objective and unresolved session context.\n")
	out.WriteString("3. Run the appropriate project tests before claiming completion.\n\n")
	out.WriteString("## Provenance\n\n")
	fmt.Fprintf(&out, "- Manifest run ID: `%s`\n", run.ID)
	fmt.Fprintf(&out, "- Workspace checkpoint digest: `%s / %s / %s / %s`\n",
		workspace.Digest.HeadSHA, workspace.Digest.StagedPatchSHA,
		workspace.Digest.UnstagedPatchSHA, workspace.Digest.UntrackedTreeSHA)
	for _, session := range run.Sessions {
		fmt.Fprintf(&out, "- %s session `%s`, normalized object `%s`\n",
			session.Platform, session.NativeID, session.NormalizedObject)
	}

	if output == "" {
		return out.String(), nil
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return "", err
	}
	if err := atomicWrite(output, []byte(out.String())); err != nil {
		return "", err
	}
	return output, nil
}

func loadEvents(objectStore *store.Store, sessions []domain.AgentSession) ([]domain.NormalizedEvent, error) {
	var result []domain.NormalizedEvent
	for _, session := range sessions {
		if session.NormalizedObject == "" {
			continue
		}
		data, err := objectStore.Get(session.NormalizedObject)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var event domain.NormalizedEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				return nil, err
			}
			result = append(result, event)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Timestamp.Equal(result[j].Timestamp) {
			return result[i].Sequence < result[j].Sequence
		}
		return result[i].Timestamp.Before(result[j].Timestamp)
	})
	return result, nil
}

func changedFiles(objectStore *store.Store, workspace domain.WorkspaceSnapshot) ([]string, error) {
	seen := make(map[string]bool)
	for _, digest := range []string{workspace.Payload.StagedPatchObject, workspace.Payload.UnstagedPatchObject} {
		data, err := objectStore.Get(digest)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "diff --git a/") {
				continue
			}
			parts := strings.SplitN(strings.TrimPrefix(line, "diff --git a/"), " b/", 2)
			if len(parts) == 2 {
				seen[parts[1]] = true
			}
		}
	}
	raw, err := objectStore.Get(workspace.Payload.UntrackedManifestObject)
	if err != nil {
		return nil, err
	}
	var manifest domain.UntrackedManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	for _, entry := range manifest.Entries {
		seen[entry.Path] = true
	}
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	return files, nil
}

func provenance(event domain.NormalizedEvent) string {
	return fmt.Sprintf(" _(session event %d, raw `%s`)_", event.Source.Record, event.Source.RawObject)
}

func writeEvidenceList(out *strings.Builder, values []string, empty string) {
	if len(values) == 0 {
		out.WriteString(empty + "\n\n")
		return
	}
	for _, value := range values {
		fmt.Fprintf(out, "- %s\n", value)
	}
	out.WriteString("\n")
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".handoff-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
