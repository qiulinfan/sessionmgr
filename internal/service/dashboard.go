package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sessionmgr/sessionmgr/internal/agent/codex"
	"github.com/sessionmgr/sessionmgr/internal/catalog"
	"github.com/sessionmgr/sessionmgr/internal/config"
	"github.com/sessionmgr/sessionmgr/internal/cryptox"
	"github.com/sessionmgr/sessionmgr/internal/home"
	"github.com/sessionmgr/sessionmgr/internal/store"
)

type Service struct {
	layout      home.Layout
	catalog     *catalog.Catalog
	objectStore *store.Store
}

type Dashboard struct {
	SchemaVersion int            `json:"schema_version"`
	Preview       bool           `json:"preview"`
	Version       string         `json:"version"`
	Home          string         `json:"home"`
	Health        []HealthCheck  `json:"health"`
	Stats         DashboardStats `json:"stats"`
	RecentRuns    []RunCard      `json:"recent_runs"`
	Stores        []StoreCard    `json:"stores"`
}

type HealthCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type DashboardStats struct {
	Runs           int `json:"runs"`
	Verified       int `json:"verified"`
	NeedsAttention int `json:"needs_attention"`
	Stores         int `json:"stores"`
}

type RunCard struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Repository string `json:"repository"`
	Agent      string `json:"agent"`
	CreatedAt  string `json:"created_at"`
	Integrity  string `json:"integrity"`
	SyncStatus string `json:"sync_status"`
	Relation   string `json:"relation"`
}

type StoreCard struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func Open() (*Service, error) {
	layout, err := home.Resolve()
	if err != nil {
		return nil, err
	}
	if err := home.Ensure(layout); err != nil {
		return nil, err
	}
	cat, err := catalog.Open(layout.Catalog)
	if err != nil {
		return nil, err
	}
	objectStore := store.New(layout)
	rebuildCatalog(cat, objectStore)
	return &Service{layout: layout, catalog: cat, objectStore: objectStore}, nil
}

func (s *Service) Close() error {
	if s == nil || s.catalog == nil {
		return nil
	}
	return s.catalog.Close()
}

func (s *Service) Initialize(ctx context.Context) (Dashboard, error) {
	select {
	case <-ctx.Done():
		return Dashboard{}, ctx.Err()
	default:
	}
	if _, err := home.LoadOrCreateMachineID(s.layout); err != nil {
		return Dashboard{}, err
	}
	if _, err := cryptox.EnsureIdentity(filepath.Join(s.layout.Keys, "identity.txt")); err != nil {
		return Dashboard{}, err
	}
	return s.Dashboard(ctx)
}

func (s *Service) Dashboard(ctx context.Context) (Dashboard, error) {
	select {
	case <-ctx.Done():
		return Dashboard{}, ctx.Err()
	default:
	}
	items, err := s.catalog.List(catalog.Filter{})
	if err != nil {
		return Dashboard{}, err
	}
	cfg, err := config.Load(s.layout.Config)
	if err != nil {
		return Dashboard{}, err
	}
	result := Dashboard{
		SchemaVersion: 1,
		Version:       "0.2.0-dev",
		Home:          s.layout.Root,
		Health:        s.health(),
		Stats: DashboardStats{
			Runs:   len(items),
			Stores: len(cfg.Stores),
		},
	}
	for _, item := range items {
		if item.IntegrityStatus == "verified" {
			result.Stats.Verified++
		} else {
			result.Stats.NeedsAttention++
		}
	}
	for i, item := range items {
		if i == 5 {
			break
		}
		result.RecentRuns = append(result.RecentRuns, RunCard{
			ID:         item.ID,
			Title:      item.Title,
			Repository: shortRepository(item.RepoID),
			Agent:      item.AgentPlatform,
			CreatedAt:  item.CreatedAt.Local().Format("Jan 02 · 15:04"),
			Integrity:  item.IntegrityStatus,
			SyncStatus: "local",
			Relation:   item.Relation,
		})
	}
	for _, configured := range cfg.Stores {
		detail := configured.URL
		if configured.Type == "ssh" {
			detail = "Encrypted remote"
		}
		result.Stores = append(result.Stores, StoreCard{
			Name: configured.Name, Type: configured.Type, Status: "configured", Detail: detail,
		})
	}
	return result, nil
}

func (s *Service) health() []HealthCheck {
	checks := []HealthCheck{{
		ID: "home", Label: "Session home", Status: "ready", Detail: s.layout.Root,
	}}
	if path, err := exec.LookPath("git"); err == nil {
		checks = append(checks, HealthCheck{
			ID: "git", Label: "Git", Status: "ready", Detail: filepath.Base(path),
		})
	} else {
		checks = append(checks, HealthCheck{
			ID: "git", Label: "Git", Status: "failed", Detail: "Git was not found",
		})
	}
	if root, err := codex.StateRoot(); err == nil {
		status, detail := "ready", "Codex sessions available"
		if _, statErr := os.Stat(root); statErr != nil {
			status, detail = "warning", "Codex state not found"
		}
		checks = append(checks, HealthCheck{
			ID: "codex", Label: "Codex", Status: status, Detail: detail,
		})
	}
	identityPath := filepath.Join(s.layout.Keys, "identity.txt")
	if identity, err := cryptox.LoadIdentity(identityPath); err == nil {
		checks = append(checks, HealthCheck{
			ID: "encryption", Label: "Encryption", Status: "ready",
			Detail: identity.Recipient().String(),
		})
	} else {
		checks = append(checks, HealthCheck{
			ID: "encryption", Label: "Encryption", Status: "warning",
			Detail: "Initialize to create an age identity",
		})
	}
	return checks
}

func PreviewDashboard() Dashboard {
	return Dashboard{
		SchemaVersion: 1,
		Preview:       true,
		Version:       "0.2.0-dev",
		Home:          "/tmp/sessionmgr-acceptance",
		Health: []HealthCheck{
			{ID: "home", Label: "Session home", Status: "ready", Detail: "Acceptance sandbox"},
			{ID: "git", Label: "Git", Status: "ready", Detail: "2.52.0"},
			{ID: "codex", Label: "Codex", Status: "ready", Detail: "3 fixture sessions"},
			{ID: "encryption", Label: "Encryption", Status: "ready", Detail: "age1…8x4k"},
		},
		Stats: DashboardStats{Runs: 12, Verified: 10, NeedsAttention: 2, Stores: 2},
		RecentRuns: []RunCard{
			{
				ID: "019fb197-fa7d-7aa1-ae70-43e8e9434c0d", Title: "Implement parser recovery",
				Repository: "sessionmgr", Agent: "codex", CreatedAt: "Today · 14:32",
				Integrity: "verified", SyncStatus: "personal-ssh", Relation: "capture",
			},
			{
				ID: "019fb0c1-5d42-70e8-8a2f-19e5c7cc4d31", Title: "Fix binary patch restore",
				Repository: "sessionmgr", Agent: "codex", CreatedAt: "Today · 11:08",
				Integrity: "verified", SyncStatus: "local", Relation: "revision",
			},
			{
				ID: "019fae91-940c-74f3-925c-10c88748f429", Title: "Investigate SSH retry",
				Repository: "capsule-lab", Agent: "codex", CreatedAt: "Yesterday · 18:47",
				Integrity: "warning", SyncStatus: "not pushed", Relation: "fork",
			},
		},
		Stores: []StoreCard{
			{Name: "local", Type: "file", Status: "ready", Detail: "12 Runs"},
			{Name: "personal-ssh", Type: "ssh", Status: "ready", Detail: "Encrypted · 8 Runs"},
		},
	}
}

func rebuildCatalog(cat *catalog.Catalog, objectStore *store.Store) {
	runIDs, err := objectStore.ListRunIDs()
	if err != nil {
		return
	}
	for _, runID := range runIDs {
		run, err := objectStore.LoadRun(runID)
		if err != nil {
			continue
		}
		digest, err := objectStore.ManifestDigest(runID)
		if err != nil {
			continue
		}
		_ = cat.InsertRun(run, digest)
	}
}

func shortRepository(value string) string {
	if len(value) <= 12 {
		return value
	}
	return fmt.Sprintf("%s…", value[:12])
}
