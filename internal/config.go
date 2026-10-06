package internal

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// PreflightCheck is one path-globbed local command set from .cix.yml.
type PreflightCheck struct {
	Name     string   `yaml:"name"`
	Paths    []string `yaml:"paths"`
	Run      []string `yaml:"run"`
	Optional bool     `yaml:"optional"`
}

// ArtifactCfg configures the release artifact gate.
type ArtifactCfg struct {
	APIURLMustNotMatch []string `yaml:"api_url_must_not_match"`
	ForbidDeps         []string `yaml:"forbid_deps"`
	RequireGradleTasks []string `yaml:"require_gradle_tasks"`
	RejectGradleTasks  []string `yaml:"reject_gradle_tasks"`
}

// Config mirrors .cix.yml. All fields optional.
type Config struct {
	Checks       []PreflightCheck `yaml:"checks"`
	Verify       string           `yaml:"verify"`
	ArtifactJobs []string         `yaml:"artifact_jobs"`
	Artifact     ArtifactCfg      `yaml:"artifact"`
	Gate         string           `yaml:"gate"`
	DefaultRef   string           `yaml:"default_ref"`
	Signatures   []SigRule        `yaml:"signatures"`
}

// DefaultConfig returns the built-in defaults.
func DefaultConfig() Config {
	return Config{
		ArtifactJobs: []string{"mobile"},
		Artifact: ArtifactCfg{
			APIURLMustNotMatch: []string{"localhost", "127.0.0.1", "10.0.2.2"},
			ForbidDeps:         []string{"expo-dev-client"},
			RequireGradleTasks: []string{"bundleRelease"},
			RejectGradleTasks:  []string{"assembleDebug", "bundleDebug"},
		},
	}
}

// LoadConfig reads .cix.yml if present, else returns defaults.
func LoadConfig(root string) (Config, error) {
	cfg := DefaultConfig()
	p := filepath.Join(root, ".cix.yml")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("cannot read %s: %w", p, err)
	}
	var raw Config
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return cfg, fmt.Errorf("malformed .cix.yml: %w", err)
	}
	if len(raw.Checks) > 0 {
		cfg.Checks = raw.Checks
	}
	cfg.Verify = raw.Verify
	if len(raw.ArtifactJobs) > 0 {
		cfg.ArtifactJobs = raw.ArtifactJobs
	}
	if len(raw.Artifact.APIURLMustNotMatch) > 0 {
		cfg.Artifact.APIURLMustNotMatch = raw.Artifact.APIURLMustNotMatch
	}
	if len(raw.Artifact.ForbidDeps) > 0 {
		cfg.Artifact.ForbidDeps = raw.Artifact.ForbidDeps
	}
	if len(raw.Artifact.RequireGradleTasks) > 0 {
		cfg.Artifact.RequireGradleTasks = raw.Artifact.RequireGradleTasks
	}
	if len(raw.Artifact.RejectGradleTasks) > 0 {
		cfg.Artifact.RejectGradleTasks = raw.Artifact.RejectGradleTasks
	}
	cfg.Gate = raw.Gate
	cfg.DefaultRef = raw.DefaultRef
	cfg.Signatures = raw.Signatures
	if err := CompileSigRules(cfg.Signatures); err != nil {
		return cfg, fmt.Errorf(".cix.yml: %w", err)
	}
	return cfg, nil
}
