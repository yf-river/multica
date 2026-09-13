package storage

import (
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// sanitizeFilename 移除可能在 Content-Disposition 里造成响应头注入的字符。
func sanitizeFilename(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		// 剥掉控制字符、换行、NUL 字节、引号、分号与反斜杠。
		if r < 0x20 || r == 0x7f || r == '"' || r == ';' || r == '\\' || r == '\x00' {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// asciiOnlyFilename 返回 name 的纯 ASCII 变体：每个非 ASCII rune 换成下划线。
// 用于原始文件名需要 RFC 5987 编码时的旧式 filename="..." 参数。
func asciiOnlyFilename(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r > 0x7f {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// needsRFC5987Encoding 当文件名含非 ASCII 字符时返回 true。
func needsRFC5987Encoding(name string) bool {
	for _, r := range name {
		if r > 0x7f {
			return true
		}
	}
	return false
}

// rfc5987Encode 按 RFC 5987 §3.2.1 对 UTF-8 编码做百分号转义。只转义非 attr-char
// （字母数字及 !#$&+-.^_`|~）的字节。
func rfc5987Encode(name string) string {
	var b strings.Builder
	b.Grow(len(name) * 3) // 最坏情况：每个字节编码成 %XX
	for _, r := range name {
		if r <= 0x7f && isAttrChar(byte(r)) {
			b.WriteByte(byte(r))
		} else {
			// 对 UTF-8 表示的每个字节逐一转义。
			buf := make([]byte, 4)
			n := utf8.EncodeRune(buf, r)
			for i := 0; i < n; i++ {
				b.WriteByte('%')
				b.WriteString(strings.ToUpper(hex.EncodeToString(buf[i : i+1])))
			}
		}
	}
	return b.String()
}

// isAttrChar 判断字节是否属于 RFC 5987 的 attr-char 集合。
func isAttrChar(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		strings.ContainsRune("!#$&+-.^_`|~", rune(c))
}

// ContentDisposition 根据 content type 与文件名构造 Content-Disposition 响应头值：
// 可内联类型给 inline，否则给 attachment；文件名非 ASCII 时附上 RFC 5987 的
// filename*。
func ContentDisposition(contentType, filename string) string {
	disposition := "attachment"
	if isInlineContentType(contentType) {
		disposition = "inline"
	}
	if !needsRFC5987Encoding(filename) {
		return disposition + `; filename="` + sanitizeFilename(filename) + `"`
	}
	// 旧客户端给纯 ASCII 回退名（RFC 6266），现代客户端走 RFC 5987 filename*。
	asciiFallback := sanitizeFilename(asciiOnlyFilename(filename))
	return disposition + `; filename="` + asciiFallback + `"; filename*=UTF-8''` + rfc5987Encode(filename)
}

// AttachmentContentDisposition 构造附件下载（attachment）场景的 Content-Disposition，
// 文件名非 ASCII 时附 RFC 5987 编码的 filename*。
func AttachmentContentDisposition(filename string) string {
	if !needsRFC5987Encoding(filename) {
		return `attachment; filename="` + sanitizeFilename(filename) + `"`
	}
	asciiFallback := sanitizeFilename(asciiOnlyFilename(filename))
	return `attachment; filename="` + asciiFallback + `"; filename*=UTF-8''` + rfc5987Encode(filename)
}

// isInlineContentType 判断浏览器应内联展示的媒体类型（图片、视频、音频、PDF）。
// 其余类型都通过 Content-Disposition: attachment 触发下载。
//
// SVG 即使 MIME 是 image/svg+xml 也被排除：SVG 是 XML，可携带 <script>、
// <foreignObject> 或 onload= 等在内联渲染时于文档源执行的属性。强制 attachment
// 可以阻止通过上传 .svg 文件造成的存储型 XSS。
//
// 匹配前先归一化输入（trim、小写、去掉参数），免得 "image/svg+xml; charset=utf-8"
// 或 "IMAGE/SVG+XML" 这类写法绕过 SVG 例外。RFC 2045 §5.1 规定 MIME 类型匹配
// 不区分大小写且允许可选参数；这里是安全边界，所以在本地归一化而不是信任调用方。
func isInlineContentType(ct string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = strings.TrimSpace(mediaType[:i])
	}
	if mediaType == "image/svg+xml" {
		return false
	}
	return strings.HasPrefix(mediaType, "image/") ||
		strings.HasPrefix(mediaType, "video/") ||
		strings.HasPrefix(mediaType, "audio/") ||
		mediaType == "application/pdf"
}
