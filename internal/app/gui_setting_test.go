package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSavedGUISettingAndOneTimeOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SESSIONMGR_CONFIG", filepath.Join(root, "config.json"))
	run := func(args ...string) map[string]any {
		t.Helper()
		var out, errout bytes.Buffer
		code, err := Run(context.Background(), args, &out, &errout)
		if err != nil || code != 0 {
			t.Fatalf("%v: %d %v %s", args, code, err, errout.String())
		}
		var data map[string]any
		if err := json.Unmarshal(out.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	run("config", "set-gui-only", "--json", "true")
	args := []string{"export", "--sources", "omp", "--omp-home", filepath.Join(root, "omp"), "--output", filepath.Join(root, "archive"), "--json"}
	if v := run(args...); v["scope"] != "gui-conversations" {
		t.Fatalf("saved setting ignored: %v", v)
	}
	if v := run(append(args, "--gui-only=false")...); v["scope"] != nil {
		t.Fatalf("one-time opt-out ignored: %v", v)
	}
	if v := run("config", "show", "--json"); v["gui_only"] != true {
		t.Fatalf("override changed saved setting: %v", v)
	}
	run("config", "set-gui-only", "--json", "false")
	if v := run(args...); v["scope"] != nil {
		t.Fatalf("cannot restore broad export: %v", v)
	}
}
