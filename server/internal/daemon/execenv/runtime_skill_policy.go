// 运行时技能禁用策略：把"本任务禁用哪些技能"落成各 provider 真正会读的
// 配置——Claude Code / CodeBuddy 使用任务专属 settings 的 skillOverrides
// 与 permissions.deny；Codex 则在 config 里追加 [[skills.config]] 禁用块。

package execenv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const claudeRuntimeSkillSettingsFile = "claude-runtime-skill-settings.json"
const codebuddyRuntimeSkillSettingsFile = "codebuddy-runtime-skill-settings.json"

// RuntimeSkillRefForEnv 标识一个"运行时本地"技能，用于按 provider 过滤任务
// 环境。provider 与 runtime 已由任务选定，所以这里只需发现根与 provider
// 原生的键。
type RuntimeSkillRefForEnv struct {
	Root   string
	Key    string
	Name   string
	Plugin string
}

// cleanRuntimeSkillKey 规整一个技能键并做路径安全校验：
// 去空白、统一平台斜杠、清理冗余段；若结果是 "."、".."、绝对路径或越出
// 根目录的相对路径则拒绝（返回 ok=false），防止用配置把禁用规则写到任意位置。
func cleanRuntimeSkillKey(key string) (string, bool) {
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimSpace(key)))
	if cleaned == "." || filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(cleaned), true
}

// prepareClaudeSkillSettings 在 envRoot 下生成/更新 Claude Code 的运行时技能
// 设置文件。无禁用项时删除旧文件并返回空串（幂等）。返回设置文件路径。
//
// 产出两层机制：skillOverrides 只对"个人/项目技能"完全隐藏生效；插件技能
// 会忽略该设置，因此对每个键额外补发 permissions.deny，它才是插件的强制路径。
func prepareClaudeSkillSettings(envRoot string, disabled []RuntimeSkillRefForEnv, workspaceSkills []SkillContextForEnv) (string, error) {
	return prepareSkillSettingsFile(filepath.Join(envRoot, claudeRuntimeSkillSettingsFile), disabled, workspaceSkills)
}

// CodeBuddy 的 skillOverrides 与 Skill 权限拒绝规则通过任务专属 --settings 生效。
func prepareCodebuddySkillSettings(envRoot string, disabled []RuntimeSkillRefForEnv, workspaceSkills []SkillContextForEnv) (string, error) {
	return prepareSkillSettingsFile(filepath.Join(envRoot, codebuddyRuntimeSkillSettingsFile), disabled, workspaceSkills)
}

func prepareSkillSettingsFile(path string, disabled []RuntimeSkillRefForEnv, workspaceSkills []SkillContextForEnv) (string, error) {
	if len(disabled) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return "", nil
	}

	overrides := make(map[string]string)
	deny := make([]string, 0, len(disabled)*2)
	seenDeny := make(map[string]struct{}, len(disabled)*2)
	addDeny := func(rule string) {
		if _, exists := seenDeny[rule]; exists {
			return
		}
		seenDeny[rule] = struct{}{}
		deny = append(deny, rule)
	}
	for _, skill := range disabled {
		key, ok := cleanRuntimeSkillKey(skill.Key)
		if !ok {
			continue
		}
		// 禁用的"调用名"：显式给出则用之，否则退回技能键的末段。
		invocationName := strings.TrimSpace(skill.Name)
		if invocationName == "" {
			invocationName = filepath.Base(filepath.FromSlash(key))
		}
		// 若同名技能其实是本任务工作区自带的（会被重新写入），则不受这条
		// 运行时禁用影响，跳过。
		if workspaceClaimsRuntimeSkill(invocationName, workspaceSkills) {
			continue
		}
		// 两种 CLI 的 skillOverrides 能完全隐藏个人/项目技能；插件技能
		// 会无视该设置，所以下面还要为每个键补发 permissions.deny——
		// 那才是对插件的强制手段。
		if skill.Root != "plugin" {
			overrides[invocationName] = "off"
		} else {
			// 插件按完整键名禁用。
			invocationName = key
		}
		addDeny("Skill(" + invocationName + ")")
		addDeny("Skill(" + invocationName + " *)")
	}
	if len(overrides) == 0 && len(deny) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return "", nil
	}
	payload := map[string]any{
		"skillOverrides": overrides,
		"permissions": map[string]any{
			"deny": deny,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ensureCodexDisabledSkillsConfig 把禁用技能追加进 Codex 的 configPath：
// 为每个技能键追加一段 `[[skills.config]] ... enabled = false`。
// 只处理 provider（codex-home/skills 下）与 universal（用户 ~/.agents/skills）
// 两类根，按完整技能路径去重。
func ensureCodexDisabledSkillsConfig(configPath, codexHome string, disabled []RuntimeSkillRefForEnv, workspaceSkills []SkillContextForEnv) error {
	if len(disabled) == 0 {
		return nil
	}
	home := ""
	paths := make([]string, 0, len(disabled))
	seen := make(map[string]struct{}, len(disabled))
	for _, skill := range disabled {
		key, ok := cleanRuntimeSkillKey(skill.Key)
		if !ok {
			continue
		}
		var skillPath string
		switch skill.Root {
		case "provider":
			// 只有技能的顶层目录段属于该 provider；若它是工作区自带的同名
			// 技能（会重写进本任务环境），则不受影响，跳过。
			firstKeyPart := strings.SplitN(key, "/", 2)[0]
			if workspaceClaimsRuntimeSkill(firstKeyPart, workspaceSkills) {
				continue
			}
			skillPath = filepath.Join(codexHome, "skills", filepath.FromSlash(key), "SKILL.md")
		case "universal":
			if home == "" {
				var err error
				home, err = os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("resolve user home for disabled Codex skills: %w", err)
				}
			}
			skillPath = filepath.Join(home, ".agents", "skills", filepath.FromSlash(key), "SKILL.md")
		default:
			continue
		}
		if _, exists := seen[skillPath]; exists {
			continue
		}
		seen[skillPath] = struct{}{}
		paths = append(paths, skillPath)
	}
	if len(paths) == 0 {
		return nil
	}
	file, err := os.OpenFile(configPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, path := range paths {
		block := "\n[[skills.config]]\npath = " + strconv.Quote(filepath.ToSlash(path)) + "\nenabled = false\n"
		if _, err := file.WriteString(block); err != nil {
			return err
		}
	}
	return nil
}

// workspaceClaimsRuntimeSkill 判断名为 name 的技能是否被本任务工作区自带的
// 某个技能"认领"（按各自 sanitizeSkillName 后的规范名比较）。被认领的技能
// 会被重新写入任务环境，不应受用户级/运行时级禁用影响。
func workspaceClaimsRuntimeSkill(name string, workspaceSkills []SkillContextForEnv) bool {
	claim := sanitizeSkillName(name)
	for _, skill := range workspaceSkills {
		if sanitizeSkillName(skill.Name) == claim {
			return true
		}
	}
	return false
}
