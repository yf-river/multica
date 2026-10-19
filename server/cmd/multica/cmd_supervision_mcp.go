package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/pkg/dbid"
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
			Roots      []investigation.Root `json:"roots"`
			TaskID     string               `json:"task_id"`
			LocalAudit string               `json:"local_audit,omitempty"`
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

func supervisionMCPTools() []map[string]any {
	var tools []map[string]any
	for _, entry := range []struct{ name, description string }{
		{"supervision_read", "分页读取授权来源内的文本；offset为字符偏移，使用sha256核对补读期间来源是否变化。"},
		{"supervision_search", "列出或按字面值搜索授权来源；结果有上限，注意truncated。query为空时列文件。"},
		{"supervision_git", "查询授权仓库；query为status、diff、history或show。diff/history须指定path；show还须完整40位revision。"},
		{"supervision_material", "列出或补读当前业务任务历史。path=index列来源索引，offset为条目偏移；path=kind/UUID读取原文，offset为字符偏移。kind为issue、comment、task、message或trace。root填materials。"},
	} {
		tools = append(tools, map[string]any{"name": entry.name, "description": entry.description, "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"root": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}, "query": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}, "revision": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "required": []string{"root", "reason"}}})
	}
	return tools
}
