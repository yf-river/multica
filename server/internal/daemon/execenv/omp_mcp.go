// 为 omp 提供者把侧车（sidecar）的 mcp_config 落到任务工作目录下：
// 生成 ".omp/mcp.json"，并把创建动作记录到 manifest，方便后续清理/回滚。

package execenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
)

// prepareOmpMcpConfig 若 provider 是 "omp" 且配置非空，则在 workDir 下
// 写出 .omp/mcp.json，目录/文件操作均通过 record* 系列登记到 manifest。
// provider 不匹配或配置为空时直接跳过（返回 nil）。
// 目标文件已存在时返回明确错误，避免覆盖用户已有配置。
func prepareOmpMcpConfig(workDir, provider string, raw json.RawMessage, manifest *sidecarManifest) error {
	if provider != "omp" {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if workDir == "" {
		return errors.New("managed mcp_config requires a working directory")
	}
	configDir := filepath.Join(workDir, "."+provider)
	if err := recordMkdirAll(configDir, 0o700, manifest); err != nil {
		return err
	}
	path := filepath.Join(configDir, "mcp.json")
	if err := recordWriteFile(path, raw, 0o600, manifest); err != nil {
		if errors.Is(err, errPathPreExists) {
			return errors.New("managed mcp_config would overwrite existing " + path)
		}
		return err
	}
	return nil
}
