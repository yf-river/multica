// 背景
//
// Hermes 把长期记忆（MEMORY.md / USER.md 以及 agent 写回的任何内容）放在
// `<HERMES_HOME>/memories/`。Multica 必须接管 HERMES_HOME 才能注入 agent
// 绑定的技能——Hermes 只从自己的 home 发现技能，从不看工作区（见
// hermes_home.go）——于是叠加层"顺带继承了"记忆放哪里的决定，并把它变成了
// 任务本地。
//
// 没有人刻意设计出以下两种并存的行为（issue #6638）：
//
//   - 绑定了技能的 agent，每个任务都拿到一份全新的 memories/，于是任务之间
//     忘光一切。用户把这种表现读作"Hermes 失忆"，而且它是在绑定一个毫不相干
//     的技能那一刻才出现的，没有任何 UI 信号。
//   - 没绑技能的 agent 根本不建叠加层，直接跑在用户真实的 ~/.hermes 上，因而
//     与运行时上所有其他"无技能 agent、任务、issue"共享一份 memories/。
//
// 本文件把记忆变成 Multica 拥有、按 agent 划分的存储：
//
//	<multica profile dir>/hermes-state/<agent>/<hermes profile>/memories
//
// 链进每任务叠加层。于是同一 agent 的记忆跨越任务与 issue 存活，绝不触碰
// 用户的 home，其他 agent 也看不见。其形态刻意与"按 issue 划分的 Codex 会话
// 存储"（codex_home.go）和"按 agent 划分的 Reasonix 状态 home"保持一致——
// 同样的生命周期、同样的 GC、同样的 active-store 守卫。
//
// 粒度设为 (hermes profile, agent) 而非仅 agent，是为了镜像 Hermes 自己的模型：
// 在那里 profile 是一个隔离的实例——把 agent 指向另一个 profile 等于给它另一
// 个身份，因此也该有另一条记忆线。与 Codex 存储按 (profile, agent, issue) 键控
// 的语义相同。
//
// 刻意不在本文件范围内：
//
//   - 会话数据库（state.db 及其 WAL 侧车）保持任务本地。在一个 agent 的并发
//     任务间共享一份存活的 SQLite 数据库，需要普通内存文件不需要的加锁与
//     一致性工作——外加 Windows 对 -shm 的字节区间锁。用户得到的是"记得约定
//     "，而非"记得完整记录"。
//   - 无技能的 Hermes agent 仍原样跑在宿主 home 上。收紧这一点会破坏 #6638
//     报告者仅有的变通手段，且它属于平台级 memory_scope 工作（MUL-5969）。
//
// 已知取舍，与其取代的"宿主直通"行为一致：一个 agent 的并发任务写同一批
// 文件，而 Hermes 会整体重写它们，所以并发记忆更新是"后写者胜"。

package execenv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// hermesMemoryStoreRoot 是 daemon 的 Multica profile 目录下、承载每个 agent
// 持久化 Hermes 状态的目录。取 `hermes-state`（而非 `hermes-memories`），
// 是因为会话那半解决后 state.db 也会加入它。
const hermesMemoryStoreRoot = "hermes-state"

// hermesMemoriesEntry 是记忆目录在存储与叠加层 home 内共用的名字——Hermes
// 相对 HERMES_HOME 解析它。
const hermesMemoriesEntry = "memories"

// HermesMemoryStorePath 返回 (daemonProfile, agentID, sourceHome) 的持久记忆
// 存储路径；记忆必须保持任务本地时返回 ""——没有 agent 可作键，或 Multica
// profile 目录无法解析。daemon 会在任务期间把返回的路径标记为使用中，因此
// PruneHermesMemoryStores 绝不会在挂载中途回收它。
//
// daemonProfile 白送了按 Multica profile 命名空间的隔离：profile 目录本就互不
// 相交，所以暂存 daemon 的 GC 永远看不到生产 daemon 的存储，也不需要哈希命名
// 空间段（不同于住在共享 ~/.codex 下的 Codex 存储）。
func HermesMemoryStorePath(daemonProfile, agentID, sourceHome string) string {
	agent := sanitizePathSegment(agentID)
	if agent == "" {
		return ""
	}
	profileDir, err := cli.ProfileDir(daemonProfile)
	if err != nil {
		return ""
	}
	return filepath.Join(profileDir, hermesMemoryStoreRoot, agent, hermesMemoryProfileSegment(sourceHome))
}

// hermesMemoryProfileSegment 把解析后的 Hermes 源 home 映射为标识它的存储段。
// 具名 profile 与默认 home 保留可读名字，让运维（以及文档记载的一次性导入）
// 能找到目录；自定义的树外 HERMES_HOME 则回退为其路径的哈希——不透明，但不会
// 与"不同位置的同名 home"撞车。
func hermesMemoryProfileSegment(sourceHome string) string {
	home := strings.TrimSpace(sourceHome)
	if home == "" {
		return "default"
	}
	if abs, err := filepath.Abs(home); err == nil {
		home = abs
	}
	home = filepath.Clean(home)
	nativeRoot := filepath.Clean(platformDefaultHermesHome())
	if home == nativeRoot {
		return "default"
	}
	// <root>/profiles/<name> —— 即 ResolveHermesProfile 对显式 -p/--profile、
	// 粘性的 active_profile 或 profile 级 custom_env HERMES_HOME 生成的形态。
	// 原生根的 profile 保留裸名；其他根的 profile 把根哈希进名字，于是
	// `<a>/profiles/x` 与 `<b>/profiles/x` 不会共享一条记忆线。
	if root := filepath.Dir(filepath.Dir(home)); filepath.Base(filepath.Dir(home)) == "profiles" {
		if name := sanitizePathSegment(filepath.Base(home)); name != "" {
			if root == nativeRoot {
				return name
			}
			return name + "_" + hashHermesHomePath(root)
		}
	}
	return "h_" + hashHermesHomePath(home)
}

// hashHermesHomePath 返回一个绝对 home 路径的短小、文件系统安全的摘要——
// 足够把不同位置的两个同名 home 区分开，又不至于把目录段顶到 255 字节的
// 文件名上限。
func hashHermesHomePath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

// mountHermesMemories 把叠加层的 memories/ 指向 storeDir，使 agent 的记忆活
// 得比任务更久。对 Reuse 幂等：已指向存储的链接原样保留。
//
// 旧 daemon（或在一个没有可作键存储的任务里跑过）留下的真实 memories/ 目录
// 会被迁移进存储而非丢弃——但只迁进空存储，因此升级绝不会覆盖 agent 已积累
// 的记忆。无法完成的迁移会让叠加层失败，而不是丢掉那份没能搬过去的目录。
func mountHermesMemories(hermesHome, storeDir string, logger *slog.Logger) error {
	dst := filepath.Join(hermesHome, hermesMemoriesEntry)

	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, rlErr := os.Readlink(dst); rlErr == nil && filepath.Clean(target) == filepath.Clean(storeDir) {
				if err := os.MkdirAll(storeDir, 0o700); err != nil {
					return fmt.Errorf("create hermes memory store %s: %w", storeDir, err)
				}
				touchHermesMemoryStore(storeDir, logger)
				return nil
			}
		} else if fi.IsDir() {
			// 迁移发生在存储目录创建之前，因此迁移能以一次原子 rename 发布
			// 一份完整复制的树。关闭即失败：下面的源目录只有在每个条目都安全
			// 进入存储后才会被删除。
			if err := migrateHermesTaskMemories(dst, storeDir, logger); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove stale memories path %s: %w", dst, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat memories path %s: %w", dst, err)
	}

	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return fmt.Errorf("create hermes memory store %s: %w", storeDir, err)
	}
	if err := createDirLink(storeDir, dst); err != nil {
		return fmt.Errorf("link memories to store %s: %w", storeDir, err)
	}
	// 给存储打上"刚用过"的戳：挂载本身不碰它的 mtime，不这样做的话 GC 的
	// 空闲检查可能正好在任务重新拾起一位久闲 agent 的记忆时把它回收掉。
	touchHermesMemoryStore(storeDir, logger)
	return nil
}

// migrateHermesTaskMemories 把既存任务本地 memories 目录的内容复制进空存储，
// 使 daemon 升级不会丢掉在途任务已经记住的东西。
//
// 调用方在本函数返回 nil 后立即删除源目录，所以 "nil" 只能意味着"源可以安全
// 删除"，别无其他。由此推出三条规则：
//
//   - 复制而非移动。移动更快，但跨文件系统会失败——workspaces 根与 Multica
//     profile 目录可能在不同卷上——而一次半截的移动会让数据两头都不在。
//   - 凡没搬过去的，绝不报成功。读任一个目录的 I/O 错误，或一个它不肯复制的
//     条目（软链、设备节点——原样复制的链接可能把存储指向它自身之外），都是
//     错误，而非静默跳过。
//   - 用暂存目录 + 一次原子 rename 发布，因此同一 agent 的两个任务若同时迁移，
//     不可能交错进一份半填充的存储。先写者赢；输家丢弃自己的暂存、采用赢家的
//     存储——这与"发现一份早已填充的存储"结果相同。
//
// 只迁入不存在或为空的存储，因此升级绝不会覆盖 agent 已积累的记忆。
func migrateHermesTaskMemories(taskDir, storeDir string, logger *slog.Logger) error {
	stored, err := os.ReadDir(storeDir)
	switch {
	case err == nil && len(stored) > 0:
		return nil // 存储已持有此 agent 的记忆——绝不覆盖
	case err != nil && !os.IsNotExist(err):
		// 若把这种情形当"无可迁移"，下面就会删除源。
		return fmt.Errorf("read hermes memory store %s: %w", storeDir, err)
	}

	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return fmt.Errorf("read task-local hermes memories %s: %w", taskDir, err)
	}
	if len(entries) == 0 {
		return nil
	}

	staging, err := newHermesStoreStaging(storeDir)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging) // 暂存目录一经晋升，这里就是 no-op

	for _, entry := range entries {
		src := filepath.Join(taskDir, entry.Name())
		if err := copyHermesMemoryEntry(src, filepath.Join(staging, entry.Name()), entry); err != nil {
			return fmt.Errorf("migrate task-local hermes memory %s into %s: %w", src, storeDir, err)
		}
	}

	promoted, err := promoteHermesStoreStaging(staging, storeDir, hermesStorePopulated)
	if err != nil {
		return err
	}
	if !promoted {
		logger.Info("execenv: another task populated the hermes memory store first; keeping it", "store", storeDir)
		return nil
	}
	logger.Info("execenv: migrated task-local hermes memories into agent store", "store", storeDir, "entries", len(entries))
	return nil
}

// newHermesStoreStaging 创建存储迁移要复制进去的暂存目录。它是存储的兄弟
// 目录，让晋升用的 rename 始终留在同一个文件系统内；带点前缀，让人一眼看出
// 它不是一份独立存储。记忆迁移（本文件）与会话数据库迁移（hermes_sessions.go）
// 共用它：两者都在复制报成功后删除源，所以都需要同一套"全有或全无"的发布。
// MkdirTemp 创建时已是 0700，与存储自身创建时所用模式相同，因此没有后续
// chmod 会失败并把它卡住。
func newHermesStoreStaging(storeDir string) (string, error) {
	parent := filepath.Dir(storeDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create hermes memory store parent %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(storeDir)+".migrating-")
	if err != nil {
		return "", fmt.Errorf("create hermes memory staging dir in %s: %w", parent, err)
	}
	return staging, nil
}

// promoteHermesStoreStaging 把一份完整复制的暂存目录发布为存储，并报告本次
// 调用是否胜出。空的存储目录（若有）先被删除：os.Remove 只在目录为空时成功，
// 因此已被另一任务填充的存储会让它变成 no-op，而 Windows 干脆拒绝把 rename
// 落到一个已存在的目录上。
//
// 输掉竞争不是错误——赢家的存储装着同一 agent 的状态——但必须被"肯定地确认"，
// 绝不能从任何失败里推断。调用方在 (false, nil) 时总会删除源目录，所以一次
// 权限错误、只读文件系统或 Windows 共享冲突必须关闭即失败，而不能冒充
// "别人已发布"。
//
// published 回答"是否已有竞争者在此发布了真实状态"，由调用方定义——因为两种
// 存储对"被占用的目录长什么样"看法不同：对记忆而言任何条目都算内容，而会话
// 存储可能只装着一份零长度数据库或不带记录的孤儿日志侧车。若共享同一个定义，
// 一次会话迁移可能在读存储时判定为空、复制进暂存，发布时又判为已占用并上报
// 竞争失败——之后调用方就会删掉一份从未被搬走的源数据库。
func promoteHermesStoreStaging(staging, storeDir string, published func(string) bool) (bool, error) {
	if err := os.Remove(storeDir); err != nil && !os.IsNotExist(err) {
		if published(storeDir) {
			return false, nil // 另一任务先发布了
		}
		return false, fmt.Errorf("clear empty hermes store %s before publishing: %w", storeDir, err)
	}
	if err := os.Rename(staging, storeDir); err != nil {
		if published(storeDir) {
			return false, nil // 在 remove 与 rename 之间输掉了一场极窄的竞争
		}
		return false, fmt.Errorf("publish hermes store %s: %w", storeDir, err)
	}
	return true, nil
}

// hermesStorePopulated 报告 storeDir 是否"被确认"持有条目。任何无法确认的
// 情形——目录不可读、路径不是目录——都返回 false，于是问"另一任务发布了吗"
// 的调用方会把不确定当作"没有"。
func hermesStorePopulated(storeDir string) bool {
	entries, err := os.ReadDir(storeDir)
	return err == nil && len(entries) > 0
}

// copyHermesMemoryEntry 把 memories 目录的单个条目复制进暂存树。任何不是
// 常规文件或目录的东西都被拒绝而非跳过：调用方成功后删除源，静默跳过就等于
// 静默丢失。
func copyHermesMemoryEntry(src, dst string, entry os.DirEntry) error {
	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", src, err)
	}
	switch {
	case entry.IsDir():
		if info.Mode().Perm()&0555 == 0 {
			return fmt.Errorf("directory %s has no read/execute permission", src)
		}
		return copyHermesMemoryTree(src, dst)
	case entry.Type().IsRegular():
		if info.Mode().Perm()&0444 == 0 {
			return fmt.Errorf("file %s has no read permission", src)
		}
		return copyFile(src, dst)
	default:
		return fmt.Errorf("refusing to migrate %s: unsupported entry type %s", src, entry.Type())
	}
}

// copyHermesMemoryTree 递归复制 memories 目录下的一个目录。它拒绝（而非静默
// 跳过）嵌套的软链与其他不规则文件：这份复制是源与其被删除之间唯一的屏障。
func copyHermesMemoryTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if info.Mode().Perm()&0555 == 0 {
				return fmt.Errorf("directory %s has no read/execute permission", path)
			}
			return os.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			if info.Mode().Perm()&0444 == 0 {
				return fmt.Errorf("file %s has no read permission", path)
			}
			return copyFile(path, target)
		default:
			return fmt.Errorf("refusing to migrate %s: unsupported entry type %s", path, d.Type())
		}
	})
}

// detachHermesMemories 给叠加层一份真实的、任务本地的 memories 目录。它用于
// "没有存储可作键"（无 agent，或 Multica profile 目录无法解析）的路径，因此
// 必须主动替换上次运行指向持久存储留下的链接：被复用的叠加层仍带着那个链接，
// 而 MkdirAll 会顺着链接静默成功，让任务写进一个 daemon 已不再守卫于 GC 的
// 存储。已存在的真实目录被保留——那是本任务自己的记忆。
func detachHermesMemories(hermesHome string) error {
	dst := filepath.Join(hermesHome, hermesMemoriesEntry)
	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 && fi.IsDir() {
			return nil
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("detach memories path %s: %w", dst, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat memories path %s: %w", dst, err)
	}
	return os.MkdirAll(dst, 0o700)
}

// touchHermesMemoryStore 刷新 storeDir 的 mtime——GC 把它读作"最近活动"信号。
// 尽力而为：一次失败的 touch 最多带来一次过于激进的清理，而 daemon 的
// active-store 守卫仍能阻止它。
func touchHermesMemoryStore(storeDir string, logger *slog.Logger) {
	now := time.Now()
	if err := os.Chtimes(storeDir, now, now); err != nil {
		logger.Warn("execenv: refresh hermes memory store activity failed", "store", storeDir, "error", err)
	}
}

// PruneHermesMemoryStores 回收 retention 内未被触碰的逐 agent Hermes 记忆
// 存储。这些存储活在任务 GC 会回收的任务级 envRoot 之外——这正是关键，记忆
// 必须比任务活得更久——因此不做本清理的话，已删除 agent 的记忆会永远躺在
// 磁盘上。retention <= 0 则整体禁用清理。
//
// 默认保留期刻意设得很长：与 Codex 的 rollout（GB 级记录）不同，这里只有寥寥
// 几个 markdown 文件，而回收一个 agent 的记忆是用户可见的失忆，不只是缓存未
// 命中。
//
// reserve（可为 nil）以与 PruneCodexSessionStores 完全相同的方式原子认领一个
// 待删存储：ok=false 表示有活任务持有该存储，于是留着它。nil 表示关闭守卫
// （测试用）。
func PruneHermesMemoryStores(daemonProfile string, retention time.Duration, now time.Time, reserve func(storeDir string) (commit func(), ok bool), logger *slog.Logger) (removed int, bytesFreed int64) {
	if retention <= 0 {
		return 0, 0
	}
	profileDir, err := cli.ProfileDir(daemonProfile)
	if err != nil {
		return 0, 0
	}
	root := filepath.Join(profileDir, hermesMemoryStoreRoot)
	agents, err := os.ReadDir(root)
	if err != nil {
		return 0, 0 // 还没建，或不可读——没什么可清理的
	}
	for _, a := range agents {
		if !a.IsDir() {
			continue
		}
		agentDir := filepath.Join(root, a.Name())
		profiles, err := os.ReadDir(agentDir)
		if err != nil {
			continue
		}
		kept := 0
		for _, p := range profiles {
			if !p.IsDir() {
				continue
			}
			storeDir := filepath.Join(agentDir, p.Name())
			newest, size := hermesMemoryStoreStat(storeDir)
			if newest.IsZero() || now.Sub(newest) <= retention {
				kept++
				continue
			}
			var commit func()
			if reserve != nil {
				c, ok := reserve(storeDir)
				if !ok {
					kept++
					continue
				}
				commit = c
			}
			err := os.RemoveAll(storeDir)
			if commit != nil {
				commit()
			}
			if err != nil {
				logger.Warn("execenv: prune hermes memory store failed", "store", storeDir, "error", err)
				kept++
				continue
			}
			removed++
			bytesFreed += size
		}
		// 最后一个 profile 存储没了就摘掉 agent 目录，避免树里留下空 <agent>/
		// 空壳。
		if kept == 0 {
			_ = os.Remove(agentDir)
		}
	}
	return removed, bytesFreed
}

// hermesMemoryStoreStat 遍历 dir 一次，返回其中"最新修改时间"（存储的最近
// 活动）与总字节大小（供 GC 记账）。
func hermesMemoryStoreStat(dir string) (newest time.Time, size int64) {
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if !d.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return newest, size
}
