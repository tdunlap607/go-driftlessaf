/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package metapathreconciler

import (
	"fmt"
	"os"

	gogit "github.com/go-git/go-git/v5"
	"gopkg.in/yaml.v3"
)

// repoConfig holds per-repo configuration loaded from
// .{identity}.yaml at the root of a target repository.
type repoConfig struct {
	Mode *Mode `yaml:"mode,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler so Mode can be used in YAML structs.
// It reuses the same string-parsing logic as EnvDecode.
func (m *Mode) UnmarshalYAML(value *yaml.Node) error {
	return m.EnvDecode(value.Value)
}

// loadRepoConfig reads .{identity}.yaml from the root of the worktree.
// For example, a reconciler with identity "skillup" reads ".skillup.yaml".
//
//   - If the file does not exist, ModeNone is returned (the reconciler skips the repo).
//   - If the file exists but has no mode field, ModeFix is returned (safe default).
//   - If the file exists with a mode field, that mode is returned.
func loadRepoConfig(wt *gogit.Worktree, identity string) (Mode, error) {
	path := fmt.Sprintf(".%s.yaml", identity)
	f, err := wt.Filesystem.Open(path)
	if os.IsNotExist(err) {
		return ModeNone, nil
	}
	if err != nil {
		return ModeNone, fmt.Errorf("open repo config: %w", err)
	}
	defer f.Close()

	var cfg repoConfig
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return ModeNone, fmt.Errorf("decode repo config: %w", err)
	}
	if cfg.Mode == nil {
		return ModeFix, nil
	}
	return *cfg.Mode, nil
}
