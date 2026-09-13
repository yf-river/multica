package skill

import (
	"path/filepath"
	"strings"
)

// IsLikelyBinaryFilePath 依据扩展名判断某文件是否是非文本载荷。采用保守黑名单——
// 不在名单上的扩展名一律假定为文本并放行。
//
// 两条 skill 摄入路径都需要它，理由相同但来自两个方向：归档/URL 导入不能把这些
// 字节写进 PG TEXT 列（SQLSTATE 22021）；运行时本地同步不能经由 SkillFileData.Content
// 携带它们，因为 encoding/json 会在 daemon/server 之间把每个非法 UTF-8 字节改写成
// U+FFFD。无论哪条路，出来的字节都不是进去的字节，所以两条路径都跳过该文件，
// 而不是存一份日后会被损坏地交回去的载荷。
func IsLikelyBinaryFilePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case
		// images
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".ico", ".heic",
		// fonts
		".ttf", ".otf", ".woff", ".woff2", ".eot",
		// archives
		".zip", ".gz", ".tar", ".bz2", ".7z", ".rar",
		// documents (binary office)
		".pdf", ".docx", ".xlsx", ".pptx", ".doc", ".xls", ".ppt",
		// media
		".mp3", ".mp4", ".wav", ".avi", ".mov", ".webm", ".m4a", ".flac",
		// compiled / executable
		".exe", ".dll", ".so", ".dylib", ".class", ".jar", ".wasm",
		// db / cache
		".db", ".sqlite", ".sqlite3", ".pyc":
		return true
	}
	return false
}
