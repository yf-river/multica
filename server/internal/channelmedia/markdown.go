// Package channelmedia 负责在 issue / Chat 的 Markdown 中持久化"频道媒体附件"的
// 下载链接与来源标记。renderer 会忽略来源标记注释，而 issue 更新逻辑靠它区分
// "频道摄入晚到的媒体写入"与"用户有意的编辑"。
package channelmedia

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// markerPrefix 是来源标记注释的前缀，后面跟附件 id 再以 " -->" 收尾。
const markerPrefix = "<!-- multica:channel-media:"

// markerPattern 匹配完整的来源标记注释，并捕获其中的 36 位 UUID。
var markerPattern = regexp.MustCompile(`<!-- multica:channel-media:([0-9a-fA-F-]{36}) -->`)

// DownloadPath 返回持久化、带鉴权的附件下载 URL（写入 issue 与 Chat 的 Markdown）。
func DownloadPath(id string) string {
	return "/api/attachments/" + id + "/download"
}

// Marker 生成某个附件的来源标记：说明该 Markdown 附件是由频道摄入异步物化的。
func Marker(id string) string {
	return markerPrefix + id + " -->"
}

// Block 渲染一个频道附件（图片/文件链接），并附上其持久来源标记。
func Block(id, filename string, image bool) string {
	downloadPath := DownloadPath(id)
	var markdown string
	if image {
		markdown = "![](" + downloadPath + ")"
	} else {
		// 文件名作为链接文字时转义 Markdown 特殊字符，并去掉换行以免破坏段落。
		label := strings.NewReplacer(
			"\\", "\\\\",
			"[", "\\[",
			"]", "\\]",
			"\r", " ",
			"\n", " ",
		).Replace(filename)
		if label == "" {
			label = "attachment"
		}
		markdown = "[" + label + "](" + downloadPath + ")"
	}
	return markdown + "\n\n" + Marker(id)
}

// MarkedIDs 按文档顺序返回有效的频道媒体附件 id，并去重。形状像标记但 id 非法的
// 注释会被忽略。
func MarkedIDs(markdown string) []string {
	matches := markerPattern.FindAllStringSubmatch(markdown, -1)
	ids := make([]string, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		parsed, err := uuid.Parse(match[1])
		if err != nil {
			continue
		}
		id := parsed.String()
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// HasMarker 报告 markdown 是否已带有该 id 的来源标记。刻意不把裸的附件 URL
// 当作来源证据。
func HasMarker(markdown, id string) bool {
	return strings.Contains(markdown, Marker(id))
}

// Append 把一块 Markdown 追加到现有内容后，使用与 issue 媒体物化一致的换行约定。
func Append(markdown, block string) string {
	if markdown == "" {
		return block
	}
	return markdown + "\n\n" + block
}
