package archive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func ompFixture(cwd string) []byte {
	slot := `{"type":"title","v":1,"title":"Saved title","updatedAt":"2026-09-29T12:10:00Z"}`
	slot += strings.Repeat(" ", 255-len(slot))
	return []byte(slot + "\n" + fmt.Sprintf(`{"type":"session","version":3,"id":"omp-fixture","timestamp":"2026-09-29T12:00:00Z","cwd":%q}`+"\n", cwd) +
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-09-29T12:01:00Z","message":{"role":"user","content":[{"type":"text","text":"first question"}]}}` + "\n" +
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-09-29T12:02:00Z","message":{"role":"assistant","content":[{"type":"text","text":"old answer"}]}}` + "\n" +
		`{"type":"message","id":"a2","parentId":"u1","timestamp":"2026-09-29T12:03:00Z","message":{"role":"assistant","content":[{"type":"thinking","text":"private reasoning"},{"type":"toolCall","id":"call1"},{"type":"text","text":"selected answer"}]}}` + "\n" +
		`{"type":"custom","id":"tail","parentId":"a2","timestamp":"2026-09-29T12:04:00Z","customType":"status","data":{}}` + "\n")
}

func TestParseOMPSessionUsesCurrentBranch(t *testing.T) {
	cwd := t.TempDir()
	session, err := parseOMPSession(ompFixture(cwd), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if session.Harness != harnessOMP || session.ID != "omp-fixture" || session.Title != "Saved title" {
		t.Fatalf("wrong OMP identity or title: %+v", session)
	}
	if session.UserMessages != 1 || session.AssistantMessages != 1 || session.AlternateBranches != 1 || session.ToolCallCount != 1 {
		t.Fatalf("wrong OMP branch/counts: %+v", session)
	}
	if len(session.Messages) != 2 || session.Messages[1].Text != "selected answer" ||
		strings.Contains(session.Messages[1].Text, "private reasoning") {
		t.Fatalf("wrong selected conversation: %+v", session.Messages)
	}
}

func TestOMPExportPublishesOnlySelectedConversation(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "omp")
	path := filepath.Join(home, "sessions", "-work", "2026-09-29_omp-fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, ompFixture(cwd), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Export(context.Background(), Options{
		OMPHome: home, OMPSessionDir: filepath.Join(home, "sessions"), Output: filepath.Join(root, "archive"), AllRepos: true, IncludeNonGit: true,
		Sources: &SourceSelection{OMP: true}, DeviceID: "device:test", DeviceName: "test",
		StabilityWindow: -time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources != 1 || result.Created != 1 || len(result.Changes) != 1 || result.Changes[0].Harness != harnessOMP {
		t.Fatalf("unexpected OMP export result: %+v", result)
	}
	document, err := os.ReadFile(result.Changes[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(document), "selected answer") || strings.Contains(string(document), "old answer") ||
		strings.Contains(string(document), "private reasoning") {
		t.Fatalf("OMP document does not match selected branch: %s", document)
	}
}

func TestOMPRejectsUnsupportedHeaderAndParent(t *testing.T) {
	cwd := t.TempDir()
	badVersion := strings.Replace(string(ompFixture(cwd)), `"version":3`, `"version":4`, 1)
	if _, err := parseOMPSession([]byte(badVersion), t.TempDir()); err == nil {
		t.Fatal("accepted unknown OMP version")
	}
	badParent := strings.Replace(string(ompFixture(cwd)), `"id":"a2","parentId":"u1"`, `"id":"a2","parentId":"missing"`, 1)
	if _, err := parseOMPSession([]byte(badParent), t.TempDir()); err == nil {
		t.Fatal("accepted missing parent")
	}
}

func TestOMPBlobReferenceStaysBoundToItsAgentHome(t *testing.T) {
	home := t.TempDir()
	hash := strings.TrimPrefix(digestBytes([]byte("image fixture")), "sha256:")
	raw := fmt.Sprintf(`[{"type":"image","mimeType":"image/png","data":"blob:sha256:%s"}]`, hash)
	_, attachments, _, omitted, err := ompMessageContent([]byte(raw), home)
	if err != nil || omitted != 0 || len(attachments) != 1 {
		t.Fatalf("OMP blob reference was not recognized: %+v / %d / %v", attachments, omitted, err)
	}
	if attachments[0].SourceKind != "local_path" || attachments[0].SourceValue != filepath.Join(home, "blobs", hash) ||
		attachments[0].ExpectedHash != "sha256:"+hash {
		t.Fatalf("OMP blob escaped its agent home or lost integrity: %+v", attachments[0])
	}
}

func TestOMPRespectsSeparateSessionDirectory(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "separate-sessions")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "session.jsonl"), ompFixture(cwd), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", store)
	result, err := Export(context.Background(), Options{
		OMPHome: filepath.Join(root, "agent"), Output: filepath.Join(root, "archive"), AllRepos: true, IncludeNonGit: true,
		Sources: &SourceSelection{OMP: true}, DeviceID: "device:test", DeviceName: "test", StabilityWindow: -time.Second,
	})
	if err != nil || result.Sources != 1 || result.Created != 1 {
		t.Fatalf("OMP separate session directory was not discovered: %+v / %v", result, err)
	}
}

func TestOMPRunningTurnKeepsCompletedSelectedBranch(t *testing.T) {
	cwd := t.TempDir()
	base := strings.Replace(string(ompFixture(cwd)), `"role":"assistant","content":[{"type":"thinking"`, `"role":"assistant","stopReason":"stop","content":[{"type":"thinking"`, 1)
	// The selected a2 answer and following custom status are already saved.
	// A new user turn starts after that status node.
	previous := []byte(base)
	active := base + `{"type":"message","id":"u2","parentId":"tail","timestamp":"2026-09-29T12:05:00Z","message":{"role":"user","content":[{"type":"text","text":"current question"}]}}` + "\n" +
		`{"type":"message","id":"a3","parentId":"u2","timestamp":"2026-09-29T12:06:00Z","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"text","text":"partial answer"}]}}` + "\n"
	for _, raw := range [][]byte{[]byte(active), append([]byte(active), []byte(`{"type":"message"`)...)} {
		session, err := parseOMPSession(raw, t.TempDir())
		if err != nil || session.UserMessages != 1 || session.AssistantMessages != 1 ||
			len(session.Messages) != 2 || session.Messages[1].Text != "selected answer" ||
			session.RawHash != digestBytes(previous) {
			t.Fatalf("running OMP turn was included: %+v / %v", session, err)
		}
	}
	// A new branch without any completed answer must not export a completed
	// answer from the abandoned branch.
	fork := base + `{"type":"message","id":"fork-u","parentId":"u1","timestamp":"2026-09-29T12:07:00Z","message":{"role":"user","content":[{"type":"text","text":"new branch"}]}}` + "\n"
	if _, err := parseOMPSession([]byte(fork), t.TempDir()); err == nil {
		t.Fatal("exported a completed answer from an abandoned OMP branch")
	}
}

func TestGUIOnlyExportReadsTheOMPSessionsAGUIStarted(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "omp")
	// One store holds every OMP session; the header's origin tells the ones a GUI started.
	write := func(path, id, origin string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		header := fmt.Sprintf(`"id":%q,`, id)
		if origin != "" {
			header += `"origin":` + origin + ","
		}
		data := strings.Replace(string(ompFixture(cwd)), `"id":"omp-fixture",`, header, 1)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	project := filepath.Join(home, "sessions", "-work")
	write(filepath.Join(project, "2026-09-29_cli.jsonl"), "cli", `{"mode":"print"}`)
	// A CLI session stays out even after a GUI took it up: its origin is the CLI's.
	write(filepath.Join(project, "2026-09-29_tui.jsonl"), "tui", `{"mode":"tui"}`)
	write(filepath.Join(project, "2026-09-29_probe.jsonl"), "probe", `{"mode":"acp","client":"probe"}`)
	write(filepath.Join(project, "2026-09-30_zed.jsonl"), "zed", `{"mode":"acp","client":"zed"}`)
	write(filepath.Join(project, "2026-09-30_zed", "subagents", "2026-09-30_sub.jsonl"), "sub", `{"mode":"tui","client":"omp-gui"}`)
	write(filepath.Join(project, "2026-09-30_iina.jsonl"), "iina", `{"mode":"acp","client":"iina"}`)
	write(filepath.Join(project, "2026-10-01_gui.jsonl"), "gui", `{"mode":"tui","client":"omp-gui"}`)
	// omp gui's chats live in their own store, written by nothing else; older ones carry no origin.
	write(filepath.Join(home, "sessions-chats", "2026-10-02_chat.jsonl"), "chat", "")

	result, err := Export(context.Background(), Options{
		OMPHome: home, Output: filepath.Join(root, "out"), AllRepos: true, IncludeNonGit: true,
		Sources: &SourceSelection{OMP: true}, DeviceID: "device:test", DeviceName: "test",
		StabilityWindow: -time.Second, GUIOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	exported := make([]string, 0, len(result.Changes))
	for _, change := range result.Changes {
		exported = append(exported, change.SessionID)
	}
	sort.Strings(exported)
	if want := []string{"chat", "gui", "iina", "zed"}; strings.Join(exported, ",") != strings.Join(want, ",") || result.FilteredInternal != 4 {
		t.Fatalf("GUI-only export should read the sessions a GUI started and omp gui's chats, not CLI, other ACP clients or subagents: exported %v, %+v", exported, result)
	}
}
