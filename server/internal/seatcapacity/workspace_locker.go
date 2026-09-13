// workspace_locker.go 提供「跨 API 副本对同一 workspace 的容量(seat)调用做串行化」
// 的分布式锁：靠 PostgreSQL 的会话级 advisory lock 保证跨进程互斥，同时在本进程内用
// 一把「本地排队门闩」让同一进程的并发调用方先排队，避免每个等待者各钉一条池连接。
package seatcapacity

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// workspaceLockReleaseTimeout 是释放锁时给 pg_advisory_unlock 的超时。
const workspaceLockReleaseTimeout = 3 * time.Second

// defaultWorkspaceCooldown 是解锁前的冷却时长：故意把连接多留一会儿再放回池，避免
// 同一 workspace 的下一个调用方立刻抢到并造成无谓的锁乒乓。
const defaultWorkspaceCooldown = 75 * time.Millisecond

// workspaceLockPollInterval 是轮询式抢锁（pg_try_advisory_lock 失败）的间隔。
const workspaceLockPollInterval = 25 * time.Millisecond

// maxWorkspaceLockConnections 是容量调用最多能钉住的连接数上限。
const maxWorkspaceLockConnections = 4

// workspaceLockPoolFraction 表示容量锁最多占用连接池的几分之一（1/4）。
const workspaceLockPoolFraction = 4

// WorkspaceLocker 串行化对某 workspace 的容量调用。Lock 成功返回一条钉住的连接
// (db.DBTX) 与释放函数：在返回的连接上执行容量 SQL，用完调释放函数归还。
type WorkspaceLocker interface {
	Lock(context.Context, uuid.UUID) (db.DBTX, func(), error)
}

// postgresWorkspaceLocker 跨 API 副本串行化同一 workspace 的 Cloud 容量调用。它需要
// 「钉住」一条 PostgreSQL 会话连接，因为 advisory lock 是会话级的；若在解锁前把连接
// 还回池，锁就会泄漏给某个无关的池借用者。
type postgresWorkspaceLocker struct {
	pool *pgxpool.Pool

	// localLocks 做本进程内的排队（见 acquireLocal）。
	mu         sync.Mutex
	localLocks map[uuid.UUID]*localWorkspaceLock
	// slots 限制「被钉在 Cloud 容量调用上的连接」数量，防止容量流量把主连接池吃光。
	// 拿不到 slot 的新调用方在此等待（不占 DB 连接），slot 数上限为池的 1/4 且不超过 4。
	slots chan struct{}
}

// localWorkspaceLock 是某个 workspace 在本进程内的排队门闩：gate 容量为 1，表示
// 「当前是谁在持有进程内通行权」；refs 统计等待/持有该门闩的调用方数量。
type localWorkspaceLock struct {
	gate chan struct{}
	refs int
}

// NewWorkspaceLocker 构造一个分布式 workspace 锁。pool 为 nil 时返回 nil（调用方应把
// 它当作「无锁」处理）。
func NewWorkspaceLocker(pool *pgxpool.Pool) WorkspaceLocker {
	if pool == nil {
		return nil
	}
	slotCount := int(pool.Config().MaxConns / workspaceLockPoolFraction)
	if slotCount < 1 {
		slotCount = 1
	}
	if slotCount > maxWorkspaceLockConnections {
		slotCount = maxWorkspaceLockConnections
	}
	return &postgresWorkspaceLocker{
		pool:       pool,
		localLocks: make(map[uuid.UUID]*localWorkspaceLock),
		slots:      make(chan struct{}, slotCount),
	}
}

// Lock 为某 workspace 获取串行锁。先在本进程内排队，再拿一个「连接槽位」，最后用
// pg_try_advisory_lock 轮询抢 PostgreSQL 会话级锁。返回的连接是「已持锁」的连接，
// 调用方在其上执行容量 SQL；返回的释放函数负责解锁并把连接还回池。
func (l *postgresWorkspaceLocker) Lock(ctx context.Context, workspaceID uuid.UUID) (db.DBTX, func(), error) {
	releaseLocal, err := l.acquireLocal(ctx, workspaceID)
	if err != nil {
		return nil, nil, fmt.Errorf("queue workspace capacity lock: %w", err)
	}
	select {
	case l.slots <- struct{}{}:
	case <-ctx.Done():
		releaseLocal()
		return nil, nil, fmt.Errorf("queue workspace capacity lock connection: %w", ctx.Err())
	}
	acquired := false
	defer func() {
		if !acquired {
			<-l.slots
			releaseLocal()
		}
	}()

	key := workspaceAdvisoryLockKey(workspaceID)
	var conn *pgxpool.Conn
	for {
		conn, err = l.pool.Acquire(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("acquire workspace capacity lock connection: %w", err)
		}
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
			conn.Release()
			return nil, nil, fmt.Errorf("try workspace capacity lock: %w", err)
		}
		if locked {
			break
		}
		// 没抢到锁：把这条连接还回去（未持锁的连接绝不能留着），轮询一小段时间再试。
		conn.Release()
		conn = nil
		timer := time.NewTimer(workspaceLockPollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, nil, fmt.Errorf("wait for workspace capacity lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
	acquired = true
	var once sync.Once
	return conn, func() {
		once.Do(func() {
			defer releaseLocal()
			defer func() { <-l.slots }()
			// 冷却一小段时间再真正解锁，缓冲同一 workspace 连续容量调用的颠簸。
			timer := time.NewTimer(defaultWorkspaceCooldown)
			<-timer.C
			unlockCtx, cancel := context.WithTimeout(context.Background(), workspaceLockReleaseTimeout)
			defer cancel()
			var unlocked bool
			err := conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, key).Scan(&unlocked)
			if err != nil || !unlocked {
				// 显式解锁失败时，这条持有会话级锁的连接绝不能还回池。把这条被劫持的
				// 连接直接关掉，PostgreSQL 侧锁随之释放，也让下一个池借用者不被污染。
				_ = conn.Hijack().Close(unlockCtx)
				return
			}
			conn.Release()
		})
	}, nil
}

// acquireLocal 让同进程的调用方先在 Go 里排队，而不是每个人都在别人持有 workspace
// 锁期间各钉一条池连接。每进程只有一个调用方真正去 PostgreSQL 轮询，锁的协调仍
// 跨 pod 生效。
func (l *postgresWorkspaceLocker) acquireLocal(ctx context.Context, workspaceID uuid.UUID) (func(), error) {
	l.mu.Lock()
	local := l.localLocks[workspaceID]
	if local == nil {
		local = &localWorkspaceLock{gate: make(chan struct{}, 1)}
		local.gate <- struct{}{}
		l.localLocks[workspaceID] = local
	}
	local.refs++
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.dropLocalRef(workspaceID, local)
		return nil, ctx.Err()
	case <-local.gate:
		// 拿到进程内通行权后再确认 context 是否已取消（取消的话把通行权交回去）。
		if err := ctx.Err(); err != nil {
			local.gate <- struct{}{}
			l.dropLocalRef(workspaceID, local)
			return nil, err
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			local.gate <- struct{}{}
			l.dropLocalRef(workspaceID, local)
		})
	}, nil
}

// dropLocalRef 释放本地引用计数；归零时把该 workspace 的本地门闩整个删掉。
func (l *postgresWorkspaceLocker) dropLocalRef(workspaceID uuid.UUID, local *localWorkspaceLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	local.refs--
	if local.refs == 0 && l.localLocks[workspaceID] == local {
		delete(l.localLocks, workspaceID)
	}
}

// workspaceAdvisoryLockKey 把 workspace id 稳定哈希成一个 int64 作为 advisory lock key。
// 加固定前缀避免与其它使用 advisory lock 的功能撞 key。
func workspaceAdvisoryLockKey(workspaceID uuid.UUID) int64 {
	sum := sha256.Sum256(append([]byte("multica-seat-capacity:"), workspaceID[:]...))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}
