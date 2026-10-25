package codediff

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// RecordInput 保存认证下载的原文，供运行结束时区分输入与实际编辑。
func RecordInput(directory, path string, data []byte) error {
	if directory == "" {
		return nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	record := struct {
		Path string
		Data []byte
	}{absolute, data}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file := filepath.Join(directory, sha256Hex([]byte(absolute))+".json")
	// 同一路径只保留首次下载基线。
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(raw)
	return err
}

func (c *Capture) loadInputs() {
	if c.before == nil || c.spec.InputDirectory == "" {
		return
	}
	entries, err := os.ReadDir(c.spec.InputDirectory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(c.spec.InputDirectory, entry.Name()))
		if err != nil {
			continue
		}
		var record struct {
			Path string
			Data []byte
		}
		if json.Unmarshal(raw, &record) != nil || !pathInside(record.Path, c.spec.WorkDir) {
			continue
		}
		rel, err := filepath.Rel(c.spec.WorkDir, record.Path)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if _, exists := c.before.Files[rel]; exists {
			continue
		}
		data := record.Data
		meta := &fileMeta{Size: int64(len(data)), Mode: 0100644, Sum: sha256Hex(data), Lines: countLines(data), Binary: isBinary(data), Redacted: IsSecretPath(rel)}
		if !meta.Redacted && !meta.Binary && int64(len(data)) <= c.spec.Options.normalized().MaxFileBytes && c.before.storeContent(meta.Sum, data) == nil {
			meta.Stored = true
		}
		c.before.Files[rel] = meta
	}
}
