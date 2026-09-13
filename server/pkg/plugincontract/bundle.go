package plugincontract

// 本文件职责：实现「发布包」的解析与校验——把作者上传的 zip（或本地目录）
// 变成一包经过验证的 Bundle：解析 manifest、收齐 manifest 点名的每个文件并
// 逐项做内容校验（surface 脚本可执行、skill 是合法文本），全程限流防膨胀。

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// bundle 就是作者发布的东西：manifest 加上 manifest 点名的每个文件。它是
// 管理员批准的单位，所以在发布时刻被解析、校验一次，此后绝不再从任何作者仍
// 能控制的东西里重新推导。
//
// 发布时校验正是关键。在 URL 托管下，entry 指向的文件如果不存在，会在几个月
// 后读者浏览器里静默失败；而这里发布会被拒绝，并指名缺失的路径。

const (
	// MaxBundleSize 限制上传的压缩包本身的字节数。
	MaxBundleSize = 2 << 20

	// MaxBundleFileSize 限制包里单个文件的大小。surface entry 是「一个脚本 +
	// 内联资源」，这正是它要装下的东西。
	MaxBundleFileSize = 1 << 20

	// MaxBundleTotalSize 限制一个版本保留的全部文件总和解压大小。按「读一个
	// 累加一次」的方式检查，所以解压后远大于压缩大小的压缩包会被拒绝而不是
	// 被缓存下来。
	MaxBundleTotalSize = 4 << 20

	// MaxBundleEntries 限制压缩包最多允许多少个条目，包括 manifest 从未点名的。
	MaxBundleEntries = 512

	// MaxSkillBytes 限制单个 SKILL.md 的大小。对散文足够宽松，又小到发布
	// 不能被用来把大段内容塞进数据库。
	MaxSkillBytes = 256 * 1024
)

// BundleFile 是一个已发布版本的某个存档文件。路径就是 manifest 自己的相对
// entry，所以服务时刻无需再解析一次。
type BundleFile struct {
	Path    string
	Content []byte
}

// Bundle 是一包已解析、已校验的产物。
type Bundle struct {
	Manifest Manifest
	// Canonical 是安装时作为「已同意快照」存储的重编码 manifest，
	// 与 ParseManifest 返回的一致。
	Canonical []byte
	// Files 只保留 manifest 引用的条目，按路径排序。压缩包里其余一切都被
	// 丢弃：没有任何东西会去服务它，留着只会是有读无路的存储。
	Files []BundleFile
}

// File 按 manifest 相对路径返回 bundle 里的某个文件。
func (b Bundle) File(entry string) ([]byte, bool) {
	for _, file := range b.Files {
		if file.Path == entry {
			return file.Content, true
		}
	}
	return nil, false
}

// TotalSize 是 bundle 文件的存储占用总和。
func (b Bundle) TotalSize() int {
	total := 0
	for _, file := range b.Files {
		total += len(file.Content)
	}
	return total
}

// ParseBundle 把一个 zip 压缩包读成一包已验证的 bundle。
func ParseBundle(archive []byte) (Bundle, error) {
	if len(archive) == 0 {
		return Bundle{}, fmt.Errorf("plugin package is empty")
	}
	if len(archive) > MaxBundleSize {
		return Bundle{}, fmt.Errorf("plugin package exceeds %d bytes", MaxBundleSize)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return Bundle{}, fmt.Errorf("plugin package must be a zip archive: %w", err)
	}
	if len(reader.File) > MaxBundleEntries {
		return Bundle{}, fmt.Errorf("plugin package contains more than %d entries", MaxBundleEntries)
	}

	entries, err := zipEntryPaths(reader)
	if err != nil {
		return Bundle{}, err
	}
	prefix, err := manifestPrefix(entries)
	if err != nil {
		return Bundle{}, err
	}

	// 文件是惰性读取的，所以一个塞满巨大未引用条目的压缩包零成本：只有
	// manifest 点名的才会被解压，且每落一个文件都检查累计总额。
	total := 0
	open := func(entry string) ([]byte, bool, error) {
		file, ok := findZipFile(reader, prefix+entry)
		if !ok {
			return nil, false, nil
		}
		content, readErr := readZipFile(file)
		if readErr != nil {
			return nil, false, readErr
		}
		total += len(content)
		if total > MaxBundleTotalSize {
			return nil, false, fmt.Errorf("plugin package files exceed %d bytes", MaxBundleTotalSize)
		}
		return content, true, nil
	}
	return buildBundle(open, prefix)
}

// ParseBundleFromDir 从文件系统目录构造同一份已验证 bundle，供本地开发通道
// 使用。`read` 是调用方已做越界检查的读取器——本包绝不直接碰文件系统。
func ParseBundleFromDir(read func(entry string) ([]byte, bool, error)) (Bundle, error) {
	total := 0
	open := func(entry string) ([]byte, bool, error) {
		content, ok, err := read(entry)
		if err != nil || !ok {
			return nil, ok, err
		}
		if len(content) > MaxBundleFileSize {
			return nil, false, fmt.Errorf("plugin file %q exceeds %d bytes", entry, MaxBundleFileSize)
		}
		total += len(content)
		if total > MaxBundleTotalSize {
			return nil, false, fmt.Errorf("plugin package files exceed %d bytes", MaxBundleTotalSize)
		}
		return content, true, nil
	}
	return buildBundle(open, "")
}

// buildBundle 是两条发布路径共享的后半段：解析 manifest，再精确收齐它引用的
// 文件。上传包与本地目录都跑这里，因此两者绝不可能被按不同标准校验。
func buildBundle(open func(entry string) ([]byte, bool, error), prefix string) (Bundle, error) {
	rawManifest, ok, err := open(ManifestFilename)
	if err != nil {
		return Bundle{}, err
	}
	if !ok {
		if prefix != "" {
			return Bundle{}, fmt.Errorf("plugin package must contain %s", prefix+ManifestFilename)
		}
		return Bundle{}, fmt.Errorf("plugin package must contain %s", ManifestFilename)
	}
	manifest, canonical, err := ParseManifest(rawManifest)
	if err != nil {
		return Bundle{}, err
	}

	files := make([]BundleFile, 0, len(manifest.Contributes.Surfaces)+len(manifest.Contributes.Resources)+1)
	seen := map[string]bool{}
	collect := func(entry string) ([]byte, error) {
		if seen[entry] {
			// 两个贡献合法地指向同一文件是允许的；存两次会撞
			// (version_id, path) 唯一索引。
			content, _ := findFile(files, entry)
			return content, nil
		}
		content, ok, err := open(entry)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("plugin package is missing %q, which the manifest declares", entry)
		}
		if len(content) > MaxBundleFileSize {
			return nil, fmt.Errorf("plugin file %q exceeds %d bytes", entry, MaxBundleFileSize)
		}
		seen[entry] = true
		files = append(files, BundleFile{Path: entry, Content: content})
		return content, nil
	}

	for _, surface := range manifest.Contributes.Surfaces {
		content, err := collect(surface.Entry)
		if err != nil {
			return Bundle{}, err
		}
		if err := validateSurfaceScript(surface.Entry, content); err != nil {
			return Bundle{}, err
		}
	}
	for _, resource := range manifest.Contributes.Resources {
		content, err := collect(resource.Entry)
		if err != nil {
			return Bundle{}, err
		}
		if err := validateSkillFile(resource.Entry, content); err != nil {
			return Bundle{}, err
		}
	}
	if manifest.Icon != "" {
		content, err := collect(manifest.Icon)
		if err != nil {
			return Bundle{}, err
		}
		if len(content) == 0 {
			return Bundle{}, fmt.Errorf("plugin icon %q is empty", manifest.Icon)
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return Bundle{Manifest: manifest, Canonical: canonical, Files: files}, nil
}

func findFile(files []BundleFile, entry string) ([]byte, bool) {
	for _, file := range files {
		if file.Path == entry {
			return file.Content, true
		}
	}
	return nil, false
}

// validateSurfaceScript 拒绝那些「只会在读者浏览器里更晚失败」的东西。host 把
// 这段文本内联进 surface 文档，所以它必须是文本；而且静态 `import` 在那里
// 解析不了——surface 是一个没有模块图的单文件，这正是 SDK 面向的契约。
func validateSurfaceScript(entry string, content []byte) error {
	if len(bytes.TrimSpace(content)) == 0 {
		return fmt.Errorf("surface entry %q is empty", entry)
	}
	if !utf8.Valid(content) {
		return fmt.Errorf("surface entry %q must be UTF-8 text", entry)
	}
	moduleSyntax, err := parseSurfaceScript(content)
	if err != nil {
		return fmt.Errorf("surface entry %q is not valid JavaScript: %w", entry, err)
	}
	if moduleSyntax {
		return fmt.Errorf("surface entry %q has top-level import/export/await or import.meta; a surface is a single classic script with no module graph, so bundle its dependencies in", entry)
	}
	return nil
}

// parseSurfaceScript 用真正的 JavaScript 解析器而不是手写识别源码文本。
// 发布是边界：误报会毫无绕路地把作者挡在门外，而漏掉仅模块可用的语法会变成
// 一帧坏掉的 classic-script。因此正则字面量、模板表达式、`import` 与 `(` 之间
// 的注释、一行两条语句等词法细节都必须遵循 JavaScript 文法，而不是某种本地
// 近似。
func parseSurfaceScript(source []byte) (bool, error) {
	ast, err := js.Parse(parse.NewInputBytes(source), js.Options{})
	if err != nil {
		return false, err
	}
	visitor := &surfaceModuleSyntaxVisitor{}
	js.Walk(visitor, ast)
	return visitor.moduleOnly, nil
}

// tdewolff 在根上按模块文法解析，此时顶层 await 与 import.meta 都合法。surface
// entry 按 classic script 执行，所以要遍历解析树、显式拒绝那些构造。嵌套的
// async 函数里 await 仍合法；import.meta 在任何深度都只属于模块。
type surfaceModuleSyntaxVisitor struct {
	functionDepth int
	moduleOnly    bool
}

func (v *surfaceModuleSyntaxVisitor) Enter(node js.INode) js.IVisitor {
	switch node := node.(type) {
	case *js.ImportStmt, *js.ExportStmt, *js.ImportMetaExpr:
		v.moduleOnly = true
	case *js.UnaryExpr:
		if node.Op == js.AwaitToken && v.functionDepth == 0 {
			v.moduleOnly = true
		}
	case *js.FuncDecl, *js.ArrowFunc:
		v.functionDepth++
	}
	return v
}

func (v *surfaceModuleSyntaxVisitor) Exit(node js.INode) {
	switch node.(type) {
	case *js.FuncDecl, *js.ArrowFunc:
		v.functionDepth--
	}
}

func validateSkillFile(entry string, content []byte) error {
	if len(bytes.TrimSpace(content)) == 0 {
		return fmt.Errorf("skill resource %q is empty", entry)
	}
	if !utf8.Valid(content) {
		return fmt.Errorf("skill resource %q must be UTF-8 text", entry)
	}
	if len(content) > MaxSkillBytes {
		return fmt.Errorf("skill resource %q exceeds %d bytes", entry, MaxSkillBytes)
	}
	return nil
}

// zipEntryPaths 返回压缩包的文件路径，拒绝任何「一旦按路径写盘或读取就能逃出
// 目录」的东西。bundle 从不解压到磁盘，但路径会与 manifest entry 比对，而
// 「归一化后与按字面读取不一致」的路径，是「发一个文件却让另一个文件获得
// 同意」的通道。
func zipEntryPaths(reader *zip.Reader) ([]string, error) {
	paths := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		name := file.Name
		if strings.HasSuffix(name, "/") {
			continue // 目录条目
		}
		if file.Mode()&fs.ModeSymlink != 0 {
			// zip 里的符号链接把目标当作内容携带。这里没有任何逻辑会去跟随
			// 它，但把它存下来等于发布一个「含义取决于我们这边并不存在的
			// 文件系统」的文件。
			return nil, fmt.Errorf("plugin package must not contain symlinks (%q)", name)
		}
		if strings.ContainsRune(name, '\\') {
			return nil, fmt.Errorf("plugin package entry %q must use forward slashes", name)
		}
		if path.IsAbs(name) || name != path.Clean(name) || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("plugin package entry %q must be a plain relative path", name)
		}
		paths = append(paths, name)
	}
	return paths, nil
}

// manifestPrefix 找出 manifest 所在的目录。
//
// 打包一个文件夹是人的自然动作，产出是 `my-plugin/multica.plugin.json`。
// 接受恰好一层前导目录，能把最常见的发布失败变成无感事件；两个不同根仍是
// 错误，因为那样「这个包是什么」就没有唯一答案了。
func manifestPrefix(paths []string) (string, error) {
	candidates := map[string]bool{}
	for _, name := range paths {
		if name == ManifestFilename {
			return "", nil
		}
		if dir, file := path.Split(name); file == ManifestFilename {
			candidates[dir] = true
		}
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("plugin package must contain %s", ManifestFilename)
	case 1:
		for dir := range candidates {
			if strings.Count(strings.TrimSuffix(dir, "/"), "/") > 0 {
				return "", fmt.Errorf("%s must be at the root of the package, or one directory below it", ManifestFilename)
			}
			return dir, nil
		}
	}
	return "", fmt.Errorf("plugin package contains more than one %s", ManifestFilename)
}

func findZipFile(reader *zip.Reader, name string) (*zip.File, bool) {
	for _, file := range reader.File {
		if file.Name == name {
			return file, true
		}
	}
	return nil, false
}

func readZipFile(file *zip.File) ([]byte, error) {
	// 压缩包声明了未压缩大小；那只是构建它的人的一句自述，所以只用来提前
	// 拒绝。下面的读取自带边界、不信任它。
	if file.UncompressedSize64 > MaxBundleFileSize {
		return nil, fmt.Errorf("plugin file %q exceeds %d bytes", file.Name, MaxBundleFileSize)
	}
	handle, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("read plugin file %q: %w", file.Name, err)
	}
	defer handle.Close()
	content, err := io.ReadAll(io.LimitReader(handle, MaxBundleFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read plugin file %q: %w", file.Name, err)
	}
	if len(content) > MaxBundleFileSize {
		return nil, fmt.Errorf("plugin file %q exceeds %d bytes", file.Name, MaxBundleFileSize)
	}
	return content, nil
}
