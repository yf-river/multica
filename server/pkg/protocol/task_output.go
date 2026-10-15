package protocol

import (
	"strings"
	"unicode/utf8"
)

const TaskOutputPageSize = 8192

func TaskOutputPreview(output string) (string, int, bool) {
	count := utf8.RuneCountInString(output)
	if count <= TaskOutputPageSize {
		return output, count, false
	}
	return string([]rune(output)[:TaskOutputPageSize]), count, true
}

type TaskOutputPage struct {
	Output     string `json:"output"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	TotalChars int    `json:"total_chars"`
	HasMore    bool   `json:"has_more"`
	Found      bool   `json:"found"`
}

// 偏移按 Unicode 字符计算，分页不会拆开中文。检索范围是完整记录。
func PageTaskOutput(output string, offset int, query string) TaskOutputPage {
	runes := []rune(output)
	if offset < 0 {
		offset = 0
	}
	if offset > len(runes) {
		offset = len(runes)
	}
	found := true
	if query != "" {
		suffix := string(runes[offset:])
		at := strings.Index(suffix, query)
		found = at >= 0
		if found {
			offset += utf8.RuneCountInString(suffix[:at])
		}
	}
	end := min(offset+TaskOutputPageSize, len(runes))
	return TaskOutputPage{Output: string(runes[offset:end]), Offset: offset, NextOffset: end, TotalChars: len(runes), HasMore: end < len(runes), Found: found}
}
