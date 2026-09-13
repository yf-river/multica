// channel_media_reconciler.go —— 频道媒体“意图账本”的清扫器（reconciler）：
// 每次上传前会先在账本里记一行，成功绑定附件的事务里再把该行清掉；凡是被遗留的
// 行（上传报错、resolve 超时、绑定失败、事务结果不明、进程崩溃等），都由这个独立
// worker 认领并收拾——先查附件是否真的被引用，没被引用就从对象存储里删掉。
package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 意图账本清扫器的各项参数是写死的常量而不是配置项：要不要调，看清扫器的指标
// 说话。
//
// 状态机管什么、时间表管什么，各管各的：
//
//   - 绑定 vs 删除 靠“状态”来隔离。一行一旦被认领置为 'deleting'，新的上传和
//     BindMediaRefs 都无法再复活/挂接该 key，所以认领后再查引用，不会和迟到的
//     COMMIT 竞态，完全不依赖时序假设。
//   - 客户端已经放弃的 PUT，跟 DELETE 之间根本排不出先后：存储端完全可能在删除
//     完成后才真正落对象。这一层靠“墓碑时间表”隔离，而不是赌单次时序：删除一个
//     未被引用的对象后，账本行保留，对象按逐渐拉长的间隔反复删除，迟到的物化
//     会被后面的趟次回收。想逃掉，除非对象在整个时间表走完后才出现
//     （见 channelMediaTombstoneRedelete）。每趟都会先复查引用：回收孤儿永远不能
//     排在“保留一个仍被持久读取的对象”前面。
//
// 因此，settle 延迟对“绑定/提交”的正确性不承担任何责任，它只决定清扫器等多久才
// 把 pending 行当作“被放弃了”；测试里铁打不变式是 settle 远大于流水线上任何
// 媒体/HTTP/DB 的时间预算（见 cmd/server 的 channel media invariant 测试）。
//
// 下面每个截止时间都是交给 SQL 的“时长”，而不是在这里算好的时间戳：Postgres 用
// 自己的 now() 推导 settle 截止、租约到期、退避和重复删除时刻，所以时钟漂移的副本
// 也会读到同一把钟。若在应用侧算时间戳，快的副本可能提前 settle 掉上传仍在途中的
// 行，或发出一把生下来就已过期的租约。
const (
	// ChannelMediaReconcileSettleDelay 是 pending 行必须“闲置”多久、清扫器才认为
	// 它被遗弃。导出是为了给不变式测试用。
	ChannelMediaReconcileSettleDelay = 15 * time.Minute
	// channelMediaReconcileSweepInterval 控制清扫循环的节奏。
	channelMediaReconcileSweepInterval = time.Minute
	// channelMediaReconcileLease 限定一行被某 worker 认领后、其他副本要等多久
	// 才能重新接管（崩溃恢复场景）。
	channelMediaReconcileLease = 2 * time.Minute
	// channelMediaReconcileSweepLimit 限定一趟清扫最多做多少次 settle（也就限定了
	// 它产生的对象存储操作量）。行是一行一行认领、顺序 settle 的，所以早退的一行
	// 带 1 分钟退避、在一趟长清扫内就可能被再次认领——这个上限数的是 settle 次数
	// 而不是去重行数，只是防止一趟清扫失控跑飞。
	channelMediaReconcileSweepLimit = 50
	// 对象存储删除失败后的退避：base << (attempt-1)，封顶。
	channelMediaReconcileBackoffBase = time.Minute
	channelMediaReconcileBackoffCap  = time.Hour
	// channelMediaReconcileDeleteTimeout 限定“单次”对象存储 DELETE 的时长。SDK
	// 默认的 HTTP 客户端没有整请求超时，不设限的话一次黑洞连接会把顺序清扫循环
	// 永远卡死（单副本部署没有别的 worker 能接手）。保持远小于租约，让一次超时的
	// 删除能带着退避被释放，而不是等别的副本把行抢走。
	channelMediaReconcileDeleteTimeout = 30 * time.Second
)

// channelMediaTombstoneRedelete 是墓碑行的“渐宽重复删除”时间表，按下标对应行的
// tombstone_pass。被客户端放弃的 PUT 仍可能在删除完成后才把对象物化出来，因此表里
// 每一项都会再触发一次幂等删除；直到最后一项走完，行才被真正丢弃。总覆盖约 31 小时，
// 相比存储端只把被放弃请求保留几秒，余量足够。
var channelMediaTombstoneRedelete = []time.Duration{
	15 * time.Minute,
	time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

// MediaObjectDeleter 是清扫器需要的唯一存储能力——Delete，且错误要冒出来，
// 这样失败能进退避而不是被当成成功。*storage.S3Storage 和 *storage.LocalStorage
// 都满足它。
type MediaObjectDeleter interface {
	DeleteObject(ctx context.Context, key string) error
}

// ChannelMediaReconciler 收拾媒体意图账本：每次上传前会写一行，附件绑定事务里会
// 把行清掉。凡是被剩下的——上传错误、resolve 超时、绑定失败、结果不明的提交、
// 崩溃——都在这里被认领（持租约把行从 'pending' 翻成 'deleting'），并在认领之后
// 检查是否存在持久的附件引用（无竞态：被认领的 key 上绑定已不可能再成功），然后
// 要么保留（有引用）要么从对象存储里删掉。它以独立 worker 运行，因此对象存储的
// 延迟尖峰不会饿死其他任何清扫器。
type ChannelMediaReconciler struct {
	Queries *db.Queries
	Storage MediaObjectDeleter
	Logger  *slog.Logger
	Metrics *metrics.ChannelMediaReconcilerMetrics

	// deleteTimeout 可被覆盖，便于做确定性测试。
	deleteTimeout time.Duration
}

func (r *ChannelMediaReconciler) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// pgInterval 把时长带给 SQL，让 Postgres 自己算截止时刻。所有 settle/租约/重试
// 判定都对数据库时钟比较，所以本地时钟漂移的副本既不能提前 settle 行、也不能发出
// 一把天生过期的租约，更不会压缩墓碑时间表。
func pgInterval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

// Run 一直循环执行 RunOnce，直到 ctx 结束。由 cmd/server 以独立 goroutine 启动，
// 刻意不与其他任何清扫器的节奏耦合。
func (r *ChannelMediaReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(channelMediaReconcileSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce 逐行 settle 到期账本行：认领一行、处理一行、再认领下一行，直到本趟上限。
// 每行的认领都紧挨在它自己的处理之前，所以一行永远不会“空握着租约等活干”——它
// 的尝试次数和退避描述的都是真正试过的删除。所有错误都以行为单位、不致命：现在
// 处理不了的行先退避，等后面某趟再试（或租约过期后被别的副本接走）。
func (r *ChannelMediaReconciler) RunOnce(ctx context.Context) {
	if r.Storage == nil {
		// 装配逻辑只在有存储后端时才构建清扫器，但这里要是 panic，会从一个裸
		// goroutine 里拖垮整个进程——把不变式守住在本函数内。没有删除器就去认领，
		// 只会让行滞留在 'deleting' 直到租约过期，所以干脆一行都不认领。
		r.logger().Error("channel media reconciler: no storage backend; skipping sweep")
		return
	}
	for settles := 0; settles < channelMediaReconcileSweepLimit; settles++ {
		leaseToken := pgtype.UUID{Bytes: uuid.New(), Valid: true}
		row, err := r.Queries.ClaimNextChannelMediaPendingObjectForReconcile(ctx, db.ClaimNextChannelMediaPendingObjectForReconcileParams{
			LeaseToken:  leaseToken,
			Lease:       pgInterval(channelMediaReconcileLease),
			SettleDelay: pgInterval(ChannelMediaReconcileSettleDelay),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			// 关停会中途取消清扫，那是清扫正常收尾的方式，不是值得叫醒谁的故障。
			if ctx.Err() != nil {
				return
			}
			r.logger().Warn("channel media reconciler: claim failed", "error", err)
			break
		}
		r.settle(ctx, row, leaseToken)
	}
}

// settle 处理调用方刚认领到的这一行。它持有的租约只需覆盖这一行（删除超时远小于
// 租约，见不变式测试），所以不需要心跳：认领是刚刚发生的，下面每次写入都有租约
// token 把守，因此一行即使在过期后被别人重新认领，旧主人的写入也会被无视而不是
// 把它搞坏。
func (r *ChannelMediaReconciler) settle(ctx context.Context, row db.ChannelMediaPendingObject, leaseToken pgtype.UUID) {
	// 引用检查在认领已把行翻成 'deleting' 之后才做：从那一刻起 BindMediaRefs
	// 无法再挂上这个 key，所以“无引用”的答案是终局性的，不是快照竞态。墓碑轮次
	// 也照做：墓碑要重删的对象和附件要读的对象是同一个 key，跳过检查就会让一次
	// 重删亲手制造出这个账本存在的意义就是要防的“悬空附件”。
	referenced, err := r.Queries.ChannelMediaObjectIsReferenced(ctx, db.ChannelMediaObjectIsReferencedParams{
		ChatMessageID: row.ChatMessageID,
		WorkspaceID:   row.WorkspaceID,
		StorageUrl:    row.StorageUrl,
	})
	if err != nil {
		r.release(ctx, row, leaseToken, err)
		return
	}
	if referenced {
		if row.State == tombstonedState {
			// 按设计这里不可达——对象 key 是按（chat message, resource）绑定的，
			// 重新摄入会拿新 key，绑定也永远不会挂上一个已离开 'pending' 的 key。
			// 真走到这里，说明某个不变式被打破了；不变式被打破时安全的回答是
			// “保留对象 + 报异常”，绝不该去删一个正被附件读着的活对象。
			r.logger().Error("channel media reconciler: tombstoned object is referenced by an attachment; keeping it",
				"storage_key", row.StorageKey,
				"workspace_id", row.WorkspaceID,
				"chat_message_id", row.ChatMessageID,
				"storage_url", row.StorageUrl,
				"tombstone_pass", row.TombstonePass)
			if r.Metrics != nil {
				r.Metrics.TombstoneReferenced.Inc()
			}
		}
		// 绑定其实落地了（它的绑定事务输给了更早一次清扫认领——针对一条慢消息——
		// 的 pending 行竞争；或重投递的 intent 活得比绑定还久）。保留对象、清掉行。
		if r.clearRow(ctx, row, leaseToken) {
			r.logger().Info("channel media reconciler: kept referenced object",
				"storage_key", row.StorageKey,
				"workspace_id", row.WorkspaceID,
				"chat_message_id", row.ChatMessageID)
			if r.Metrics != nil {
				r.Metrics.RowsReferenced.Inc()
			}
		}
		return
	}
	// 未被引用：交给共用的“删除 + 墓碑收尾”流程。
	r.settleDeletedObject(ctx, row, leaseToken)
}

// settleDeletedObject 先删对象，然后要么把行置成墓碑等后面的重删趟次，要么（时间表
// 走完后）把行清掉。首次（未被引用的）settle 和每趟墓碑复查共用它。
func (r *ChannelMediaReconciler) settleDeletedObject(ctx context.Context, row db.ChannelMediaPendingObject, leaseToken pgtype.UUID) {
	// 删除在任何事务之外执行（做存储 I/O 时不占数据库连接/行锁），由租约把关、由
	// 自己的超时限定，一条卡死的连接就不会卡住整个顺序清扫。
	delTimeout := r.deleteTimeout
	if delTimeout == 0 {
		delTimeout = channelMediaReconcileDeleteTimeout
	}
	delCtx, delCancel := context.WithTimeout(ctx, delTimeout)
	err := r.Storage.DeleteObject(delCtx, row.StorageKey)
	delCancel()
	if err != nil {
		if r.Metrics != nil {
			r.Metrics.DeleteFailures.Inc()
		}
		r.release(ctx, row, leaseToken, err)
		return
	}
	// 删除成功了，但被放弃的 PUT 仍可能随后把这个对象物化出来。把行留成墓碑，按
	// 渐宽的时间表反复重删；直到时间表走完才丢弃。
	if next, idx, ok := r.nextTombstonePass(row); ok {
		if r.tombstoneRow(ctx, row, leaseToken, next, idx) {
			msg := "channel media reconciler: deleted unreferenced object; tombstoned for re-delete"
			if row.State == tombstonedState {
				// 重删趟次是幂等的：通常那里已经什么都没有了。
				msg = "channel media reconciler: tombstone re-delete pass done"
			}
			r.logger().Info(msg,
				"storage_key", row.StorageKey,
				"workspace_id", row.WorkspaceID,
				"chat_message_id", row.ChatMessageID,
				"attempt", row.Attempt,
				"tombstone_pass", idx,
				"next_redelete_in", next)
			if r.Metrics != nil && row.State != tombstonedState {
				// 只在“第一次真正删掉它”的那趟计数一次。
				r.Metrics.ObjectsDeleted.Inc()
			}
		}
		return
	}
	if r.clearRow(ctx, row, leaseToken) {
		r.logger().Info("channel media reconciler: tombstone schedule exhausted; ledger row cleared",
			"storage_key", row.StorageKey,
			"workspace_id", row.WorkspaceID,
			"chat_message_id", row.ChatMessageID,
			"attempt", row.Attempt)
	}
}

const tombstonedState = "tombstoned"

// nextTombstonePass 返回本行下一次重删前该等多久、以及要记录的时间表位置；当时间表
// 已走完、行可以丢弃时返回 ok=false。首次被删的行（状态 'deleting'）从下标 0 开始；
// 墓碑则从上次成功删除记录的 tombstone_pass 往后走——这样中间某次重删失败（只写了
// last_error/attempt）不会让进程从头再走一遍时间表。
func (r *ChannelMediaReconciler) nextTombstonePass(row db.ChannelMediaPendingObject) (delay time.Duration, idx int, ok bool) {
	idx = 0
	if row.State == tombstonedState {
		idx = int(row.TombstonePass) + 1
	}
	if idx >= len(channelMediaTombstoneRedelete) {
		return 0, 0, false
	}
	return channelMediaTombstoneRedelete[idx], idx, true
}

func (r *ChannelMediaReconciler) tombstoneRow(ctx context.Context, row db.ChannelMediaPendingObject, leaseToken pgtype.UUID, next time.Duration, idx int) bool {
	if ctx.Err() != nil {
		// 和 release 一样：已取消的上下文带不动这次写入，而关停不是故障。行保持被
		// 认领状态直到租约过期，下一趟会幂等地重删它。
		return false
	}
	n, err := r.Queries.TombstoneChannelMediaPendingObject(ctx, db.TombstoneChannelMediaPendingObjectParams{
		StorageKey:    row.StorageKey,
		WorkspaceID:   row.WorkspaceID,
		LeaseToken:    leaseToken,
		RedeleteDelay: pgInterval(next),
		TombstonePass: int32(idx),
	})
	if err != nil {
		r.logger().Warn("channel media reconciler: tombstone failed", "storage_key", row.StorageKey, "error", err)
		return false
	}
	return n > 0
}

// release 让行保持 'deleting'（绑定仍然永远挂不上它）、放下租约，并把下一次尝试
// 退避到更晚。
func (r *ChannelMediaReconciler) release(ctx context.Context, row db.ChannelMediaPendingObject, leaseToken pgtype.UUID, cause error) {
	if ctx.Err() != nil {
		// 关停取消了这次 settle 本身（在途的 DELETE 或查询）。退避写入在同一把已
		// 取消的上下文上也会失败，所以没什么可记、也不值得叫醒谁——租约过期自会
		// 像对待任何被中断的 worker 那样回收这行。
		return
	}
	backoff := channelMediaReconcileBackoffBase << min(row.Attempt-1, 10)
	if backoff > channelMediaReconcileBackoffCap || backoff <= 0 {
		backoff = channelMediaReconcileBackoffCap
	}
	r.logger().Warn("channel media reconciler: settle failed; backing off",
		"storage_key", row.StorageKey,
		"workspace_id", row.WorkspaceID,
		"attempt", row.Attempt,
		"backoff", backoff,
		"error", cause)
	if err := r.Queries.ReleaseChannelMediaPendingObject(ctx, db.ReleaseChannelMediaPendingObjectParams{
		StorageKey:  row.StorageKey,
		WorkspaceID: row.WorkspaceID,
		LeaseToken:  leaseToken,
		Backoff:     pgInterval(backoff),
		LastError:   pgtype.Text{String: cause.Error(), Valid: true},
	}); err != nil {
		// 无论这里成功与否，租约过期都会回收这行。
		r.logger().Warn("channel media reconciler: release failed", "storage_key", row.StorageKey, "error", err)
	}
}

func (r *ChannelMediaReconciler) clearRow(ctx context.Context, row db.ChannelMediaPendingObject, leaseToken pgtype.UUID) bool {
	if ctx.Err() != nil {
		// 见 release：settle 中途关停时把行留给租约过期处理，而不是去记录一次根本没
		// 机会落地的写入。
		return false
	}
	n, err := r.Queries.DeleteChannelMediaPendingObject(ctx, db.DeleteChannelMediaPendingObjectParams{
		StorageKey:  row.StorageKey,
		WorkspaceID: row.WorkspaceID,
		LeaseToken:  leaseToken,
	})
	if err != nil {
		r.logger().Warn("channel media reconciler: clear row failed", "storage_key", row.StorageKey, "error", err)
		return false
	}
	return n > 0
}
