package execenv

// 文件导读：本文件实现 Cursor 的「受管 MCP」落地——当 agent 保存了显式的
// managed mcp_config 时，把配置物化成 Cursor 原生的 sidecar。
//
// 产出物有四处：(1) 项目根/.cursor/mcp.json——声明要加载的 MCP server；
// (2) env root 下一个隔离的 cursor-data/projects/... 数据目录，里面放只含受管 server
// 的 mcp-approvals.json 与 .workspace-trusted 信任标记（Cursor 仍会读全局
// ~/.cursor/mcp.json，但只有在这个 per-task 数据目录里有审批的 server 才能加载，
// 用户全局 server 不会泄漏进受管运行）；(3) 有 mcp-auth 来源时把 mcp-auth.json 链/
// 复制进该目录；(4) daemon 把该数据目录导出为 CURSOR_DATA_DIR。
//
// 审批键用「project path + 归一化 server 配置」的 SHA-256 前缀仿照 Cursor 自己的
// 算法，且 JSON 序列化关闭了 Go 默认 HTML 转义，保证与 Cursor JSON.stringify 的
// 字节一致——否则 Cursor 认不出同一 server 而重新要审批。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// CursorMcpAuthSourceEnv 是 daemon 在启动 cursor-agent 前消费的一个 agent
	// custom_env 键。设置时它必须指向一份显式的 mcp-auth.json，或指向一个包含它的
	// Cursor 项目数据目录。键名刻意不用 MULTICA_* 前缀，因为 custom_env 会拦下用户
	// 设置带该前缀的变量。
	CursorMcpAuthSourceEnv = "CURSOR_MCP_AUTH_SOURCE"

	// cursorWorkspaceTrustedFile 是 Cursor 记录「信任某 workspace」的标记文件名。
	cursorWorkspaceTrustedFile = ".workspace-trusted"
	// cursorMcpAuthFile 是 Cursor 的 MCP 鉴权文件（mcp-auth.json）的文件名。
	cursorMcpAuthFile = "mcp-auth.json"
)

// cursorMcpConfigFile 是 .cursor/mcp.json 的顶层结构：server 名 → 原始 server 配置。
type cursorMcpConfigFile struct {
	McpServers map[string]json.RawMessage `json:"mcpServers"`
}

// cursorStdioMcpApprovalServer 是 Cursor 做审批哈希前归一化后的 stdio server 形态。
type cursorStdioMcpApprovalServer struct {
	Type    json.RawMessage `json:"type,omitempty"`
	Command json.RawMessage `json:"command"`
	Args    json.RawMessage `json:"args,omitempty"`
	Env     json.RawMessage `json:"env,omitempty"`
	Cwd     json.RawMessage `json:"cwd,omitempty"`
}

// cursorRemoteMcpApprovalServer 是 Cursor 做审批哈希前归一化后的 remote server 形态。
type cursorRemoteMcpApprovalServer struct {
	Type    json.RawMessage `json:"type,omitempty"`
	URL     json.RawMessage `json:"url"`
	Headers json.RawMessage `json:"headers,omitempty"`
}

// prepareCursorMcpConfig 为「保存了显式 managed mcp_config」的 agent 写 Cursor 原生
// 的 MCP sidecar。nil/null 的 mcp_config 表示「让 Cursor 按正常行为走」，因此既不建
// .cursor/mcp.json 也不建 CURSOR_DATA_DIR。
func prepareCursorMcpConfig(envRoot, workDir string, mcpConfig json.RawMessage, mcpAuthSource string, manifest *sidecarManifest) (string, error) {
	if !hasManagedCursorMcpConfig(mcpConfig) {
		return "", nil
	}
	if envRoot == "" {
		return "", fmt.Errorf("env root is required for managed cursor mcp_config")
	}

	projectRoot := cursorProjectRoot(workDir)
	servers, err := parseCursorManagedMcpServers(mcpConfig)
	if err != nil {
		return "", err
	}

	cursorDir := filepath.Join(projectRoot, ".cursor")
	if err := recordMkdirAll(cursorDir, 0o755, manifest); err != nil {
		return "", fmt.Errorf("create .cursor dir: %w", err)
	}
	configData, err := marshalCursorMcpConfig(servers)
	if err != nil {
		return "", err
	}
	if err := recordWriteFile(filepath.Join(cursorDir, "mcp.json"), configData, 0o600, manifest); err != nil {
		if errors.Is(err, errPathPreExists) {
			return "", fmt.Errorf("managed cursor mcp_config would overwrite existing .cursor/mcp.json")
		}
		return "", fmt.Errorf("write .cursor/mcp.json: %w", err)
	}

	cursorDataDir := filepath.Join(envRoot, "cursor-data")
	projectDataDir := filepath.Join(cursorDataDir, "projects", cursorSlugifyPath(projectRoot))
	if err := os.MkdirAll(projectDataDir, 0o700); err != nil {
		return "", fmt.Errorf("create cursor project data dir: %w", err)
	}
	if err := removeCursorMcpAuthFile(projectDataDir); err != nil {
		return "", err
	}
	approvals, err := cursorMcpApprovalKeys(projectRoot, servers)
	if err != nil {
		return "", err
	}
	approvalData, err := json.MarshalIndent(approvals, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal cursor mcp approvals: %w", err)
	}
	if err := os.WriteFile(filepath.Join(projectDataDir, "mcp-approvals.json"), approvalData, 0o600); err != nil {
		return "", fmt.Errorf("write cursor mcp approvals: %w", err)
	}
	trustData, err := json.MarshalIndent(map[string]string{
		"trustedAt":     "1970-01-01T00:00:00Z",
		"workspacePath": projectRoot,
		"trustMethod":   "multica-managed",
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal cursor workspace trust: %w", err)
	}
	if err := os.WriteFile(filepath.Join(projectDataDir, cursorWorkspaceTrustedFile), trustData, 0o600); err != nil {
		return "", fmt.Errorf("write cursor workspace trust: %w", err)
	}
	if strings.TrimSpace(mcpAuthSource) != "" {
		if err := seedCursorMcpAuthFile(projectDataDir, mcpAuthSource); err != nil {
			return "", err
		}
	}

	return cursorDataDir, nil
}

// seedCursorMcpAuthFile 把用户提供的 mcp-auth.json 放进 per-task 数据目录：优先
// symlink（保持与用户源文件同步），失败则复制。
func seedCursorMcpAuthFile(projectDataDir, source string) error {
	sourcePath, err := resolveCursorMcpAuthSource(source)
	if err != nil {
		return err
	}
	target := filepath.Join(projectDataDir, cursorMcpAuthFile)
	if err := os.Symlink(sourcePath, target); err == nil {
		return nil
	}
	if err := copyCursorMcpAuthFile(target, sourcePath); err != nil {
		return fmt.Errorf("seed cursor mcp auth file: %w", err)
	}
	return nil
}

// removeCursorMcpAuthFile 清掉数据目录里可能残留的旧 mcp-auth.json（不存在时忽略）。
func removeCursorMcpAuthFile(projectDataDir string) error {
	target := filepath.Join(projectDataDir, cursorMcpAuthFile)
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove prior cursor mcp auth file: %w", err)
	}
	return nil
}

// resolveCursorMcpAuthSource 把 CURSOR_MCP_AUTH_SOURCE 解析成确定的 mcp-auth.json
// 路径：支持 ~ 展开；目录则补上文件名；最终必须是一个指向 mcp-auth.json 的绝对路径。
func resolveCursorMcpAuthSource(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("%s is empty", CursorMcpAuthSourceEnv)
	}
	if source == "~" || strings.HasPrefix(source, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve %s home directory: %w", CursorMcpAuthSourceEnv, err)
		}
		if source == "~" {
			source = home
		} else {
			source = filepath.Join(home, source[2:])
		}
	}
	if !filepath.IsAbs(source) {
		return "", fmt.Errorf("%s must be an absolute path to %s or its containing Cursor project directory", CursorMcpAuthSourceEnv, cursorMcpAuthFile)
	}
	source = filepath.Clean(source)
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", CursorMcpAuthSourceEnv, err)
	}
	if info.IsDir() {
		source = filepath.Join(source, cursorMcpAuthFile)
		info, err = os.Stat(source)
		if err != nil {
			return "", fmt.Errorf("stat %s %s: %w", CursorMcpAuthSourceEnv, cursorMcpAuthFile, err)
		}
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s must resolve to a file, got directory %s", CursorMcpAuthSourceEnv, source)
	}
	if filepath.Base(source) != cursorMcpAuthFile {
		return "", fmt.Errorf("%s must point at %s, got %s", CursorMcpAuthSourceEnv, cursorMcpAuthFile, filepath.Base(source))
	}
	return source, nil
}

// copyCursorMcpAuthFile 把 source 复制成 target（O_EXCL，0600）；失败时清理半成品。
func copyCursorMcpAuthFile(target, source string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	return nil
}

// hasManagedCursorMcpConfig 报告 mcp_config 是否携带「受管」内容：nil、空或字面量
// null 都视为没有（让 Cursor 正常行为）。
func hasManagedCursorMcpConfig(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// parseCursorManagedMcpServers 从 mcp_config 解析 mcpServers 映射并做基本校验（名字
// 非空、每个 server 是 JSON 对象）。
func parseCursorManagedMcpServers(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var cfg struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse mcp_config json: %w", err)
	}
	if cfg.McpServers == nil {
		return map[string]json.RawMessage{}, nil
	}
	for name, server := range cfg.McpServers {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("mcp server name must not be empty")
		}
		var obj map[string]any
		if err := json.Unmarshal(server, &obj); err != nil {
			return nil, fmt.Errorf("mcp_servers.%s: %w", name, err)
		}
		if obj == nil {
			return nil, fmt.Errorf("mcp_servers.%s must be a JSON object", name)
		}
	}
	return cfg.McpServers, nil
}

// marshalCursorMcpConfig 把 server 映射序列化成 .cursor/mcp.json 的字节（带结尾
// 换行）。
func marshalCursorMcpConfig(servers map[string]json.RawMessage) ([]byte, error) {
	if servers == nil {
		servers = map[string]json.RawMessage{}
	}
	data, err := json.MarshalIndent(cursorMcpConfigFile{McpServers: servers}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal cursor mcp config: %w", err)
	}
	return append(data, '\n'), nil
}

// cursorMcpApprovalKeys 仿照 Cursor 的做法，为每个受管 server 算出审批键：对
// {"path": <projectRoot>,"server": <归一化 server 配置>} 的紧凑 JSON 做 SHA-256，
// 取前 16 位 hex 作为 `<name>-<hash>`。排序保证确定性输出。
func cursorMcpApprovalKeys(projectRoot string, servers map[string]json.RawMessage) ([]string, error) {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	approvals := make([]string, 0, len(names))
	for _, name := range names {
		server, err := marshalCursorMcpApprovalServer(servers[name])
		if err != nil {
			return nil, fmt.Errorf("marshal mcp_servers.%s for cursor approval: %w", name, err)
		}
		pathJSON, err := json.Marshal(projectRoot)
		if err != nil {
			return nil, fmt.Errorf("marshal cursor project root: %w", err)
		}
		payload := []byte(`{"path":`)
		payload = append(payload, pathJSON...)
		payload = append(payload, []byte(`,"server":`)...)
		payload = append(payload, server...)
		payload = append(payload, '}')

		sum := sha256.Sum256(payload)
		approvals = append(approvals, name+"-"+hex.EncodeToString(sum[:])[:16])
	}
	return approvals, nil
}

func marshalCursorMcpApprovalServer(raw json.RawMessage) ([]byte, error) {
	compact := &bytes.Buffer{}
	if err := json.Compact(compact, raw); err != nil {
		return nil, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(compact.Bytes(), &fields); err != nil {
		return nil, err
	}
	if command, isStdio := fields["command"]; isStdio {
		// Cursor 在做哈希前会把 stdio server 归一化成这一精确字段集与顺序；
		// 未知字段丢弃、缺席字段省略。
		return marshalJSONStringifyCompatible(cursorStdioMcpApprovalServer{
			Type:    fields["type"],
			Command: command,
			Args:    fields["args"],
			Env:     fields["env"],
			Cwd:     fields["cwd"],
		})
	}
	if url, isRemote := fields["url"]; isRemote {
		// remote server 在做哈希前使用另一套归一化形态。
		return marshalJSONStringifyCompatible(cursorRemoteMcpApprovalServer{
			Type:    fields["type"],
			URL:     url,
			Headers: fields["headers"],
		})
	}
	return compact.Bytes(), nil
}

// marshalJSONStringifyCompatible 序列化值时不启用 Go 默认的 HTML 转义，使审批载荷
// 字节与 Cursor 的 JSON.stringify 输出一致。
func marshalJSONStringifyCompatible(value any) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// cursorProjectRoot 从 workdir 向上找到最近的含 .git 的目录作为 Cursor 项目根；
// 找不到就退回 workdir 本身（先做 symlink 解析与绝对化）。
func cursorProjectRoot(workDir string) string {
	if workDir == "" {
		return workDir
	}
	dir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		dir = workDir
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		dir = workDir
	}
	fallback := dir
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fallback
		}
		dir = parent
	}
}

// cursorSlugifyPath 把路径转成 Cursor 风格的目录名片段：字母数字保留、其它字符折叠
// 成单个 `-`、首尾的 `-` 去掉。
func cursorSlugifyPath(path string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range path {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
