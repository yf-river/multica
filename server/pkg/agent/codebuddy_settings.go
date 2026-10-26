package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// 人工关闭 hooks 时，普通执行采用完整任务环境与原生指令文件，避免常驻根进程
// 在没有轮次环境投递器的情况下执行。无法解析的原生设置交由 CLI 自身处理。
func codebuddyTaskHooksPermitted(cfg Config, opts ExecOptions) bool {
	settings, err := codebuddyHookSettings(opts)
	if err != nil || codebuddySettingsDisableHooks(settings) {
		return false
	}
	config := resolveSDKConfigDir(cfg.Env)
	if config == "" {
		return false
	}
	paths := []string{filepath.Join(config, "settings.json")}
	for dir := opts.Cwd; dir != ""; dir = filepath.Dir(dir) {
		paths = append(paths, filepath.Join(dir, ".codebuddy", "settings.json"), filepath.Join(dir, ".codebuddy", "settings.local.json"))
		if dir == filepath.Dir(dir) {
			break
		}
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var settings map[string]json.RawMessage
		if err != nil || json.Unmarshal(raw, &settings) != nil || settings == nil || codebuddySettingsDisableHooks(settings) {
			return false
		}
	}
	return true
}

func codebuddySettingsDisableHooks(settings map[string]json.RawMessage) bool {
	for _, key := range []string{"disableAllHooks", "allowManagedHooksOnly"} {
		var disabled bool
		if json.Unmarshal(settings[key], &disabled) == nil && disabled {
			return true
		}
	}
	return false
}

// 自定义参数沿用 CLI 的最后一次 --settings；任务策略叠加在该设置之上。
// hooks、权限列表等数组合并，防止托管设置覆盖人工规则。
func codebuddyHookSettings(opts ExecOptions) (map[string]json.RawMessage, error) {
	var selected string
	args := append(append([]string(nil), opts.ExtraArgs...), opts.CustomArgs...)
	for i := 0; i < len(args); i++ {
		if args[i] == "--settings" {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return nil, errors.New("CodeBuddy --settings requires a file or JSON object")
			}
			i++
			selected = args[i]
		} else if strings.HasPrefix(args[i], "--settings=") {
			selected = strings.TrimPrefix(args[i], "--settings=")
			if selected == "" {
				return nil, errors.New("CodeBuddy --settings requires a file or JSON object")
			}
		}
	}
	settings := map[string]json.RawMessage{}
	for _, source := range []string{selected, opts.CodebuddySettingsPath} {
		if source == "" {
			continue
		}
		raw := []byte(strings.TrimSpace(source))
		if !bytes.HasPrefix(raw, []byte("{")) {
			name := source
			if !filepath.IsAbs(name) {
				name = filepath.Join(opts.Cwd, name)
			}
			var ok bool
			raw, ok = readRegularFileBounded(name)
			if !ok {
				return nil, errors.New("CodeBuddy hook settings source is unavailable")
			}
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return nil, errors.New("CodeBuddy hook settings source must be an object")
		}
		for key, value := range object {
			settings[key] = mergeCodebuddySetting(settings[key], value)
		}
	}
	return settings, nil
}

func mergeCodebuddySetting(left, right json.RawMessage) json.RawMessage {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(left, &a) == nil && a != nil && json.Unmarshal(right, &b) == nil && b != nil {
		for key, value := range b {
			a[key] = mergeCodebuddySetting(a[key], value)
		}
		raw, _ := json.Marshal(a)
		return raw
	}
	var x, y []json.RawMessage
	if json.Unmarshal(left, &x) == nil && x != nil && json.Unmarshal(right, &y) == nil && y != nil {
		seen := map[string]bool{}
		merged := make([]json.RawMessage, 0, len(x)+len(y))
		for _, value := range append(x, y...) {
			var decoded any
			_ = json.Unmarshal(value, &decoded)
			canonical, _ := json.Marshal(decoded)
			if !seen[string(canonical)] {
				merged = append(merged, value)
				seen[string(canonical)] = true
			}
		}
		raw, _ := json.Marshal(merged)
		return raw
	}
	return right
}

func codebuddySettingsHaveMCP(settings map[string]json.RawMessage) bool {
	for key := range settings {
		if strings.Contains(strings.ToLower(key), "mcp") {
			return true
		}
	}
	return false
}
