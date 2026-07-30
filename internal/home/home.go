package home

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Layout struct {
	Root      string
	Config    string
	Catalog   string
	Objects   string
	Runs      string
	Refs      string
	Keys      string
	Cache     string
	Tmp       string
	Reports   string
	Handoffs  string
	MachineID string
}

func Resolve() (Layout, error) {
	root := os.Getenv("SESSIONMGR_HOME")
	if root == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Layout{}, err
		}
		root = filepath.Join(userHome, ".sessionmgr")
	}
	return ForRoot(root)
}

func ForRoot(root string) (Layout, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Layout{}, err
	}
	return Layout{
		Root:      abs,
		Config:    filepath.Join(abs, "config.toml"),
		Catalog:   filepath.Join(abs, "catalog.sqlite"),
		Objects:   filepath.Join(abs, "objects"),
		Runs:      filepath.Join(abs, "runs"),
		Refs:      filepath.Join(abs, "refs"),
		Keys:      filepath.Join(abs, "keys"),
		Cache:     filepath.Join(abs, "cache"),
		Tmp:       filepath.Join(abs, "tmp"),
		Reports:   filepath.Join(abs, "operation-reports"),
		Handoffs:  filepath.Join(abs, "handoff"),
		MachineID: filepath.Join(abs, "machine-id"),
	}, nil
}

func Ensure(layout Layout) error {
	dirs := []string{
		layout.Root, layout.Objects, layout.Runs, filepath.Join(layout.Refs, "runs"),
		layout.Keys, layout.Cache, layout.Tmp, layout.Reports, layout.Handoffs,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	if _, err := os.Stat(layout.Config); os.IsNotExist(err) {
		config := []byte("schema_version = 1\ndefault_store = \"local\"\ntelemetry = false\n\n[capture]\ninclude_untracked = true\ninclude_ignored = false\nmax_file_bytes = 268435456\nmax_total_bytes = 1073741824\n\n[security]\nblock_private_keys = true\nblock_high_confidence_tokens = true\n\n[[stores]]\nname = \"local\"\ntype = \"file\"\nurl = \"" + strings.ReplaceAll(layout.Root, "\\", "\\\\") + "\"\n")
		if err := atomicWrite(layout.Config, config, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func LoadOrCreateMachineID(layout Layout) (string, error) {
	if data, err := os.ReadFile(layout.MachineID); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	id := "machine:uuid:" + hex.EncodeToString(random[:])
	if err := atomicWrite(layout.MachineID, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sessionmgr-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
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
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}
