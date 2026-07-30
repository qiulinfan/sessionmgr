package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	SchemaVersion int            `toml:"schema_version"`
	DefaultStore  string         `toml:"default_store"`
	Telemetry     bool           `toml:"telemetry"`
	Capture       CaptureConfig  `toml:"capture"`
	Security      SecurityConfig `toml:"security"`
	Stores        []StoreConfig  `toml:"stores"`
}

type CaptureConfig struct {
	IncludeUntracked bool  `toml:"include_untracked"`
	IncludeIgnored   bool  `toml:"include_ignored"`
	MaxFileBytes     int64 `toml:"max_file_bytes"`
	MaxTotalBytes    int64 `toml:"max_total_bytes"`
}

type SecurityConfig struct {
	BlockPrivateKeys          bool `toml:"block_private_keys"`
	BlockHighConfidenceTokens bool `toml:"block_high_confidence_tokens"`
}

type StoreConfig struct {
	Name          string   `toml:"name"`
	Type          string   `toml:"type"`
	URL           string   `toml:"url"`
	AgeRecipients []string `toml:"age_recipients"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var result Config
	if _, err := toml.Decode(string(data), &result); err != nil {
		return Config{}, err
	}
	if result.SchemaVersion != 1 {
		return Config{}, fmt.Errorf("unsupported config schema %d", result.SchemaVersion)
	}
	if result.DefaultStore == "" {
		result.DefaultStore = "local"
	}
	if result.Capture.MaxFileBytes == 0 {
		result.Capture.MaxFileBytes = 256 * 1024 * 1024
	}
	if result.Capture.MaxTotalBytes == 0 {
		result.Capture.MaxTotalBytes = 1024 * 1024 * 1024
	}
	return result, nil
}

func (c Config) Store(name string) (StoreConfig, error) {
	if name == "" {
		name = c.DefaultStore
	}
	for _, candidate := range c.Stores {
		if candidate.Name == name {
			return candidate, nil
		}
	}
	return StoreConfig{}, fmt.Errorf("store %q is not configured", name)
}
