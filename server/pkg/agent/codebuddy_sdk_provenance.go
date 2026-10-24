package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

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
func codebuddySDKProvenance(configDir, sessionID, cwd, cid string) (traceID, sum, source string) {
	traceID, sum, _, _ = codebuddySDKProvenanceWithSource(configDir, sessionID, cwd, cid)
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
func codebuddySDKProvenanceWithSource(configDir, sessionID, cwd, cid string) (traceID, sum, source, filePath string) {
	if configDir == "" || sessionID == "" || cid == "" {
		return "", "", "", ""
	}
	matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) != 1 {
		return "", "", "", ""
	}
	filePath = matches[0]
	raw, err := os.ReadFile(filePath)
	if err != nil {
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
