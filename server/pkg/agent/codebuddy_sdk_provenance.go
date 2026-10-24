package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxSDKLogBytes 是读 SDK 日志的上限。溯源是"有就给、没有就算了"的附加信息，
// 不能为了它把一次已完成的运行拖住，也不能把大文件整个读进内存。
const maxSDKLogBytes = 64 << 20

// SDKTraceSourceMarker 是正式 traces 接口对 sdk_trace_source_path 要求的字面值：
// 它说明 SDK id 取自记录里的哪个字段，而不是本地文件路径。绝对路径属于私有证据，
// 不进这条 API 字段。
const SDKTraceSourceMarker = "providerData.traceId"

// codebuddySDKProvenance 返回正式接口需要的三件套（SDK trace id、文件快照 sha256、
// 来源字段标记）。
//
// 归属按"本轮"判断，不是按整份文件：
//   - 只认 conversationRequestId 等于本轮 CID 的记录；
//   - 记录自带 sessionId / cwd 时，必须与本轮一致；
//   - 这些记录里的 traceId 必须恰好一个，且是 32 位十六进制；
//   - 文件必须读完整（scan 出错或某行解析不了都算未读全），否则不能说"唯一"。
//
// 任一条件不成立就返回空三件套，上层保留未知，不猜。
// resolveSDKConfigDir 决定去哪个配置目录找会话日志。agent 子进程拿到的是过滤后的
// 环境，配置目录不一定在里面；daemon 进程自身的环境里通常有（runtime_mcp 用同一个
// 变量），都没有时按 CLI 的约定回落到 $HOME/.codebuddy。解析不出就返回空，让溯源
// 保持未知，而不是到处乱找。
func resolveSDKConfigDir(env map[string]string) string {
	// key 存在就用它：buildEnv 会用显式值覆盖进程环境，空字符串也一样覆盖。
	// 所以 "显式空值" 和 "没设置" 不是一回事——前者不能回读进程环境。
	if dir, ok := env["CODEBUDDY_CONFIG_DIR"]; ok {
		return strings.TrimSpace(dir)
	}
	if dir := strings.TrimSpace(os.Getenv("CODEBUDDY_CONFIG_DIR")); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".codebuddy")
	}
	return ""
}

func codebuddySDKProvenance(env map[string]string, sessionID, cwd, cid string) (traceID, sum, source string) {
	traceID, sum, _, _ = codebuddySDKProvenanceWithSource(env, sessionID, cwd, cid)
	return traceID, sum, sourceOrEmpty(traceID)
}

func sourceOrEmpty(traceID string) string {
	if traceID == "" {
		return ""
	}
	return SDKTraceSourceMarker
}

// codebuddySDKProvenanceWithSource 与上面同义，额外返回本地文件路径，供日志与私有
// 证据清单复核；该路径不会被写进 API 字段。
func codebuddySDKProvenanceWithSource(env map[string]string, sessionID, cwd, cid string) (traceID, sum, source, filePath string) {
	configDir := resolveSDKConfigDir(env)
	if configDir == "" || sessionID == "" || cid == "" {
		return "", "", "", ""
	}
	matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) != 1 {
		return "", "", "", ""
	}
	filePath = matches[0]
	// 只读普通文件：FIFO/字符设备/符号链接一律当未知。ReadFile 在 FIFO 上会阻塞，
	// 而这段代码跑在"发核心 Result 之前"，堵住它会连带把取消时收回 Result 的预算
	// 耗尽——宁可不给溯源，也不能拖住已知的 usage/session/CID。
	info, err := os.Lstat(filePath)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", "", ""
	}
	if info.Size() > maxSDKLogBytes {
		return "", "", "", ""
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", "", "", ""
	}
	defer func() { _ = file.Close() }()
	// 再按上限读一次：Lstat 与 open 之间文件仍可能变大。
	raw, err := io.ReadAll(io.LimitReader(file, maxSDKLogBytes+1))
	if err != nil || int64(len(raw)) > maxSDKLogBytes {
		return "", "", "", ""
	}

	ids := map[string]struct{}{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record struct {
			SessionID    string `json:"sessionId"`
			Cwd          string `json:"cwd"`
			ProviderData struct {
				ConversationRequestID string `json:"conversationRequestId"`
				TraceID               string `json:"traceId"`
			} `json:"providerData"`
		}
		if json.Unmarshal(line, &record) != nil {
			// 半行/截断的场景：这份文件不能证明"唯一"。
			return "", "", "", ""
		}
		pd := record.ProviderData
		if pd.TraceID == "" || pd.ConversationRequestID != cid {
			continue
		}
		if record.SessionID != "" && record.SessionID != sessionID {
			continue
		}
		if record.Cwd != "" && cwd != "" && record.Cwd != cwd {
			continue
		}
		ids[pd.TraceID] = struct{}{}
		if len(ids) > 1 {
			return "", "", "", ""
		}
	}
	if scanner.Err() != nil || len(ids) != 1 {
		return "", "", "", ""
	}
	for id := range ids {
		if len(id) != 32 || !isLowerHex(id) {
			return "", "", "", ""
		}
		traceID = id
	}
	digest := sha256.Sum256(raw)
	return traceID, hex.EncodeToString(digest[:]), SDKTraceSourceMarker, filePath
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return !strings.ContainsAny(value, " \t")
}

// codebuddySessionTranscriptMissing 判断本轮要 resume 的会话在本地是否确定没有记录。
//
// 配置目录的解析必须和子进程看到的一致：子进程环境是 os.Environ() 再叠加 cfg.Env，
// 所以 cfg.Env 没写但 daemon 进程环境里有 CODEBUDDY_CONFIG_DIR 时，CLI 用的是后者
// ——只看 cfg.Env 会把"能续接"误判成"缺文件"。
//
// 结论只允许在"确定不存在"时给出：目录 stat 失败、项目目录不可读（例如 0111）、
// 枚举出错，都算未知（返回 false），继续走原来的续接流程。Glob 会静默吞掉读取
// 权限错误，所以这里逐层 ReadDir 并检查错误，而不是用 Glob 的空结果当证据。
func codebuddySessionTranscriptMissing(env map[string]string, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	dir := resolveSDKConfigDir(env)
	if dir == "" {
		// 解析不出（未设置且进程环境也没有，或显式置空）：我们对磁盘一无所知。
		return false
	}
	root := filepath.Join(dir, "projects")
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	target := sessionID + ".jsonl"
	sawProjectDir := false
	for _, entry := range entries {
		if !entry.IsDir() {
			// 符号链接等项目条目没有核实过内容，不能据此认定"整棵树都可读且不存在"。
			if entry.Type()&os.ModeSymlink != 0 {
				return false
			}
			continue
		}
		sawProjectDir = true
		files, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			// 读不进去就证明不了"没有"，保持未知。
			return false
		}
		for _, file := range files {
			if file.Name() == target {
				return false
			}
		}
	}
	return sawProjectDir
}
