// 本文件实现「上传技能归档」导入路径（POST /api/skills/import 的 multipart 分支）：
// 读取 .skill / .zip 归档，解压成 importedSkill，再交共享的 finishSkillImport 收尾。
// 安全重点：压缩前体积封顶、zip-slip / 绝对路径防护、逐文件解压限流（zip 头部
// 谎报体积也不能撑爆内存）、保留 SKILL.md 专属名、按单包字节/文件数上限把关。
package handler

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	skillpkg "github.com/multica-ai/multica/server/internal/skill"
)

// maxImportArchiveUploadSize 限制归档导入路径接受的压缩上传体。解压后的资源包仍
// 受既有的单文件/总量/文件数上限（maxImportFileSize、maxImportTotalSize、
// maxImportFileCount）约束；这个外层上限只是防止客户端在解压限流能生效之前就
// 流进一个无界的压缩体。
const maxImportArchiveUploadSize = skillpkg.MaxArchiveBytes // 80 MiB

// isMultipartForm 判断请求是否携带 multipart/form-data body（上传的技能归档），
// 而不是 JSON 的 URL 导入 body。
func isMultipartForm(r *http.Request) bool {
	return strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data")
}

// importSkillFromArchive 处理 body 是上传技能归档（.skill / .zip）时的
// POST /api/skills/import。它读取文件与可选的 on_conflict 表单字段，把归档解压
// 成 importedSkill，再交给共享的 finishSkillImport 收尾。归档路径总是产出结构化
// （status / skill / existing_skill）结果——没有需要兼容的 legacy pre-on_conflict
// 客户端。
func (h *Handler) importSkillFromArchive(w http.ResponseWriter, r *http.Request, workspaceID string, workspaceUUID, creatorUUID pgtype.UUID, creatorID string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportArchiveUploadSize)
	if err := r.ParseMultipartForm(maxImportArchiveUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart upload or file exceeds the size limit")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	onConflict := r.FormValue("on_conflict")
	if !validImportOnConflict(onConflict) {
		writeError(w, http.StatusBadRequest, "on_conflict must be one of: fail, overwrite, rename, skip")
		return
	}
	strategy := onConflict
	if strategy == "" {
		strategy = importOnConflictFail
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, `a skill archive file is required (form field "file")`)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read uploaded file")
		return
	}

	filename := ""
	if header != nil {
		filename = header.Filename
	}
	imported, err := parseSkillArchive(data, filename)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.finishSkillImport(w, r, workspaceID, workspaceUUID, creatorUUID, creatorID, strategy, true, imported)
}

// parseSkillArchive 把上传的技能归档（.skill / .zip）解压成 importedSkill。
// .skill 是标准 zip，其条目要么位于归档根（SKILL.md、scripts/...），要么嵌套在
// 单个顶层目录下（my-skill/SKILL.md、my-skill/scripts/...）——即 Anthropic 的
// package_skill 产出的布局。两种都以找到的最浅 SKILL.md 为根来接受。
//
// 安全：每个条目都校验穿越/绝对路径（zip-slip）；保留的 SKILL.md 附带路径被
// 丢弃；读取时对单文件大小设限（zip 头部谎报也不会撑爆内存）；共享的 addFile
// 强制单包字节与文件数上限。
func parseSkillArchive(data []byte, filename string) (*importedSkill, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("uploaded file is not a valid .skill/.zip archive")
	}

	// 定位技能根：最浅 SKILL.md 所在的目录。这同时接受根级 SKILL.md 与常见的
	// 单层包装布局。候选路径前置校验（拒绝绝对/穿越条目），恶意归档无法把不安全
	// 路径偷渡成主内容——保证每个被接受的条目都 zip-slip 安全。
	seen := map[string]bool{}
	var skillMd *zip.File
	rootPrefix := ""
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !validateFilePath(strings.TrimSuffix(f.Name, "/")) {
			return nil, fmt.Errorf("unsafe archive path: %s", f.Name)
		}
		if seen[f.Name] {
			return nil, fmt.Errorf("duplicate archive path: %s", f.Name)
		}
		seen[f.Name] = true
		clean := path.Clean(f.Name)
		if !strings.EqualFold(path.Base(clean), skillpkg.ContentFilename) {
			continue
		}
		if !validateFilePath(clean) {
			continue
		}
		prefix := archiveEntryPrefix(clean)
		if skillMd == nil || len(prefix) < len(rootPrefix) {
			skillMd = f
			rootPrefix = prefix
		}
	}
	if skillMd == nil {
		return nil, fmt.Errorf("archive does not contain a SKILL.md")
	}

	content, err := readZipFile(skillMd, maxImportFileSize)
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md: %w", err)
	}

	name, description := skillpkg.ParseSkillFrontmatter(content)
	if name == "" {
		name = skillNameFromArchive(rootPrefix, filename)
	}
	if name == "" {
		return nil, fmt.Errorf("could not determine the skill name: SKILL.md has no name field and the archive is unnamed")
	}

	imported := &importedSkill{
		name:        name,
		description: description,
		content:     content,
	}

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !validateFilePath(strings.TrimSuffix(f.Name, "/")) {
			return nil, fmt.Errorf("unsafe archive path: %s", f.Name)
		}
		clean := path.Clean(f.Name)
		// 只有解析出的技能根之下的文件才属于这个技能。
		if rootPrefix != "" && !strings.HasPrefix(clean, rootPrefix) {
			continue
		}
		rel := strings.TrimPrefix(clean, rootPrefix)
		if rel == "" {
			continue
		}
		// 根 SKILL.md 是正文；嵌套技能正文属于支持文件。
		if skillpkg.IsReservedContentPath(rel) {
			continue
		}
		if isIgnoredArchiveEntry(rel) {
			continue
		}
		// zip-slip / 绝对路径防护。
		if !validateFilePath(rel) || !f.Mode().IsRegular() {
			return nil, fmt.Errorf("unsafe archive path: %s", rel)
		}
		fileContent, ferr := readZipFile(f, maxImportFileSize)
		if ferr != nil {
			return nil, ferr
		}

		// addFile 校验整个资源包的大小、路径唯一性和文件数量。
		if err := imported.addFile(rel, fileContent); err != nil {
			return nil, err
		}
		imported.files[len(imported.files)-1].mode = skillpkg.FileMode(int32(f.Mode().Perm()))
	}

	sort.Slice(imported.files, func(i, j int) bool {
		return imported.files[i].path < imported.files[j].path
	})
	return imported, nil
}

// archiveEntryPrefix 返回某个已清洗、斜杠分隔的归档条目的目录前缀（含尾斜杠）：
// 根条目为 ""，"my-skill/SKILL.md" 为 "my-skill/"。
func archiveEntryPrefix(cleanName string) string {
	dir := path.Dir(cleanName)
	if dir == "." || dir == "/" {
		return ""
	}
	return dir + "/"
}

// skillNameFromArchive 在 SKILL.md 没有 name 字段时派生兜底技能名：技能嵌套时用
// 包装目录名，否则用去掉扩展名的上传文件名。
func skillNameFromArchive(rootPrefix, filename string) string {
	if rootPrefix != "" {
		base := path.Base(strings.TrimSuffix(rootPrefix, "/"))
		if base != "." && base != "/" && base != ".." {
			return base
		}
	}
	clean := strings.ReplaceAll(filename, "\\", "/")
	base := path.Base(clean)
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return strings.TrimSpace(base)
}

// isIgnoredArchiveEntry 把编辑器/操作系统噪音与 license 文件从附带包里滤掉，
// 镜像 daemon 的 local-skill 发现规则。
func isIgnoredArchiveEntry(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case "", ".git", ".svn", "__MACOSX", ".DS_Store":
			return true
		}
	}
	return false
}

// readZipFile 读取单个 zip 条目，把读取封顶在 maxSize+1 字节——头部少报未压缩
// 体积的条目无法逼迫无界分配。超过 maxSize 的条目被拒绝。
func readZipFile(f *zip.File, maxSize int64) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, maxSize+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxSize {
		return "", fmt.Errorf("file %q exceeds %d bytes", f.Name, maxSize)
	}
	return string(data), nil
}
