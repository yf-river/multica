package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 常量前缀：匹配结果标注"该路径是 daemon 受管的子路径"时使用。
const managedArtifactPatternPrefix = "managed:"

// artifactMatcher 把运维配置的 basename 匹配与精确的 daemon 受管路径组合在一起。
// 精确路径优先，因此像 .sandbox-bin 这样宽泛的 basename 不会把受管目录重复计数。
type artifactMatcher struct {
	basenames      map[string]struct{}
	exactPaths     map[string]string
	exactLeafNames map[string]struct{}
}

// newArtifactMatcher 用 basename 模式集与受管子路径集构造匹配器；无法转成安全
// 相对路径的子路径会被跳过。
func newArtifactMatcher(patterns, managedSubpaths []string) artifactMatcher {
	m := artifactMatcher{
		basenames:      buildPatternSet(patterns),
		exactPaths:     make(map[string]string, len(managedSubpaths)),
		exactLeafNames: make(map[string]struct{}, len(managedSubpaths)),
	}
	for _, subpath := range managedSubpaths {
		cleaned, ok := safeRelativePath(subpath)
		if !ok {
			continue
		}
		display := filepath.ToSlash(cleaned)
		m.exactPaths[cleaned] = managedArtifactPatternPrefix + display
		m.exactLeafNames[filepath.Base(cleaned)] = struct{}{}
	}
	return m
}

// matchDirectory 判定 (absRoot 下的 path, entry) 是否命中某个 artifact 模式，返回
// 匹配标签；精确受管路径命中返回 "managed:<相对路径>"，否则 basename 命中返回
// entry 名。
func (m artifactMatcher) matchDirectory(absRoot, path string, entry os.DirEntry) (string, bool) {
	_, exactCandidate := m.exactLeafNames[entry.Name()]
	_, basenameMatch := m.basenames[entry.Name()]
	if !exactCandidate && !basenameMatch {
		return "", false
	}

	// 只有叶子可能命中的目录才需要 Rel 与包含关系校验。大多数 workdir 条目根本
	// 不进这条路径。
	rel, err := filepath.Rel(absRoot, path)
	if err != nil {
		return "", false
	}
	rel, ok := safeRelativePath(rel)
	if !ok {
		return "", false
	}
	if label, ok := m.exactPaths[rel]; ok {
		return label, true
	}
	if basenameMatch {
		return entry.Name(), true
	}
	return "", false
}

// managedSubpaths 返回全部精确受管相对路径（按字典序稳定输出）。
func (m artifactMatcher) managedSubpaths() []string {
	out := make([]string, 0, len(m.exactPaths))
	for rel := range m.exactPaths {
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

// safeRelativePath 校验并清理一个相对路径：非空、非绝对路径、无卷名，且清理后
// 不得逃逸到父目录（. 或 .. 形式均拒绝）。返回清理后的安全相对路径。
func safeRelativePath(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", false
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	return cleaned, true
}
