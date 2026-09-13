package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
)

// daemonIDFileName 保存这台机器稳定 daemon 标识符的文件。一旦创建，其中的 UUID
// 就是 daemon 的永久身份——主机名变化、.local 后缀漂移、profile 切换与系统改名
// 都不再会铸造出新的身份。
const daemonIDFileName = "daemon.id"

// EnsureDaemonID 返回本 daemon 实例的稳定 UUID，首次调用时持久化到磁盘。
// 身份按机器范围：同一台机器上的每个 profile 共享一个 UUID，存于
// `~/.multica/daemon.id`。profile 边界关乎 daemon 对接哪个 backend/账号，而非
// 物理机的身份，因此一台同时跑 CLI 拉起 daemon 与桌面拉起 daemon（或切换
// profile）的主机，到处都注册成一个 runtime，而不是 N 个。
//
// 保留 `profile` 参数纯粹是为了做一次性迁移：若规范文件尚不存在、而当前 profile
// 有来自 pre-#1220 布局的每 profile daemon.id 遗留，就把就地提升它，让以前在具名
// profile 下跑过 daemon 的用户保持同一 UUID，而不是"新铸造 + 合并"往返。任何
// 其它遗留的每 profile daemon.id 文件会单独经 LegacyDaemonUUIDs 浮出，让服务器在
// 注册时把它们的 runtime 行合并进规范行。
//
// 若文件存在但损坏（解析不了），会重新生成它，使 daemon 能继续启动而不是硬失败。
func EnsureDaemonID(profile string) (string, error) {
	dir, err := cli.ProfileDir("")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, daemonIDFileName)

	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			if _, perr := uuid.Parse(id); perr == nil {
				return id, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read daemon id file: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create profile directory: %w", err)
	}

	// One-time promotion from pre-change per-profile layout.
	if promoted, ok := promoteProfileDaemonID(profile, path); ok {
		return promoted, nil
	}

	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate daemon id: %w", err)
	}

	if err := writeDaemonIDFile(path, id.String()); err != nil {
		return "", err
	}
	return id.String(), nil
}

// promoteProfileDaemonID 把变更前布局遗留的每 profile daemon.id 复制到规范的机器
// 级位置。成功时返回被提升的 UUID 与 true；无有效内容可提升（profile 为空、源文件
// 缺失/损坏、任何 I/O 失败）返回 ("", false)。提升是一次 best-effort 迁移——这里
// 失败会落入重新铸造 UUID。
func promoteProfileDaemonID(profile, targetPath string) (string, bool) {
	if profile == "" {
		return "", false
	}
	profileDir, err := cli.ProfileDir(profile)
	if err != nil {
		return "", false
	}
	src := filepath.Join(profileDir, daemonIDFileName)
	data, err := os.ReadFile(src)
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(data))
	if _, err := uuid.Parse(id); err != nil {
		return "", false
	}
	if err := writeDaemonIDFile(targetPath, id); err != nil {
		return "", false
	}
	return id, true
}

// writeDaemonIDFile 用 0600 权限原子地把 UUID 写入 path（先写临时文件再改名）。
func writeDaemonIDFile(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon-*.id.tmp")
	if err != nil {
		return fmt.Errorf("create temp daemon id file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp daemon id file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp daemon id file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp daemon id file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename daemon id file: %w", err)
	}
	return nil
}

// LegacyDaemonIDs 返回这台机器在改用持久 UUID 之前可能注册过的 daemon_id 集合。
// 服务器在注册时用这份列表把旧 runtime 行合并进新 UUID 键控的行（移动 agent/任务
// 然后删除过期行）。
//
// 覆盖三种历史格式：
//
//   - pre-#906：  "<hostname>-<profile>"        （带 profile 后缀，不做 .local 剥离）
//   - pre-#1070： "<hostname>"                  （裸主机名，常以 .local 结尾）
//   - 当前：      "<hostname>"，按系统状态存在 .local 漂移
//
// .local 漂移是双向的——同一台机器在不同时刻 os.Hostname() 返回过 "foo" 与
// "foo.local"（mDNS 状态、系统重启、登录项顺序）。因此无论当前是哪种形态，我们
// 总是同时输出裸主机名与带 .local 后缀两个变体，让迁移覆盖以前持久化过的任意
// 形态。大小写漂移在服务器侧经大小写不敏感查找处理，因此这里不再额外输出大小写
// 排列。
func LegacyDaemonIDs(hostname, profile string) []string {
	host := strings.TrimSpace(hostname)
	if host == "" {
		return nil
	}
	stripped := strings.TrimSuffix(host, ".local")
	dotLocal := stripped + ".local"

	hostForms := []string{stripped, dotLocal}

	candidates := make([]string, 0, len(hostForms)*2)
	candidates = append(candidates, hostForms...)
	if profile != "" {
		for _, h := range hostForms {
			candidates = append(candidates, h+"-"+profile)
		}
	}

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

// LegacyDaemonUUIDs 扫描 `~/.multica/profiles/*/daemon.id` 并返回每个能通过解析的
// UUID。这些是在 daemon 身份变成机器级之前、按 profile 铸造出的身份；注册在它们
// 下的 runtime 行——可能分布在多个 backend（prod/dev/自托管）——需要合并进规范
// 机器 UUID。该列表可以安全发给每个 backend：一个从没在那个后端注册过的 UUID 在
// 服务器合并查找里只是匹配不到任何东西。
//
// 读取单个 profile 文件的错误被吞掉：坏文件不应阻塞 daemon 启动。profiles 目录
// 缺失返回 (nil, nil)——干净安装的常见情况。
func LegacyDaemonUUIDs() ([]string, error) {
	root, err := cli.ProfileDir("")
	if err != nil {
		return nil, err
	}
	profilesDir := filepath.Join(root, "profiles")
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read profiles dir: %w", err)
	}

	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(profilesDir, entry.Name(), daemonIDFileName))
		if err != nil {
			continue
		}
		id := strings.TrimSpace(string(data))
		if _, err := uuid.Parse(id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// filterLegacyIDs 移除任何等于 current 的条目（例如用户显式把 MULTICA_DAEMON_ID
// 钉到主机名本身时，没有需要迁移的——该行已经以当前 id 为键）。
func filterLegacyIDs(ids []string, current string) []string {
	if current == "" {
		return ids
	}
	out := ids[:0]
	for _, id := range ids {
		if id == current {
			continue
		}
		out = append(out, id)
	}
	return out
}
