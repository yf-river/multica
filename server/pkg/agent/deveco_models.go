package agent

// 文件职责：DevEco Code 的模型发现（自包含，不依赖 OpenCode 的模型解析器）。
// DevEco 维护自己的目录与 CLI，所以发现逻辑放这里而不是复用 parseOpenCodeModels。
// CLI 缺失或目录为空时返回空列表，让调用方（运行时模型选择器）退回手动输入模型，
// 而不是报错卡死。

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// discoverDevecoModels 枚举 `deveco models` 目录：调 DevEco Code CLI 并解析
// `provider/model` 行。发现失败（二进制缺失、非零退出、输出不可解析）静默返回
// 空列表，让模型选择降级到手动输入而不是卡住。
func discoverDevecoModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "deveco"
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return []Model{}, nil
	}
	// `deveco models` 首次运行可能联网同步托管目录；给个宽裕的超时，免得冷启动时
	// 选择器是空的。
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := runtimeCmd.exec(runCtx, "models")
	hideAgentWindow(cmd)
	out, _ := outputOwned(cmd, runtimeCmd.logger)
	models := parseDevecoModels(string(out))
	if len(models) == 0 {
		return []Model{}, nil
	}
	return models, nil
}

// parseDevecoModels 从 `deveco models` 输出里抽取 `provider/model` id。每行非空时，
// 第一个以空白分隔的 token 只要含 `/`、且不像表头或 JSON，就当模型 id 处理。
// 输出去重、保持先见顺序。这里不解析按模型变体/思考档位的标注——本版本对 DevEco
// 不提供思考档位选择器（见 thinking.go）。
func parseDevecoModels(output string) []Model {
	var models []Model
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		id := fields[0]
		// 跳过 JSON 对象/数组、带引号字符串，以及 "PROVIDER/MODEL" 这类表头。
		if strings.HasPrefix(id, "{") || strings.HasPrefix(id, "[") || strings.HasPrefix(id, "\"") {
			continue
		}
		if !strings.Contains(id, "/") {
			continue
		}
		// 全大写多半是表头或非模型文本，跳过。
		if id == strings.ToUpper(id) {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		provider := ""
		if slash := strings.Index(id, "/"); slash > 0 {
			provider = id[:slash]
		}
		models = append(models, Model{ID: id, Label: id, Provider: provider})
	}
	return models
}
