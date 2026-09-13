// 背景
//
// Hermes 把每个 ACP 会话——即真实的对话记录——持久化在
// `<HERMES_HOME>/state.db` 的 SQLite 数据库里（acp_adapter/session.py）。
// 由于 Multica 必须接管 HERMES_HOME 才能注入绑定技能，那个数据库落进了
// "每任务叠加层"，导致 Hermes agent 的对话只能存活一个任务。
//
// 那次失败是静默的，而这正是它代价高昂的原因。对一份"不含该会话"的数据库
// 执行 `session/resume` 不会失败：Hermes 会新建一个会话并用一个普通的成功帧
// 应答（acp_adapter/server.py resume_session），于是 daemon 以为自己恢复成功，
// 在之后的每一轮都重发同一个已死的 id，用户看到的则是"每次重启都从零开始"
// 的对话，且日志里毫无线索（GH #6806）。后端现已能检测这种重新绑定
// （resolveHermesResumedSessionID），而本文件负责消除它的成因。
//
// 存储按 (agent, hermes profile, conversation) 分片：
//
//	<multica profile dir>/hermes-sessions/<agent>/<hermes profile>/<conversation>/state.db
//
// 按键粒度收到"会话"——即 issue，或聊天会话的 `chat_<id>`——正因如此，它才
// 能在 #6693 无法做到的地方安全共享。当初把 state.db 保持任务本地的理由是
// 并发：一个 agent 的并行任务会共写一个存活的 SQLite 数据库，那需要内存文件
// 不需要的加锁与一致性工作（外加 Windows 上对 `-shm` 的字节区间锁）。
// 同一会话的任务按构造是串行的——后续任务要等前一个报告完成后才被分发——
// 所以每个分片同一时刻只有一个写者；而两个不同 issue 各有各的数据库，永不相遇。
//
// (agent, profile) 前缀与记忆存储的划定范围完全一致，因此把 agent 指向另一个
// Hermes profile，会像给它一条全新的记忆线一样，给它一条全新的对话线。它作为
// `hermes-state/<agent>/<profile>` 的兄弟根而非子目录存在，因为那个目录本身就是
// agent 的记忆目录（叠加层把 `memories/` 直接链向它）——若把会话嵌进去，Hermes
// 会把它们当成一条记忆条目暴露出来。
//
// 叠加层通过在 `<store>/state.db` 处软链 `<overlay>/state.db` 来挂载分片。
// SQLite 在推导 `-wal` 与 `-shm` 路径时会解析该链接，因此日志侧车被创建在
// 存储里、紧挨数据库，而不是在任务目录里；任务拆除时绝不可能把预写日志一起
// 带走。

// 实现提醒：对话"记录"存在 store 里、通过软链挂进每任务 HERMES_HOME。
// 以下代码保证：无破坏性操作发生在链接（在暂存名下）真正建立之前；
// 链接失败时静默退化为"保持任务本地"，绝不丢弃/覆盖任何数据库。

package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// hermesSessionStoreRoot 是 daemon 的 Multica profile 目录下、承载每个 agent
// 持久化 Hermes 会话分片的目录。
const hermesSessionStoreRoot = "hermes-sessions"

// hermesSessionDBEntry 是存储与叠加层 home 内共用的数据库文件名——Hermes
// 相对 HERMES_HOME 解析它。
const hermesSessionDBEntry = "state.db"

// HermesSessionStorePath 返回 (daemonProfile, agentID, sourceHome, conversation)
// 的持久会话存储路径；在会话数据库必须保持任务本地时返回 ""——没有 agent
// 可作键、没有会话可作键（既非 issue 也非聊天会话）、或 Multica profile 目录
// 无法解析。daemon 会在任务期间把返回的路径标记为使用中，因此
// PruneHermesSessionStores 绝不会在挂载中途回收它。
func HermesSessionStorePath(daemonProfile, agentID, sourceHome string, task TaskContextForEnv) string {
	agent := sanitizePathSegment(agentID)
	if agent == "" {
		return ""
	}
	conversation := hermesConversationSegment(task)
	if conversation == "" {
		return ""
	}
	profileDir, err := cli.ProfileDir(daemonProfile)
	if err != nil {
		return ""
	}
	return filepath.Join(profileDir, hermesSessionStoreRoot, agent,
		hermesMemoryProfileSegment(sourceHome), conversation)
}

// hermesConversationSegment 把任务映射到它所属的会话：它所运行的 issue，或
// 没有 issue 时的聊天会话。与按 issue 划分的 Codex 会话存储（codexSessionStoreKey）
// 用同一种推导，让两个 provider 对"一次会话"的含义一致。对既非 issue 也非
// 聊天会话的任务返回 ""——其会话数据库保持任务本地，而不是造一个谁也不会
// 恢复的分片。
func hermesConversationSegment(task TaskContextForEnv) string {
	if issue := sanitizePathSegment(task.IssueID); issue != "" {
		return issue
	}
	if chat := sanitizePathSegment(task.ChatSessionID); chat != "" {
		return "chat_" + chat
	}
	return ""
}

// hermesSessionMount 描述一次挂载尝试实际达成了什么。
//
// 两个布尔字段刻意分开。Mounted 表示 state.db 现在解析到存储；
// HistoryPresent 表示存储里确实有一份可恢复的数据库。已挂载但为空的存储，
// 是"会话第一轮"的常态，也是 GC 回收、切换 profile 或运维 `rm` 之后留下的
// 形态——把"已挂载"当成"可恢复"，就会把一个会话 id 送进空数据库，那正是
// 本改动要消灭的静默重启。
type hermesSessionMount struct {
	Mounted        bool
	HistoryPresent bool
}

// hermesSessionLink 创建存储链接。做成可间接调用，好让测试模拟一个无法创建
// 软链的主机——降级路径是最不能毁掉数据库的路径，所以它需要在所有地方有
// 覆盖，而不只是在 Windows 上。
var hermesSessionLink = os.Symlink

// hermesSessionCopy 复制一次迁移的单个文件。做成可间接调用，好让测试在
// state.db 家族的第二份文件上失败，证明存储绝不会持有"迁移了一半的会话"。
var hermesSessionCopy = copyFile

// mountHermesSessionDB 把叠加层的 state.db 指向该会话的存储，使对话记录
// 活得比任务更久。
//
// 操作顺序就是契约。在链接（在暂存名下）真正建立之前，任何可能毁掉既有
// 数据库的事都不发生——因为另一个选项（先迁移、再删除、然后发现该主机不能
// 软链）恰恰会在降级路径本要保护的那些主机上，把一段可用的任务本地对话变成
// 空的。因此"Mounted=false 且 err=nil"意味着"什么都没碰，数据库保持任务
// 本地"，即改动前的既有行为。
//
// 对 Reuse 幂等：已指向存储的链接原样保留。旧 daemon——或本会话拥有存储之前
// 运行的任务——留下的真实 state.db 会被迁移进空的存储而不是被丢弃，因此在
// 对话中途升级不会丢掉用户正进行到一半的记录。
func mountHermesSessionDB(hermesHome, storeDir string, logger *slog.Logger) (hermesSessionMount, error) {
	dst := filepath.Join(hermesHome, hermesSessionDBEntry)
	target := filepath.Join(storeDir, hermesSessionDBEntry)

	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return hermesSessionMount{}, fmt.Errorf("create hermes session store %s: %w", storeDir, err)
	}

	fi, err := os.Lstat(dst)
	switch {
	case err == nil && fi.Mode()&os.ModeSymlink != 0:
		if current, rlErr := os.Readlink(dst); rlErr == nil && filepath.Clean(current) == filepath.Clean(target) {
			// 已经挂载（Reuse 路径）。链接仍可能是悬空的——上一轮之后 GC 可能
			// 已回收存储——所以"有无历史"的问题从存储回答，从不看链接。
			touchHermesSessionStore(storeDir, logger)
			return hermesSessionMount{Mounted: true, HistoryPresent: hermesStoreHasSessionDB(storeDir)}, nil
		}
	case err != nil && !os.IsNotExist(err):
		return hermesSessionMount{}, fmt.Errorf("stat hermes session db %s: %w", dst, err)
	}

	// 在任何破坏性操作前，先证明这台主机能建链接。必须是真软链、绝不是复制：
	// createFileLink 会在不能软链的 Windows 主机上退化为复制，而一份被复制的
	// SQLite 数据库会把本轮的每一次写入都吸收进"下个任务会扔掉"的文件里——
	// 比保持任务本地更糟，因为它看起来像是成功了。
	staged := filepath.Join(hermesHome, hermesSessionLinkStagingEntry)
	if err := os.RemoveAll(staged); err != nil {
		return hermesSessionMount{}, fmt.Errorf("clear stale session link staging %s: %w", staged, err)
	}
	if err := hermesSessionLink(target, staged); err != nil {
		logger.Warn("execenv: hermes session store not mounted; conversation history stays task-local",
			"store", storeDir,
			"error", err,
		)
		return hermesSessionMount{}, nil
	}
	defer os.RemoveAll(staged) // 暂存链接一旦 rename 到位，这里就是 no-op

	if err := migrateHermesTaskSessionDB(hermesHome, storeDir, logger); err != nil {
		return hermesSessionMount{}, err
	}
	if err := removeHermesSessionDBFamily(hermesHome); err != nil {
		return hermesSessionMount{}, err
	}
	// 发布链接。关闭即失败：到这一步数据库已安全待在存储里，因此 rename 失败
	// 说明文件系统坏了，而非历史丢失；任务绝不能对着一个根本没有 state.db
	// 路径的 home 启动。
	if err := os.Rename(staged, dst); err != nil {
		return hermesSessionMount{}, fmt.Errorf("publish hermes session link %s: %w", dst, err)
	}
	// 给存储打上"刚用过"的戳：挂载本身不碰它的 mtime，不这样做的话 GC 的空闲
	// 检查可能正好在任务重新拾起一段久闲会话时把它回收掉。
	touchHermesSessionStore(storeDir, logger)
	return hermesSessionMount{Mounted: true, HistoryPresent: hermesStoreHasSessionDB(storeDir)}, nil
}

// hermesSessionLinkStagingEntry 是存储链接在发布（覆盖 state.db）之前创建时
// 所在的名字。带点前缀，使 isHermesTaskLocalStateEntry 匹配不到它，家族清理
// 也就删不掉这个即将发布的链接。
const hermesSessionLinkStagingEntry = ".multica-session-link"

// hermesStoreHasSessionDB 报告 storeDir 是否持有一份有内容的会话数据库。
// 零长度文件是 SQLite 在一次从未写过页面的 `open` 之后留下的，对着它恢复
// 与对着不存在的数据库一样失忆——所以它算"无历史"，不算"有历史"。
func hermesStoreHasSessionDB(storeDir string) bool {
	fi, err := os.Stat(filepath.Join(storeDir, hermesSessionDBEntry))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// migrateHermesTaskSessionDB 把任务本地的 state.db 搬进空的存储，使 daemon
// 升级不会重启一段已在途中的对话。日志侧车要一起搬：数据库的任务若在写入
// 中途被杀，其最近几次提交还留在 `-wal` 里；只搬数据库会把它静默截断到上次
// 检查点。
//
// 与记忆存储的迁移一样，整个家族先复制进暂存目录、再以一次 rename 发布——
// 因为调用方在本函数返回 nil 后立即删除源。直接复制进存储会让"主数据库落地
// 之后才失败"留下一个看起来已迁移的存储：下次尝试会跳过迁移、删除源，把对话
// 未检查点化的尾部一起带走。
//
// 只迁入空存储——已持有数据库的存储是本会话的真实历史，绝不能被覆盖。
func migrateHermesTaskSessionDB(hermesHome, storeDir string, logger *slog.Logger) error {
	if hermesStoreHasSessionDB(storeDir) {
		return nil // 存储已持有本会话——绝不覆盖
	}

	entries, err := os.ReadDir(hermesHome)
	if err != nil {
		return fmt.Errorf("read overlay home %s: %w", hermesHome, err)
	}
	family := make([]string, 0, 3)
	for _, entry := range entries {
		if isHermesTaskLocalStateEntry(entry.Name()) && entry.Type().IsRegular() {
			family = append(family, entry.Name())
		}
	}
	if len(family) == 0 {
		return nil
	}

	staging, err := newHermesStoreStaging(storeDir)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging) // 暂存目录一经晋升，这里就是 no-op

	for _, name := range family {
		if err := hermesSessionCopy(filepath.Join(hermesHome, name), filepath.Join(staging, name)); err != nil {
			return fmt.Errorf("migrate task-local hermes session db %s into %s: %w", name, storeDir, err)
		}
	}

	promoted, err := publishHermesSessionStaging(staging, storeDir)
	if err != nil {
		return err
	}
	if !promoted {
		logger.Info("execenv: another task published this conversation's session store first; keeping it", "store", storeDir)
		return nil
	}
	logger.Info("execenv: migrated task-local hermes session db into conversation store",
		"store", storeDir,
		"files", len(family),
	)
	return nil
}

// publishHermesSessionStaging 把一份已完整复制的暂存目录发布为该会话的存储，
// 并报告本次调用是否胜出。
//
// 它存在，是因为这里"被占用"的含义与记忆存储不同。一份会话存储可能只装着
// 不带任何记录的 state.db 家族残骸——一次从未写页面的 `open` 留下的零长度
// 数据库，或被杀任务遗留的日志侧车——而上面的迁移已经判定它们不算历史。
// 通用 publish 会不认同：它把任何条目都读作竞争者的已发布状态、上报竞争失败，
// 调用方随后就会删掉一份从未被搬走的源数据库。所以先清掉残骸（按本文件处处
// 使用的定义，它们本来就什么都不是），再用迁移问过的同一个谓词来回答占用问题。
//
// 无法被清空的存储"不"上报为竞争失败：共享 promote 关闭即失败，迁移返回错误，
// 调用方保留源数据库。
//
// 检查、清理与 promote 作为一步持有 hermesSessionPublishMu。判定残骸什么
// 都不是、然后按路径名删除它们，只有在其间没有任何东西能替换它们时才成立：
// 不加锁的话，竞争者可能在本次检查之后发布一段真实记录，而清理会把赢家的
// 数据库当作残骸删掉——此时赢家已经上报成功并丢弃了自己的源。
func publishHermesSessionStaging(staging, storeDir string) (bool, error) {
	hermesSessionPublishMu.Lock()
	defer hermesSessionPublishMu.Unlock()
	if hermesStoreHasSessionDB(storeDir) {
		return false, nil // 竞争者先发布了一段真实记录
	}
	if hermesSessionPublishBarrier != nil {
		hermesSessionPublishBarrier(storeDir)
	}
	if err := removeHermesSessionDBFamily(storeDir); err != nil {
		return false, err
	}
	return promoteHermesStoreStaging(staging, storeDir, hermesStoreHasSessionDB)
}

// hermesSessionPublishMu 串行化本进程内的每一次会话存储发布。这里的进程内
// 互斥是"真刀真枪"而非近似：daemon 是存储树的唯一写者——任务、迁移乃至 GC
// 的保留守卫（Daemon.activeStores）都活在同一进程里——所以两个发布者只可能是
// 本 daemon 的两个 goroutine。用一个进程级互斥而非按存储的 map，是因为发布
// 每个会话至多发生一次（首次发现任务本地数据库的那次挂载），其临界区只是一
// 次 stat、几次 unlink 和一次 rename；竞争根本不值得为它记账。
var hermesSessionPublishMu sync.Mutex

// hermesSessionPublishBarrier 非 nil 时，在加锁区间内、占用检查与残骸清理之间
// 运行——那正是这把锁要合上的精确窗口。测试在这里停住一个发布者，以证明竞争
// 者无法把一次发布插入该窗口。生产环境恒为 nil。
var hermesSessionPublishBarrier func(storeDir string)

// removeHermesSessionDBFamily 清空一个目录下的 state.db 家族。在数据库要么已
// 安全进存储、要么正被链接有意替换之后调用；在链接旁留一份陈旧的 `-wal`，
// 只会是 SQLite 永不读取（侧车路径由已解析的存储路径推导）的死重，也是任务
// 目录里一个令人困惑的存在。
//
// 只删除本代码能识别的条目，且绝不递归：常规文件归 SQLite，软链是先前某次
// 挂载留下的存储链接（活叠加层下存储路径变了会留下，比如对话中途切换
// profile）——os.Remove 只摘链、不动目标。state.db 名下出现目录或任何其他
// 不规则类型，本代码不认作数据库残骸；删除无法识别的内容恰恰是本文件要防的
// 失败，所以关闭即失败，调用方保留其源。
func removeHermesSessionDBFamily(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, entry := range entries {
		if !isHermesTaskLocalStateEntry(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if t := entry.Type(); !t.IsRegular() && t&os.ModeSymlink == 0 {
			return fmt.Errorf("refusing to remove hermes session state %s: unexpected %s under a state.db name", path, t)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove task-local hermes session state %s: %w", path, err)
		}
	}
	return nil
}

// touchHermesSessionStore 刷新 storeDir 的 mtime——GC 把它读作"最近活动"信号。
// 尽力而为：一次失败的 touch 最多带来一次过于激进的清理，而 daemon 的
// active-store 守卫仍能阻止它。
func touchHermesSessionStore(storeDir string, logger *slog.Logger) {
	now := time.Now()
	if err := os.Chtimes(storeDir, now, now); err != nil {
		logger.Warn("execenv: refresh hermes session store activity failed", "store", storeDir, "error", err)
	}
}

// PruneHermesSessionStores 回收 retention 内未被触碰的逐会话 Hermes 会话存储。
// 它们活在任务 GC 会回收的任务级 envRoot 之外——这正是关键，记录必须比任务
// 活得更久——因此不做本清理的话，已结束 issue 的对话会永远躺在磁盘上，
// 删除 issue 或 agent 也不会把它带走。retention <= 0 则整体禁用清理。
//
// 保留期比记忆存储的短是刻意为之：这里装的是完整对话记录而非寥寥几篇
// markdown 笔记，丢掉一段空闲对话的重放，代价是"恢复时从新开始"（带一条
// 连续性说明），而不是 agent 忘掉它学到的东西。
//
// reserve（可为 nil）以与 PruneCodexSessionStores 完全相同的方式原子认领一个
// 待删存储：ok=false 表示有活任务持有该存储，于是留着它。nil 表示关闭守卫
// （测试用）。
func PruneHermesSessionStores(daemonProfile string, retention time.Duration, now time.Time, reserve func(storeDir string) (commit func(), ok bool), logger *slog.Logger) (removed int, bytesFreed int64) {
	if retention <= 0 {
		return 0, 0
	}
	profileDir, err := cli.ProfileDir(daemonProfile)
	if err != nil {
		return 0, 0
	}
	root := filepath.Join(profileDir, hermesSessionStoreRoot)
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
		keptProfiles := 0
		for _, p := range profiles {
			if !p.IsDir() {
				continue
			}
			profileStoreDir := filepath.Join(agentDir, p.Name())
			conversations, err := os.ReadDir(profileStoreDir)
			if err != nil {
				keptProfiles++
				continue
			}
			kept := 0
			for _, conv := range conversations {
				if !conv.IsDir() {
					continue
				}
				storeDir := filepath.Join(profileStoreDir, conv.Name())
				// 与记忆存储清理相同的"最新 mtime 即最近活动 + 总大小"遍历；
				// SQLite 每次提交都会刷新数据库的 mtime，因此持续进展的会话
				// 会让自己的分片保持新鲜。
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
					logger.Warn("execenv: prune hermes session store failed", "store", storeDir, "error", err)
					kept++
					continue
				}
				removed++
				bytesFreed += size
			}
			// 最后一个会话没了就摘掉 profile 目录，最后一个 profile 没了就摘掉
			// agent 目录，避免树里留下空壳。os.Remove 只在目录为空时成功，所以
			// 仍持有内容的目录会留住自己的父级。
			if kept > 0 {
				keptProfiles++
				continue
			}
			if err := os.Remove(profileStoreDir); err != nil {
				keptProfiles++
			}
		}
		if keptProfiles == 0 {
			_ = os.Remove(agentDir)
		}
	}
	return removed, bytesFreed
}
