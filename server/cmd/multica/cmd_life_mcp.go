// 文件职责：实现 `multica life mcp`（隐藏命令）——把受治理的 life 认知工具
// （life_evidence_resolve、life_job_complete）以 MCP 服务形态暴露。走 stdin/stdout
// 的逐行 JSON-RPC 2.0：init() 把命令挂在 lifeCmd 下，runLifeMCPServer 读取每行
// 请求并回一行响应。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// lifeMCPProtocolVersion 是这个 MCP server 声明的协议版本（initialize 响应用）。
const lifeMCPProtocolVersion = "2025-03-26"

// lifeMCPCmd 是 `life mcp` 隐藏命令，运行 MCP server（stdio）。
var lifeMCPCmd = &cobra.Command{
	Use:    "mcp",
	Short:  "Serve governed life cognition tools over MCP",
	Hidden: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runLifeMCPServer(cmd, os.Stdin, os.Stdout)
	},
}

// lifeMCPRequest 是收到的 JSON-RPC 请求（id 保为 RawMessage 以便原样回显）。
type lifeMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// lifeMCPResponse 是回写的 JSON-RPC 响应（result/error 二选一）。
type lifeMCPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *lifeMCPError   `json:"error,omitempty"`
}

// lifeMCPError 是 JSON-RPC 错误对象。
type lifeMCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func init() {
	lifeCmd.AddCommand(lifeMCPCmd)
}

// runLifeMCPServer 逐行读 stdin 的 JSON-RPC 请求并逐行写 stdout 响应（MCP stdio
// 传输）。行太长时扩大 scanner 缓冲上限至 4MB；解析失败回 -32700 parse error。
func runLifeMCPServer(cmd *cobra.Command, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request lifeMCPRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			if err := encoder.Encode(lifeMCPResponse{JSONRPC: "2.0", Error: &lifeMCPError{Code: -32700, Message: "parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		response := handleLifeMCPRequest(cmd, request)
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// handleLifeMCPRequest 分发单个 JSON-RPC 请求：支持 initialize/ping/tools/list/
// tools/call，其余回 -32601 method not found。tools/call 出错时不回错误对象而是
// 把错误文本放进 isError=true 的工具结果——MCP 惯例是让模型读到工具报错而非
// 传输层失败。
func handleLifeMCPRequest(cmd *cobra.Command, request lifeMCPRequest) lifeMCPResponse {
	response := lifeMCPResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": lifeMCPProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "multica-life", "version": version},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": lifeMCPTools()}
	case "tools/call":
		result, err := callLifeMCPTool(cmd, request.Params)
		if err != nil {
			response.Result = lifeMCPToolResult(err.Error(), true)
		} else {
			response.Result = lifeMCPToolResult(result, false)
		}
	default:
		response.Error = &lifeMCPError{Code: -32601, Message: "method not found"}
	}
	return response
}

// lifeMCPTools 返回声明的工具清单（tools/list 用）。
func lifeMCPTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "life_evidence_resolve",
			"description": "Resolve exact governed material, chronicle, memory, or observer knowledge for the current life cognition task.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"references": map[string]any{
						"type": "array", "minItems": 1,
						"items": map[string]any{
							"type": "object", "additionalProperties": false,
							"properties": map[string]any{
								"source_type": map[string]any{"type": "string", "enum": []string{"material", "chronicle", "memory", "observer_knowledge"}},
								"source_id":   map[string]any{"type": "string", "format": "uuid"},
							},
							"required": []string{"source_type", "source_id"},
						},
					},
				},
				"required":             []string{"references"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "life_job_complete",
			"description": "Submit the complete structured result for the current durable life cognition job.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"job_id": map[string]any{"type": "string", "format": "uuid"},
					"output": map[string]any{"type": "object"},
				},
				"required":             []string{"job_id", "output"},
				"additionalProperties": false,
			},
		},
	}
}

// callLifeMCPTool 解析 tools/call 参数并按工具名分发到 cmd_life.go 的底层实现，
// 结果序列化成字符串返回。
func callLifeMCPTool(cmd *cobra.Command, raw json.RawMessage) (string, error) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return "", fmt.Errorf("invalid tool call: %w", err)
	}
	var result map[string]any
	var err error
	switch params.Name {
	case "life_evidence_resolve":
		var arguments struct {
			References []map[string]string `json:"references"`
		}
		if err := json.Unmarshal(params.Arguments, &arguments); err != nil || len(arguments.References) == 0 {
			return "", fmt.Errorf("references are required")
		}
		result, err = resolveLifeEvidence(cmd, arguments.References)
	case "life_job_complete":
		var arguments struct {
			JobID  string         `json:"job_id"`
			Output map[string]any `json:"output"`
		}
		if err := json.Unmarshal(params.Arguments, &arguments); err != nil || strings.TrimSpace(arguments.JobID) == "" || arguments.Output == nil {
			return "", fmt.Errorf("job_id and output are required")
		}
		result, err = completeLifeJob(cmd, strings.TrimSpace(arguments.JobID), arguments.Output)
	default:
		return "", fmt.Errorf("unknown tool %q", params.Name)
	}
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// lifeMCPToolResult 把工具结果包成 MCP 的文本内容块；isError 标记失败供模型感知。
func lifeMCPToolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": isError,
	}
}
