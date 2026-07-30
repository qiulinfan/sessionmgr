package service

import (
	"context"
	"testing"
)

func TestDashboardAndInitialize(t *testing.T) {
	t.Setenv("SESSIONMGR_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	service, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	before, err := service.Dashboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.SchemaVersion != 1 || before.Stats.Runs != 0 {
		t.Fatalf("unexpected initial dashboard: %#v", before)
	}
	after, err := service.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundEncryption := false
	for _, check := range after.Health {
		if check.ID == "encryption" && check.Status == "ready" {
			foundEncryption = true
		}
	}
	if !foundEncryption {
		t.Fatal("initialization did not make encryption ready")
	}
}

func TestPreviewDashboard(t *testing.T) {
	preview := PreviewDashboard()
	if !preview.Preview || len(preview.RecentRuns) < 3 || preview.Stats.Runs == 0 {
		t.Fatalf("preview dashboard is incomplete: %#v", preview)
	}
}
