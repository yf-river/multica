// local.go 实现把对象存在本地磁盘目录的 Storage 后端，用于开发/自托管等无需 S3 的场景。
package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// LocalStorage 把对象写入本地目录，并通过 baseURL 暴露访问 URL。
type LocalStorage struct {
	uploadDir string
	baseURL   string
}

// metaSuffix 是磁盘上旁车（sidecar）JSON 文件的扩展名，用来记录上传的原始文件名与
// 嗅探出的 content type。旁车文件让 ServeFile 能像 S3 的 PutObject 路径那样设置
// Content-Disposition，而不是让浏览器退回到用存储键的 basename 当下载文件名。
const metaSuffix = ".meta.json"

// tempSuffix 是 writeAtomic 先写、再 rename 就位的暂存文件的扩展名。每个 key 确定
// 唯一，因此崩溃遗留物仍可被回收——见 tempPath。
const tempSuffix = ".tmp"

// localMeta 记录一份上传的原始文件名与 content type。
type localMeta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// NewLocalStorageFromEnv 从环境变量创建 LocalStorage；无法创建上传目录时返回 nil。
//
// 环境变量：
//   - LOCAL_UPLOAD_DIR（默认 "./data/uploads"）
//   - LOCAL_UPLOAD_BASE_URL（可选，如 "http://localhost:8080"）
func NewLocalStorageFromEnv() *LocalStorage {
	uploadDir := os.Getenv("LOCAL_UPLOAD_DIR")
	if uploadDir == "" {
		uploadDir = "./data/uploads"
	}

	if required := os.Getenv("LOCAL_UPLOAD_REQUIRED_MOUNT"); required != "" {
		data, err := os.ReadFile("/proc/self/mountinfo")
		mounted := false
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 4 && fields[4] == required {
				mounted = true
				break
			}
		}
		if err != nil || !mounted || !isUnder(required, uploadDir) {
			slog.Error("required upload mount unavailable", "mount", required)
			return nil
		}
	}
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		slog.Error("failed to create upload directory", "dir", uploadDir, "error", err)
		return nil
	}

	baseURL := strings.TrimSuffix(os.Getenv("LOCAL_UPLOAD_BASE_URL"), "/")

	slog.Info("local storage initialized", "dir", uploadDir, "baseURL", baseURL)
	return &LocalStorage{
		uploadDir: uploadDir,
		baseURL:   baseURL,
	}
}

func (s *LocalStorage) CdnDomain() string {
	if s.baseURL == "" {
		return ""
	}
	u, err := url.Parse(s.baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (s *LocalStorage) KeyFromURL(rawURL string) string {
	if s.baseURL != "" && strings.HasPrefix(rawURL, s.baseURL) {
		rawURL = strings.TrimPrefix(rawURL, s.baseURL)
	}

	prefix := "/uploads/"
	if idx := strings.Index(rawURL, prefix); idx >= 0 {
		return rawURL[idx+len(prefix):]
	}
	if i := strings.LastIndex(rawURL, "/"); i >= 0 {
		return rawURL[i+1:]
	}
	return rawURL
}

// GetReader 打开底层文件供流式读取。拒绝解析到 uploadDir 之外的 key（防御存有
// 遍历组件的 key），也拒绝旁车文件后缀，免得 /content 被诱导泄漏 .meta.json 内容。
func (s *LocalStorage) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == "" {
		return nil, fmt.Errorf("local GetReader: empty key")
	}
	if isInternalLocalPath(key) {
		return nil, fmt.Errorf("local GetReader: refusing to serve internal key %q", key)
	}
	filePath := filepath.Join(s.uploadDir, key)
	if !isUnder(s.uploadDir, filePath) {
		return nil, fmt.Errorf("local GetReader: key escapes upload dir: %q", key)
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("local GetReader: %w", err)
	}
	return f, nil
}

func (s *LocalStorage) Delete(ctx context.Context, key string) {
	if err := s.DeleteObject(ctx, key); err != nil {
		slog.Error("local storage Delete failed", "key", key, "error", err)
	}
}

// DeleteObject 是把失败暴露出来的 Delete——媒体协调器需要它来保留台账行并安排重试，
// 而不是假定成功。文件不存在视为成功（删除是幂等的）。暂存文件也一并删除：写入与
// rename 之间崩溃会留下它，这里正好回收（媒体台账在上传前就记录了意图，因此协调器
// 对废弃 key 总会走到这次删除）。
func (s *LocalStorage) DeleteObject(_ context.Context, key string) error {
	if key == "" {
		return nil
	}
	filePath := filepath.Join(s.uploadDir, key)
	for _, p := range []string{filePath, filePath + metaSuffix, tempPath(filePath)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ObjectURL 返回"对 key 成功 Upload/UploadStream 会返回的 URL"——纯配置函数，
// 因此媒体意图台账可以在上传前就持久化它。
func (s *LocalStorage) ObjectURL(key string) string {
	if s.baseURL != "" {
		return fmt.Sprintf("%s/uploads/%s", s.baseURL, key)
	}
	return fmt.Sprintf("/uploads/%s", key)
}

func (s *LocalStorage) DeleteKeys(ctx context.Context, keys []string) {
	for _, key := range keys {
		s.Delete(ctx, key)
	}
}

func (s *LocalStorage) Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error) {
	dest := filepath.Join(s.uploadDir, key)
	if err := writeAtomic(dest, bytes.NewReader(data)); err != nil {
		return "", err
	}
	// 尽力而为的旁车写入：让 ServeFile 能在 Content-Disposition 里还原原始文件名。
	// 失败只记日志、不让上传失败——文件仍可用，只是少了人类可读的下载名。没有
	// 文件名可保留时跳过：不带文件名的旁车是死重，因为 ServeFile 只为该字段读它。
	if filename != "" {
		body, _ := json.Marshal(localMeta{Filename: filename, ContentType: contentType})
		if err := os.WriteFile(dest+metaSuffix, body, 0644); err != nil {
			slog.Error("local storage meta write failed", "key", key, "error", err)
		}
	}

	if s.baseURL != "" {
		return fmt.Sprintf("%s/uploads/%s", s.baseURL, key), nil
	}
	return fmt.Sprintf("/uploads/%s", key), nil
}

func (s *LocalStorage) UploadStream(ctx context.Context, key string, data io.Reader, _ int64, contentType string, filename string) (string, error) {
	dest := filepath.Join(s.uploadDir, key)
	if err := writeAtomic(dest, data); err != nil {
		return "", err
	}
	if filename != "" {
		body, _ := json.Marshal(localMeta{Filename: filename, ContentType: contentType})
		if err := os.WriteFile(dest+metaSuffix, body, 0644); err != nil {
			slog.Error("local storage meta write failed", "key", key, "error", err)
		}
	}

	if s.baseURL != "" {
		return fmt.Sprintf("%s/uploads/%s", s.baseURL, key), nil
	}
	return fmt.Sprintf("/uploads/%s", key), nil
}

// writeAtomic 先经目标目录内的临时文件写入，再 rename 就位。若直接写 dest，
// 会一开始就截断已有对象，中途失败会把同 key 之前上传成功的文件毁掉（旧清理甚至
// 直接删了它）——附件行就可能指向一个已不存在的文件。rename 就位后，失败的写入
// 只会丢弃自己的临时文件，已有对象安然无恙。两条上传路径都走这里，谁也无法
// 重新引入这种破坏性写法。
func writeAtomic(dest string, src io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("local storage MkdirAll: %w", err)
	}
	tmp := tempPath(dest)
	// 打开即 0644，与原来直接写的模式一致（os.CreateTemp 会建 0600，还得 chmod，
	// 某些挂载点拒绝 chmod）。
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("local storage create temp: %w", err)
	}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("local storage stream copy: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("local storage Close: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("local storage Rename: %w", err)
	}
	return nil
}

// tempPath 是 writeAtomic 将要 rename 就位的暂存文件。它由对象 key 派生而不是随机
// （os.CreateTemp），因此写入与 rename 之间崩溃留下的文件，删除路径仍能找到：
// DeleteObject 会连同对象一起删它，意味着媒体意图台账也能回收它——台账行在上传前
// 就写好，协调器删除该 key 时遗留物一并消失。随机后缀会留下一个谁都无法点名回收的
// 孤儿，每个最多 100 MiB 资源上限，且跨崩溃无界累积。
//
// 名字对同一 key 稳定，因此两个并发写同一 key 的写者会共享它。这里不会发生——
// 媒体 key 是按 (chat message, resource) 的，只会解析一次，其余上传路径都会铸造
// 全新 UUID key——而且这类写者本来就在目标文件上互相竞争。
func tempPath(dest string) string {
	return filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+tempSuffix)
}

// isInternalLocalPath 判断 key 是否指向本地后端自己的文件——.meta.json 旁车或
// .<name>.tmp 暂存文件——而不是某个对象。读路径的 key 直接取自请求 URL，因此两者
// 都不可达：暂存文件否则会暴露半个写入的正文。对象 key 是服务端生成的
// （UUID 或哈希 basename），永远不会以点开头，所以用户上传 "notes.tmp"
// （key "<uuid>.tmp"）不受影响。
func isInternalLocalPath(key string) bool {
	if strings.HasSuffix(key, metaSuffix) {
		return true
	}
	base := filepath.Base(key)
	return strings.HasPrefix(base, ".") && strings.HasSuffix(base, tempSuffix)
}

// GetFilePath 返回 key 对应的磁盘绝对路径。
func (s *LocalStorage) GetFilePath(key string) string {
	return filepath.Join(s.uploadDir, key)
}

// ServeFile 提供与 S3 后端对齐的附件下载语义：优先用旁车元数据里的原始文件名设置
// Content-Disposition。
func (s *LocalStorage) ServeFile(w http.ResponseWriter, r *http.Request, filename string) {
	// 旁车与暂存文件是本地后端的实现细节；拒绝直接服务它们，免得
	// /uploads/<key>.meta.json（或半写入的 .<key>.tmp）成为稳定的读 API。
	// 放在任何磁盘操作之前，让对兄弟路径的穿越尝试无法触发树外读取。
	if isInternalLocalPath(filename) {
		http.NotFound(w, r)
		return
	}

	filePath := filepath.Join(s.uploadDir, filename)
	// filepath.Join 会清理路径但不强制包含关系，因此调用方传 "../etc/passwd"
	// 会落到 uploadDir 之外。http.ServeFile 会按 r.URL.Path 拒绝这类请求，但
	// readLocalMeta 在其之前执行——没有这个守卫，构造过的路径可能在 400 落地前
	// 就触发对任意 <some-path>.meta.json 的越界磁盘读。
	if !isUnder(s.uploadDir, filePath) {
		http.NotFound(w, r)
		return
	}
	slog.Info("serving file", "filename", filename, "filepath", filePath)

	// 镜像 S3 Upload 路径：该 key 存在旁车元数据时，用上传时的原始文件名设置
	// Content-Disposition。没有它，浏览器会用存储键的 basename（UUID + 扩展名）
	// 作为下载名，而不是上传者选的人类可读名字。旁车机制上线前上传的文件在磁盘
	// 上没有 .meta.json，会落到原有行为。
	if meta, ok := readLocalMeta(filePath); ok && meta.Filename != "" {
		w.Header().Set("Content-Disposition", ContentDisposition(meta.ContentType, meta.Filename))
	}

	// http.ServeFile 自带路径穿越防护，会清理路径并阻止目录外访问。
	http.ServeFile(w, r, filePath)
}

// isUnder 判断 target 是否解析到 dir 内（含等于 dir）。两个入参都经 filepath.Clean，
// 因此尾斜杠与 "." 段骗不过比较。
func isUnder(dir, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// readLocalMeta 读取并解析 key 对应的旁车元数据；缺失或损坏返回 ok=false。
func readLocalMeta(filePath string) (localMeta, bool) {
	body, err := os.ReadFile(filePath + metaSuffix)
	if err != nil {
		return localMeta{}, false
	}
	var meta localMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return localMeta{}, false
	}
	return meta, true
}

func (s *LocalStorage) UploadFromReader(ctx context.Context, key string, reader io.Reader, contentType string, filename string) (string, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("local storage ReadAll: %w", err)
	}

	return s.Upload(ctx, key, data, contentType, filename)
}
