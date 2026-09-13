package remotemcp

// 本文件职责：定义 Remote MCP 在「插件发现 → 任务会话认领」间传递的核心线类型
// （Tool、Connection）、插件贡献连接的 id 前缀约定，以及工具 schema 内容摘要函数。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Tool 是管理员「钉死」的一个远程 MCP 工具。SchemaDigest 冻结了当初被批准的
// 精确输入 schema——daemon broker 可以用它拒绝那些「在任务运行中途偷换工具
// 契约」的 MCP server。
type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	SchemaDigest string          `json:"schema_digest"`
	Risk         string          `json:"risk,omitempty"`
}

// Connection 是「认领时刻」的、按任务作用域划分的连接元数据。凭证刻意不出现
// 在这个线类型里，由 daemon broker 在用时才即时解析。
type Connection struct {
	InstallationID       string          `json:"installation_id"`
	ContributionID       string          `json:"contribution_id"`
	ContributionKey      string          `json:"contribution_key"`
	ConfigID             string          `json:"config_id"`
	ConfigRevision       int64           `json:"config_revision"`
	Endpoint             string          `json:"endpoint"`
	PublicConfig         json.RawMessage `json:"public_config,omitempty"`
	Transport            string          `json:"transport"`
	ProtocolVersions     []string        `json:"protocol_versions"`
	EndpointAllowedHosts []string        `json:"endpoint_allowed_hosts,omitempty"`
	CredentialHeader     string          `json:"credential_header,omitempty"`
	ApprovedTools        []Tool          `json:"approved_tools"`
	ToolSchemaDigest     string          `json:"tool_schema_digest"`
	FailurePolicy        string          `json:"failure_policy"`
}

// PluginContributionPrefix 标记一条连接来自「已安装的插件」贡献，而不是
// workspace 自己的 Remote MCP 配置。
//
// 两种来源共用同一种 Connection 形状和同一个 broker，但凭证存放的位置不同、
// 由不同的路由服务。daemon 在拨号时手里只有 contribution id，所以必须靠 id
// 本身说明它是哪种——如果试图从字符串形状去推断，那一条恰好带冒号的云端
// 签发 id 就会被错送去解析插件路由。
const PluginContributionPrefix = "plugin:"

// DigestBytes 计算内容摘要，用于钉死被批准的工具 schema。
func DigestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}
