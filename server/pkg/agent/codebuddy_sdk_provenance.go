package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// codebuddySDKProvenance 从 CodeBuddy 自己落盘的会话日志里取 SDK 侧的 trace id。
//
// 三个标识必须分开：
//   - CID（ProviderTraceID）是子进程在 stream-json 事件里自报的根请求 id；
//   - SDK trace id 是 SDK 日志 traceparent 的 trace-id 段，只有本地文件里才有；
//   - AgentLens 的 trace/span 是外部观测系统的另一套编号。
//
// 这里只做"归属唯一才给"的事：按 <config>/projects/<任意工作目录>/<session>.jsonl
// 定位，必须恰好一个文件、且文件内 trace id 恰好一个；任一条件不满足就返回空，
// 让上层保留未知，而不是按时间或目录名猜。
func codebuddySDKProvenance(configDir, sessionID string) (traceID, sum, path string) {
	if configDir == "" || sessionID == "" {
		return "", "", ""
	}
	matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) != 1 {
		return "", "", ""
	}
	filePath := matches[0]
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", ""
	}
	ids := map[string]struct{}{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var record struct {
			ProviderData struct {
				TraceID string `json:"traceId"`
			} `json:"providerData"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if id := record.ProviderData.TraceID; id != "" {
			ids[id] = struct{}{}
			if len(ids) > 1 {
				return "", "", ""
			}
		}
	}
	if len(ids) != 1 {
		return "", "", ""
	}
	for id := range ids {
		traceID = id
	}
	digest := sha256.Sum256(raw)
	return traceID, hex.EncodeToString(digest[:]), filePath
}
