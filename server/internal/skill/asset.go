package skill

import (
	"net/http"
	"path"
	"strings"
	"unicode/utf8"
)

const MaxFileBytes = 16 << 20
const MaxBundleBytes = 64 << 20
const MaxFiles = 512
const MaxArchiveBytes = 80 << 20

func SafePath(p string) bool {
	return p != "" && p != "." && p != ".." && path.Clean(p) == p && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00:")
}

func FileKind(p string, data []byte) string {
	if IsLikelyBinaryFilePath(p) || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "binary"
	}
	return "text"
}

func ContentType(data []byte) string {
	return http.DetectContentType(data)
}

func FileMode(mode int32) int32 {
	if mode&0111 != 0 {
		return 0755
	}
	return 0644
}
