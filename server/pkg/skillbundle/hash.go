// Package skillbundle 计算 skill 清单（manifest）的确定性哈希，用于检测跨端
// （workspace/builtin/plugin）同步时 skill 内容是否变化。
package skillbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

const (
	// SourceWorkspace 表示 skill 来自工作区。
	SourceWorkspace = "workspace"
	// SourceBuiltin 表示 skill 是内置的。
	SourceBuiltin = "builtin"
	// SourcePlugin 表示 skill 来自插件。
	SourcePlugin = "plugin"
)

// File 是 skill 的一个附带支持文件（路径 + 内容）。
type File struct {
	Kind      string
	SHA256    string
	SizeBytes int64
	Mode      int32
	Path      string
	Content   string
}

// Skill 是一次待计算清单的 skill（含主内容与支持文件）。
type Skill struct {
	ID          string
	Source      string
	Name        string
	Description string
	Content     string
	Files       []File
}

// FileRef 是清单里单个支持文件的引用与摘要。
type FileRef struct {
	Path      string
	SHA256    string
	SizeBytes int64
}

// Manifest 是 skill 内容的确定性摘要，Hash 变化即内容变化。
type Manifest struct {
	Hash      string
	SizeBytes int64
	FileCount int
	Files     []FileRef
}

// BuildManifest 计算 skill 的确定性清单：主内容与支持文件都纳入哈希，文件按路径排序
// 保证顺序稳定。哈希形如 "sha256:<hex>"。
func BuildManifest(skill Skill) Manifest {
	files := append([]File(nil), skill.Files...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	h := sha256.New()
	version := "v1"
	for _, f := range files {
		if f.Kind == "binary" || f.Mode == 0755 {
			version = "v2"
			break
		}
	}
	writeHashPart(h, version)
	writeHashPart(h, skill.Source)
	writeHashPart(h, skill.ID)
	writeHashPart(h, skill.Name)
	writeHashPart(h, skill.Description)
	writeHashPart(h, skill.Content)

	size := int64(len(skill.Content))
	refs := make([]FileRef, 0, len(files))
	for _, file := range files {
		fileHash := sha256.Sum256([]byte(file.Content))
		fileDigest := "sha256:" + hex.EncodeToString(fileHash[:])
		fileSize := int64(len(file.Content))
		if file.Kind == "binary" {
			fileDigest = file.SHA256
			fileSize = file.SizeBytes
		}
		if version == "v2" {
			writeHashPart(h, fmt.Sprint(file.Mode))
			writeHashPart(h, file.Kind)
			writeHashPart(h, fmt.Sprint(fileSize))
		}
		writeHashPart(h, file.Path)
		writeHashPart(h, fileDigest)
		writeHashPart(h, file.Content)
		size += fileSize
		refs = append(refs, FileRef{
			Path:      file.Path,
			SHA256:    fileDigest,
			SizeBytes: fileSize,
		})
	}

	return Manifest{
		Hash:      "sha256:" + hex.EncodeToString(h.Sum(nil)),
		SizeBytes: size,
		FileCount: len(files),
		Files:     refs,
	}
}

// writeHashPart 把一段值连同其字节长度写入哈希器（长度前缀避免拼接歧义）。
func writeHashPart(h interface{ Write([]byte) (int, error) }, value string) {
	_, _ = fmt.Fprintf(h, "%d:%s\n", len(value), value)
}
