// Package util 提供跨包共享的小工具：URL/JSON/文本清洗、UUID 转换、pgx 辅助等
// 通用基础设施，不依赖其它 internal 包。
package util

import (
	"net/url"
	"strings"
)

const (
	attachmentDownloadPathPrefix = "/api/attachments/"
	attachmentDownloadPathSuffix = "/download"
)

// AttachmentDownloadPath 返回可持久化进 Markdown 正文的稳定 API 路径。构造与解析
// 该路由都放在这一处，避免采集与渲染两端重复维护其语法。
func AttachmentDownloadPath(attachmentID string) string {
	return attachmentDownloadPathPrefix + attachmentID + attachmentDownloadPathSuffix
}

// AttachmentIDFromDownloadURL 从稳定下载路由里提取附件 UUID。接受站内相对路径与
// 绝对 http(s) URL，也容忍查询串与锚点；其它路径/协议一律拒绝。
func AttachmentIDFromDownloadURL(rawURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}
	// 有 scheme 则必须是 http/https；"有 host 但无 scheme"说明形如 "example.com/x"，
	// 那是另一个站点的裸域名，拒绝。
	if parsed.Scheme != "" && !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", false
	}
	if parsed.Host != "" && parsed.Scheme == "" {
		return "", false
	}
	path := parsed.Path
	if !strings.HasPrefix(path, attachmentDownloadPathPrefix) || !strings.HasSuffix(path, attachmentDownloadPathSuffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, attachmentDownloadPathPrefix), attachmentDownloadPathSuffix)
	// 中间再夹路径段说明格式不对。
	if strings.Contains(id, "/") {
		return "", false
	}
	parsedID, err := ParseUUID(id)
	if err != nil {
		return "", false
	}
	return UUIDToString(parsedID), true
}
