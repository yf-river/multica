package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/multica-ai/multica/server/pkg/skillbundle"
)

// SkillBundleCache 是把已下载技能包持久化在磁盘上的缓存：以
// (workspace, source, id, hash) 为路径键，写盘用"临时目录 + 原子改名"，读盘会
// 重新校验包，防止磁盘上的损坏/篡改内容被直接信任。带 per-key 锁以合并并发下载。
type SkillBundleCache struct {
	root      string
	rename    func(string, string) error
	removeAll func(string) error
	mu        sync.Mutex
	locks     map[string]*sync.Mutex
}

// NewSkillBundleCache 构造缓存，root 为缓存根目录（测试可注入 rename/removeAll
// 假实现）。
func NewSkillBundleCache(root string) *SkillBundleCache {
	return &SkillBundleCache{
		root:      root,
		rename:    os.Rename,
		removeAll: os.RemoveAll,
		locks:     make(map[string]*sync.Mutex),
	}
}

// Load 按引用读缓存中的包并校验；未命中、读失败或校验不过（会把坏文件删掉）
// 都返回 (零值, false)。
func (c *SkillBundleCache) Load(workspaceID string, ref SkillRefData) (SkillData, bool) {
	if c == nil || c.root == "" {
		return SkillData{}, false
	}
	keyPath := c.bundlePath(workspaceID, ref)
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return SkillData{}, false
	}
	var bundle SkillData
	if err := json.Unmarshal(data, &bundle); err != nil || !validateSkillBundle(ref, bundle) {
		_ = os.Remove(keyPath)
		return SkillData{}, false
	}
	return bundle, true
}

// Store 原子地写入一个包：先在同一父目录下写临时目录，再整体改名到位；若目标
// 已存在（并发者先到）则删掉旧目标重试一次。中途失败不会留下半写状态。
func (c *SkillBundleCache) Store(workspaceID string, bundle SkillData) error {
	if c == nil || c.root == "" {
		return nil
	}
	ref := SkillRefData{ID: bundle.ID, Source: bundle.Source, Hash: bundle.Hash}
	dir := filepath.Dir(c.bundlePath(workspaceID, ref))
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".bundle-*")
	if err != nil {
		if mkErr := os.MkdirAll(filepath.Dir(dir), 0o755); mkErr != nil {
			return mkErr
		}
		tmp, err = os.MkdirTemp(filepath.Dir(dir), ".bundle-*")
		if err != nil {
			return err
		}
	}
	defer os.RemoveAll(tmp)

	data, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "bundle.json"), data, 0o644); err != nil {
		return err
	}
	if err := c.removeAll(dir); err != nil {
		return err
	}
	if err := c.rename(tmp, dir); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := c.removeAll(dir); err != nil {
			return err
		}
		if err := c.rename(tmp, dir); err != nil {
			return err
		}
	}
	return nil
}

// WithRefLock 为某个技能引用取排它锁后执行 fn，把"下载同一引用"的并发调用串行
// 化（fn 内可再次查缓存避免重复下载）。
func (c *SkillBundleCache) WithRefLock(workspaceID string, ref SkillRefData, fn func() error) error {
	if c == nil {
		return fn()
	}
	key := workspaceID + "\x00" + ref.Source + "\x00" + ref.ID + "\x00" + ref.Hash
	lock := c.lockForKey(key)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

// lockForKey 取/建某个键的专用锁（键条目常驻，简化生命周期）。
func (c *SkillBundleCache) lockForKey(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lock := c.locks[key]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	c.locks[key] = lock
	return lock
}

// bundlePath 计算某引用的缓存文件路径；各段都经 safeCacheSegment 清洗，杜绝
// 路径穿越。
func (c *SkillBundleCache) bundlePath(workspaceID string, ref SkillRefData) string {
	return filepath.Join(
		c.root,
		safeCacheSegment(workspaceID),
		safeCacheSegment(ref.Source),
		safeCacheSegment(ref.ID),
		safeCacheSegment(ref.Hash),
		"bundle.json",
	)
}

// validateSkillBundle 校验缓存里的包与期望引用一致：ID/source/hash/文件数匹配，
// 文件路径安全，且由包自身重算的清单哈希与大小和 ref 完全一致。
func validateSkillBundle(ref SkillRefData, bundle SkillData) bool {
	if bundle.ID != ref.ID || bundle.Source != ref.Source || bundle.Hash != ref.Hash {
		return false
	}
	if len(bundle.Files) != ref.FileCount {
		return false
	}
	files := make([]skillbundle.File, 0, len(bundle.Files))
	for _, file := range bundle.Files {
		if !safeSkillFilePath(file.Path) {
			return false
		}
		files = append(files, skillbundle.File{Path: file.Path, Content: file.Content})
	}
	manifest := skillbundle.BuildManifest(skillbundle.Skill{
		ID:          bundle.ID,
		Source:      bundle.Source,
		Name:        bundle.Name,
		Description: bundle.Description,
		Content:     bundle.Content,
		Files:       files,
	})
	if manifest.Hash != ref.Hash {
		return false
	}
	if ref.SizeBytes > 0 && manifest.SizeBytes != ref.SizeBytes {
		return false
	}
	return true
}

// safeSkillFilePath 校验技能包内文件路径：非空、无 NUL/反斜杠/绝对路径，清理后
// 不得变化或逃逸到父目录。
func safeSkillFilePath(p string) bool {
	if p == "" || strings.Contains(p, "\x00") || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	clean := path.Clean(p)
	if clean == "." || clean != p || strings.HasPrefix(clean, "../") || clean == ".." {
		return false
	}
	return true
}

// safeCacheSegment 把任意字符串清洗成安全的单一路径段（字母数字、-、_、. 之外
// 全部换成 _），空串返回 "_"，"." / ".." 加前缀避免目录逃逸。
func safeCacheSegment(s string) string {
	var b strings.Builder
	if s == "" {
		return "_"
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "." || out == ".." {
		return fmt.Sprintf("_%s", out)
	}
	return out
}
