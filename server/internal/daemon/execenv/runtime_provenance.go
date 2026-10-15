package execenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteRuntimeProvenance 记录平台生成的提示来源与指纹，不复制可能包含凭据的正文。
func WriteRuntimeProvenance(envRoot, workDir, provider, content string, ctx TaskContextForEnv) error {
	hash := sha256.Sum256([]byte(content))
	skills := make([]string, 0, len(ctx.AgentSkills))
	for _, skill := range modelVisibleSkills(ctx.AgentSkills) {
		skills = append(skills, skill.Name)
	}
	ancestors := []string{}
	for dir := filepath.Dir(workDir); ; dir = filepath.Dir(dir) {
		for _, name := range []string{"AGENTS.md", "AGENTS.override.md", "CLAUDE.md"} {
			path := filepath.Join(dir, name)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				ancestors = append(ancestors, path)
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	data, err := json.MarshalIndent(map[string]any{
		"version": 1, "provider": provider, "runtime_file": runtimeConfigPath(workDir, provider),
		"sha256": hex.EncodeToString(hash[:]), "skills": skills,
		"ancestor_instruction_candidates": ancestors,
		"squad_leader":                    ctx.IsSquadLeader,
		"sources":                         []string{"agent identity", "workspace context", "task context", "runtime_config_sections.go", "per-turn prompt.go"},
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(envRoot, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(envRoot, "runtime-instructions.json"), append(data, '\n'), 0600)
}
