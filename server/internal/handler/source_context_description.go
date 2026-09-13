// 本文件是 source_context.go 的帮手：把「议题描述 / 评论正文里的 Markdown」投影成
// 可比较的形式，供 sourceContextIssueChangeDetails 判断「捕获时的内容 vs 现在的内容」
// 是否真的变了。
//
// 为什么需要投影：编辑器的附件是「先上传再在正文里嵌入引用」的，克隆到 source
// context 后附件 URL 换了（/uploads/... 或 /api/attachments/... 指向克隆对象），直接
// 比原文会把「同一份内容、不同附件地址」误判成变化。投影做的事是把正文里指向已知
// 附件的引用统一替换成稳定的附件身份标识，其余文本（含外部链接）原样保留再比较。
//
// 描述（description）与评论（comment content）各有一个投影函数，差异在「描述引用要
// 抽出附件清单供删除/改名判定」，评论只做内容比较。
package handler

import (
	"strings"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

// sourceContextDescriptionAttachmentReference 是描述正文里一处「附件引用」的抽取结果：
// 指向哪个附件（AttachmentID）+ 当时的文件名。
type sourceContextDescriptionAttachmentReference struct {
	AttachmentID string
	Filename     string
}

// sourceContextMarkdownReference 是一次 Markdown 链接语法扫描的中间结果：记录它在原文
// 中的起止位置、链接文字（Filename）与目标 URL 位置。
type sourceContextMarkdownReference struct {
	Start    int
	End      int
	URLStart int
	URLEnd   int
	Filename string
	URL      string
}

// sourceContextDescriptionProjection 把编辑器的附件节点与其余 Markdown 分开处理。
// 快照的附件清单仍是历史下载的权威来源，但它本身不是用户可见的 issue 内容：只有
// 嵌在描述里的引用参与这次比较。
func sourceContextDescriptionProjection(
	description *string,
	capturedAttachments, currentAttachments []service.SourceContextAttachment,
) (string, []sourceContextDescriptionAttachmentReference) {
	if description == nil || *description == "" {
		return "", []sourceContextDescriptionAttachmentReference{}
	}
	content := strings.ReplaceAll(*description, "\r\n", "\n")
	knownFilenames := sourceContextKnownAttachmentFilenames(capturedAttachments, currentAttachments)
	aliases := sourceContextAttachmentAliases(capturedAttachments, currentAttachments)
	references := make([]sourceContextDescriptionAttachmentReference, 0)
	var body strings.Builder
	last := 0
	for offset := 0; offset < len(content); {
		candidate, ok := scanSourceContextMarkdownReference(content, offset)
		if !ok {
			offset++
			continue
		}
		attachmentID := sourceContextDescriptionAttachmentID(candidate.URL, knownFilenames)
		if attachmentID == "" {
			offset = candidate.End
			continue
		}
		if canonicalID, ok := aliases[strings.ToLower(attachmentID)]; ok {
			attachmentID = canonicalID
		}
		removeStart, removeEnd := sourceContextStandaloneReferenceRange(content, candidate.Start, candidate.End)
		if removeStart < last {
			removeStart = candidate.Start
			removeEnd = candidate.End
		}
		body.WriteString(content[last:removeStart])
		last = removeEnd
		filename := unescapeSourceContextMarkdownLabel(candidate.Filename)
		if filename == "" {
			filename = knownFilenames[strings.ToLower(attachmentID)]
		}
		references = append(references, sourceContextDescriptionAttachmentReference{
			AttachmentID: attachmentID,
			Filename:     filename,
		})
		offset = candidate.End
	}
	body.WriteString(content[last:])
	return strings.TrimSpace(body.String()), references
}

// sourceContextKnownAttachmentFilenames 从多组附件清单里建立「id（大小写归一）→
// 文件名」的映射，用于把引用 URL 认出是哪个附件、以及补出丢失的文件名。
func sourceContextKnownAttachmentFilenames(groups ...[]service.SourceContextAttachment) map[string]string {
	result := make(map[string]string)
	for _, items := range groups {
		for _, item := range items {
			if item.ID != "" {
				result[strings.ToLower(item.ID)] = item.Filename
			}
			if item.SourceAttachmentID != "" {
				result[strings.ToLower(item.SourceAttachmentID)] = item.Filename
			}
		}
	}
	return result
}

// sourceContextAttachmentAliases 建立「任意 id（本地 id 或跨来源稳定 id）→ 规范 id」
// 的别名表，让同一附件在不同副本里的引用能归一到同一个身份再比较。
func sourceContextAttachmentAliases(groups ...[]service.SourceContextAttachment) map[string]string {
	result := make(map[string]string)
	for _, items := range groups {
		for _, item := range items {
			canonicalID := item.SourceAttachmentID
			if canonicalID == "" {
				canonicalID = item.ID
			}
			canonicalID = strings.ToLower(canonicalID)
			if canonicalID == "" {
				continue
			}
			result[canonicalID] = canonicalID
			if item.ID != "" {
				result[strings.ToLower(item.ID)] = canonicalID
			}
		}
	}
	return result
}

// sourceContextMarkdownContentProjection 保留作者写的 Markdown，但把能认出的附件
// 目标替换成它们本来的附件身份。于是持久化的克隆与线上源可以比较相等，同时不弱化
// 对普通文本或外部链接的比较。
func sourceContextMarkdownContentProjection(
	content string,
	capturedAttachments, currentAttachments []service.SourceContextAttachment,
) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	knownFilenames := sourceContextKnownAttachmentFilenames(capturedAttachments, currentAttachments)
	aliases := sourceContextAttachmentAliases(capturedAttachments, currentAttachments)
	var projected strings.Builder
	last := 0
	for offset := 0; offset < len(content); {
		candidate, ok := scanSourceContextMarkdownReference(content, offset)
		if !ok {
			offset++
			continue
		}
		attachmentID := sourceContextDescriptionAttachmentID(candidate.URL, knownFilenames)
		canonicalID, recognized := aliases[strings.ToLower(attachmentID)]
		if attachmentID == "" || !recognized {
			offset = candidate.End
			continue
		}
		projected.WriteString(content[last:candidate.URLStart])
		projected.WriteString("source-context-attachment:")
		projected.WriteString(canonicalID)
		last = candidate.URLEnd
		offset = candidate.End
	}
	projected.WriteString(content[last:])
	return projected.String()
}

// sourceContextDescriptionAttachmentID 从引用 URL 里反解附件 id：先按下载 URL 的
// 已知形态直接抽，抽不到就退化到「稳定 URL 里包含已知 id 子串」的启发式。
func sourceContextDescriptionAttachmentID(rawURL string, knownFilenames map[string]string) string {
	if id, ok := util.AttachmentIDFromDownloadURL(rawURL); ok {
		return id
	}
	stableURL := strings.ToLower(strings.SplitN(strings.SplitN(strings.TrimSpace(rawURL), "#", 2)[0], "?", 2)[0])
	for id := range knownFilenames {
		if strings.Contains(stableURL, id) {
			return id
		}
	}
	return ""
}

// scanSourceContextMarkdownReference 从 content[start:] 起尝试识别一段附件 Markdown
// 链接（!file[...](...) / ![...](...) / [...](...)），识别成功返回整段的起止与内部
// 位置。反斜杠转义开头的引用会被跳过，避免把 `\[...]` 这类字面文本当引用。
func scanSourceContextMarkdownReference(content string, start int) (sourceContextMarkdownReference, bool) {
	if start > 0 && content[start-1] == '\\' {
		return sourceContextMarkdownReference{}, false
	}
	prefixLength := 0
	switch {
	case strings.HasPrefix(content[start:], "!file["):
		prefixLength = len("!file[")
	case strings.HasPrefix(content[start:], "!["):
		prefixLength = len("![")
	case strings.HasPrefix(content[start:], "["):
		prefixLength = len("[")
	default:
		return sourceContextMarkdownReference{}, false
	}
	labelStart := start + prefixLength
	labelEnd := sourceContextUnescapedDelimiter(content, labelStart, ']')
	if labelEnd < 0 || labelEnd+1 >= len(content) || content[labelEnd+1] != '(' {
		return sourceContextMarkdownReference{}, false
	}
	urlStart := labelEnd + 2
	urlEnd := sourceContextUnescapedDelimiter(content, urlStart, ')')
	if urlEnd < 0 {
		return sourceContextMarkdownReference{}, false
	}
	return sourceContextMarkdownReference{
		Start: start, End: urlEnd + 1, URLStart: urlStart, URLEnd: urlEnd,
		Filename: content[labelStart:labelEnd], URL: content[urlStart:urlEnd],
	}, true
}

// sourceContextUnescapedDelimiter 从 start 起找第一个未被反斜杠转义的 delimiter，
// 找不到返回 -1。
func sourceContextUnescapedDelimiter(content string, start int, delimiter byte) int {
	escaped := false
	for index := start; index < len(content); index++ {
		if escaped {
			escaped = false
			continue
		}
		if content[index] == '\\' {
			escaped = true
			continue
		}
		if content[index] == delimiter {
			return index
		}
	}
	return -1
}

// sourceContextStandaloneReferenceRange 确定一段独立成行的附件引用应被剔除的范围：
// 若该引用独占一行（行内无其它内容），连同行前/行后相邻的一个空行分隔一起删，让
// 普通 Markdown 的投影与编辑器在删除附件块后的序列化结果一致；否则只删引用本身。
func sourceContextStandaloneReferenceRange(content string, start, end int) (int, int) {
	lineStart := strings.LastIndex(content[:start], "\n") + 1
	lineEndRelative := strings.Index(content[end:], "\n")
	lineEnd := len(content)
	if lineEndRelative >= 0 {
		lineEnd = end + lineEndRelative + 1
	}
	lineContentEnd := lineEnd
	if lineContentEnd > 0 && content[lineContentEnd-1] == '\n' {
		lineContentEnd--
	}
	if strings.TrimSpace(content[lineStart:start]) != "" || strings.TrimSpace(content[end:lineContentEnd]) != "" {
		return start, end
	}

	// 独立附件块还带一个相邻的空白行分隔符一起删。这保证普通 Markdown 投影与编辑器
	// 删掉该块后的序列化输出一致。
	if lineStart > 0 {
		previousLineEnd := lineStart - 1
		previousLineStart := strings.LastIndex(content[:previousLineEnd], "\n") + 1
		if strings.TrimSpace(content[previousLineStart:previousLineEnd]) == "" {
			lineStart = previousLineStart
			return lineStart, lineEnd
		}
	}
	if lineEnd < len(content) {
		nextLineEndRelative := strings.Index(content[lineEnd:], "\n")
		nextLineEnd := len(content)
		if nextLineEndRelative >= 0 {
			nextLineEnd = lineEnd + nextLineEndRelative + 1
		}
		nextContentEnd := nextLineEnd
		if nextContentEnd > lineEnd && content[nextContentEnd-1] == '\n' {
			nextContentEnd--
		}
		if strings.TrimSpace(content[lineEnd:nextContentEnd]) == "" {
			lineEnd = nextLineEnd
		}
	}
	return lineStart, lineEnd
}

// unescapeSourceContextMarkdownLabel 把 Markdown 标签里转义过的字符还原成字面值。
func unescapeSourceContextMarkdownLabel(value string) string {
	replacer := strings.NewReplacer(`\[`, `[`, `\]`, `]`, `\(`, `(`, `\)`, `)`, `\\`, `\`)
	return replacer.Replace(value)
}

// sourceContextDescriptionAttachmentChanges 对比「捕获时」与「现在」的描述附件引用
// 清单，产出 removed / replaced / added 三类变化。同名附件允许出现多次，按出现顺序
// 匹配。
func sourceContextDescriptionAttachmentChanges(
	captured, current []sourceContextDescriptionAttachmentReference,
) []sourceContextDescriptionAttachmentChange {
	currentByID := make(map[string][]sourceContextDescriptionAttachmentReference)
	for _, reference := range current {
		currentByID[reference.AttachmentID] = append(currentByID[reference.AttachmentID], reference)
	}
	matchedCurrent := make(map[string]int)
	changes := make([]sourceContextDescriptionAttachmentChange, 0)
	for _, reference := range captured {
		matchIndex := matchedCurrent[reference.AttachmentID]
		matches := currentByID[reference.AttachmentID]
		if matchIndex >= len(matches) {
			changes = append(changes, sourceContextDescriptionAttachmentChange{
				Kind: "removed", AttachmentID: reference.AttachmentID, Filename: reference.Filename,
			})
			continue
		}
		currentReference := matches[matchIndex]
		matchedCurrent[reference.AttachmentID] = matchIndex + 1
		if reference.Filename != currentReference.Filename {
			changes = append(changes, sourceContextDescriptionAttachmentChange{
				Kind: "replaced", AttachmentID: reference.AttachmentID, Filename: currentReference.Filename,
				PreviousFilename: reference.Filename,
			})
		}
	}
	seenCurrent := make(map[string]int)
	for _, reference := range current {
		seenCurrent[reference.AttachmentID]++
		if seenCurrent[reference.AttachmentID] <= matchedCurrent[reference.AttachmentID] {
			continue
		}
		changes = append(changes, sourceContextDescriptionAttachmentChange{
			Kind: "added", AttachmentID: reference.AttachmentID, Filename: reference.Filename,
		})
	}
	return changes
}
