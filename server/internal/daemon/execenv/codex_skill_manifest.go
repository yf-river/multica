package execenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

type codexSkillManifestEntry struct {
	MetadataError string `json:"metadata_error,omitempty"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Path          string `json:"path"`
	ResolvedPath  string `json:"resolved_path"`
	SHA256        string `json:"sha256"`
}

// 清单来自实际铺设的文件，供诊断路径与内容版本；原生 Codex 仍负责技能发现与按需读取。
func writeCodexSkillManifest(home string) error {
	entries := []codexSkillManifestEntry{}
	var inspect func(string) error
	inspect = func(dir string) error {
		children, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, child := range children {
			path := filepath.Join(dir, child.Name())
			if child.Name() == ".system" {
				if err := inspect(path); err != nil {
					return err
				}
				continue
			}
			skill := filepath.Join(path, "SKILL.md")
			data, err := os.ReadFile(skill)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("read installed skill %s: %w", skill, err)
			}
			resolved, err := filepath.EvalSymlinks(skill)
			if err != nil {
				return err
			}
			metadata := struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
			}{Name: child.Name()}
			metadataError := ""
			if frontmatter, _, ok := frontmatterParts(string(data)); ok {
				if err := yaml.Unmarshal([]byte(frontmatter), &metadata); err != nil {
					// 清单描述装载输入，不能因单个用户技能的 YAML 阻止整轮执行。
					metadataError = err.Error()
					metadata.Name = child.Name()
					metadata.Description = ""
				}
			}
			digest := sha256.Sum256(data)
			entries = append(entries, codexSkillManifestEntry{MetadataError: metadataError, Name: metadata.Name, Description: metadata.Description, Path: skill, ResolvedPath: resolved, SHA256: hex.EncodeToString(digest[:])})
		}
		return nil
	}
	if err := inspect(filepath.Join(home, "skills")); err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "skills-manifest.json"), append(data, '\n'), 0600)
}
