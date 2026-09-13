// Package runtimeapps 定义"运行时可连接应用"的任务级元数据：不含机密的 app
// 信息会随任务给到 daemon / agent，用于告知本次运行实际挂载了哪些外部应用能力。
package runtimeapps

import (
	"encoding/json"
	"strings"
)

// ConnectedApp 是不含机密的、任务作用域元数据，告诉 daemon 本次运行实际挂载了
// 哪些外部应用能力。
type ConnectedApp struct {
	Provider    string `json:"provider"`
	ServerName  string `json:"server_name"`
	ToolkitSlug string `json:"toolkit_slug"`
	ToolkitName string `json:"toolkit_name,omitempty"`
}

// MCPOverlayResult 同时携带两份产物：含机密的 MCP 配置 overlay，以及用来向
// agent 简报的不含机密的已连接应用清单。
type MCPOverlayResult struct {
	MCPOverlay    json.RawMessage
	ConnectedApps []ConnectedApp
}

// DisplayNameForToolkitSlug 返回紧凑、人类可读的展示名，且不因此在入队路径上
// 发起目录查询。品牌名大小写这里刻意"尽力而为"；slug 才是每份简报里的功能标识符。
func DisplayNameForToolkitSlug(slug string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return ""
	}
	switch slug {
	case "github":
		return "GitHub"
	case "gmail":
		return "Gmail"
	case "linkedin":
		return "LinkedIn"
	}
	// 未知 slug 拆下划线/连字符为单词，逐个转首字母大写后以空格连接。
	words := strings.FieldsFunc(slug, func(r rune) bool {
		return r == '_' || r == '-'
	})
	if len(words) == 0 {
		return slug
	}
	for i, word := range words {
		words[i] = titleASCII(word)
	}
	return strings.Join(words, " ")
}

// titleASCII 把 ASCII 字符串转成"首字母大写、其余小写"形式。
func titleASCII(s string) string {
	if s == "" {
		return ""
	}
	b := []byte(strings.ToLower(s))
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}
