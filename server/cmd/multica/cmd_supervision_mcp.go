package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/supervision"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/redact"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/investigation"
	"github.com/spf13/cobra"
)

var supervisionMCPCmd = &cobra.Command{
	Use: "supervision-mcp <manifest>", Hidden: true, Args: exactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		var manifest struct {
			Roots      []investigation.Root   `json:"roots"`
			TaskID     string                 `json:"task_id"`
			LocalAudit string                 `json:"local_audit,omitempty"`
			Materials  []supervision.Material `json:"materials,omitempty"`
		}
		if err = json.Unmarshal(data, &manifest); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
		defer cancel()
		session := investigation.Session{Roots: manifest.Roots}
		if manifest.TaskID != "" {
			session.ReadMaterial = func(ctx context.Context, req investigation.Request) (investigation.Result, error) {
				var out investigation.Result
				parts := strings.Split(req.Path, "/")
				if req.Path == "index" {
					parts = []string{"index", ""}
				}
				if len(parts) != 2 {
					return out, fmt.Errorf("path应为材料kind/UUID")
				}
				client, err := newAPIClient(cmd)
				if err != nil {
					return out, err
				}
				query := url.Values{"kind": {parts[0]}, "id": {parts[1]}, "offset": {fmt.Sprint(req.Offset)}, "reason": {req.Reason}}
				err = client.GetJSON(ctx, "/api/supervision/investigation/material?"+query.Encode(), &out)
				return out, err
			}
		} else if manifest.LocalAudit != "" {
			session.ReadMaterial = func(ctx context.Context, req investigation.Request) (investigation.Result, error) {
				out, err := offlineSupervisionMaterial(manifest.Materials, req)
				if err != nil {
					return out, err
				}
				if err = session.Save(ctx, req, &out); err != nil {
					return investigation.Result{}, err
				}
				return out, nil
			}
		}
		session.Save = func(ctx context.Context, req investigation.Request, result *investigation.Result) error {
			if manifest.LocalAudit != "" {
				result.ID = dbid.NewV7().String()
				// 离线评测审计路径仅来自启动器清单，模型不能传入写入位置。
				f, e := os.OpenFile(manifest.LocalAudit, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if e != nil {
					return e
				}
				defer f.Close()
				return json.NewEncoder(f).Encode(map[string]any{"request": req, "result": result})
			}
			if manifest.TaskID == "" {
				return fmt.Errorf("缺少审查运行身份")
			}
			client, e := newAPIClient(cmd)
			if e != nil {
				return e
			}
			var reply struct {
				ID string `json:"id"`
			}
			if e = client.PostJSON(ctx, "/api/supervision/investigation", map[string]any{"request": req, "result": result}, &reply); e != nil {
				return e
			}
			result.ID = reply.ID
			return nil
		}
		return runScopedMCPServer(os.Stdin, os.Stdout, func(request lifeMCPRequest) lifeMCPResponse {
			response := lifeMCPResponse{JSONRPC: "2.0", ID: request.ID}
			switch request.Method {
			case "initialize":
				response.Result = map[string]any{"protocolVersion": lifeMCPProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "multica-supervision", "version": version}}
			case "ping":
				response.Result = map[string]any{}
			case "tools/list":
				response.Result = map[string]any{"tools": supervisionMCPTools()}
			case "tools/call":
				var call struct {
					Name      string                `json:"name"`
					Arguments investigation.Request `json:"arguments"`
				}
				if err := json.Unmarshal(request.Params, &call); err != nil {
					response.Result = lifeMCPToolResult("调查参数无效", true)
					break
				}
				result, err := session.Call(ctx, call.Name, call.Arguments)
				if err != nil {
					result.Error = err.Error()
				}
				response.Result = lifeMCPToolResult(result.JSON(), err != nil)
			default:
				response.Error = &lifeMCPError{Code: -32601, Message: "method not found"}
			}
			return response
		})
	},
}

func init() { rootCmd.AddCommand(supervisionMCPCmd) }

// 离线评测回放冻结材料；与联调一样，模型不能用任意文件路径取得未授权历史。
func offlineSupervisionMaterial(materials []supervision.Material, req investigation.Request) (investigation.Result, error) {
	out := investigation.Result{Kind: "investigation", Root: "materials", Tool: "supervision_material", Path: req.Path, Offset: req.Offset, ObservedAt: time.Now().UTC()}
	if req.Offset < 0 {
		return out, fmt.Errorf("读取位置无效")
	}
	if req.Path == "index" {
		var refs []supervision.Evidence
		for _, m := range materials {
			if m.Kind != "investigation" {
				refs = append(refs, m.Evidence)
			}
		}
		start := min(req.Offset, len(refs))
		end := min(start+20, len(refs))
		raw, _ := json.Marshal(refs[start:end])
		out.Text = string(raw)
		out.Length = len(refs)
		out.End = end
		out.Truncated = end < len(refs)
		return out, nil
	}
	for _, m := range materials {
		if req.Path != m.Kind+"/"+m.ID {
			continue
		}
		if m.Offset != 0 || m.End < m.Length {
			return out, fmt.Errorf("冻结快照缺少完整原文，不能声称已补读")
		}
		body := []rune(m.Text)
		start := min(req.Offset, len(body))
		end := min(start+investigation.PageSize, len(body))
		out.Text = redact.Text(string(body[start:end]))
		out.Length = len(body)
		out.Offset = start
		out.End = end
		out.Truncated = end < len(body)
		out.RunID = m.RunID
		sum := sha256.Sum256([]byte(m.Text))
		out.SHA256 = hex.EncodeToString(sum[:])
		return out, nil
	}
	return out, fmt.Errorf("本任务冻结材料中没有此来源")
}

func supervisionMCPTools() []map[string]any {
	var tools []map[string]any
	for _, entry := range []struct{ name, description string }{
		{"supervision_read", "分页读取授权来源内的文本；offset为字符偏移，使用sha256核对补读期间来源是否变化。"},
		{"supervision_search", "列出或按字面值搜索授权来源；结果有上限，注意truncated。query为空时列文件。"},
		{"supervision_git", "查询授权仓库；query为status、diff、history或show。diff/history须指定path；show还须完整40位revision。"},
		{"supervision_material", "列出或补读当前业务任务历史。path=index列来源索引，offset为条目偏移；path=kind/UUID读取原文，offset为字符偏移。kind为issue、comment、task、message或trace。root填materials。"},
	} {
		tools = append(tools, map[string]any{"name": entry.name, "description": entry.description, "annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}, "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"root": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}, "query": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}, "revision": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "required": []string{"root", "reason"}}})
	}
	return tools
}
