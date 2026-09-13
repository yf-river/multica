// plugin_event_dispatch.go —— 事件触发的插件 hook 分发器：
// 把领域事件扇出（fan-out）给订阅了它的 hook，负责背压、熔断/重试/限流的记录表
// 维护、以及把“宿主被插件拖住”这类问题从结构上排除。
package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// 事件触发式 hook。
//
// 这个文件存在的意义就是守住一条规则：事件 hook 绝不阻塞宿主。事件总线是同步的——
// Bus.Publish 在“产生该事件的请求”所在的 goroutine 上、内联地跑它的监听器——所以
// 一个去拨第三方端点的监听器，就等于把一个外部服务器放进了“建 issue”的关键路径。
// 因此这里的一切都交给有界的 worker 池处理、立刻返回。
//
// 同样的道理也解释了 agent 执行路径上为何完全没有 hook：任何必须在 agent 每轮前后
// 运行的 hook，都等于第三方握着产品主循环不撒手。agent 在 PR 4 里是通过“把调用
// hook 当作一个工具”来触达它们的，而工具调用是可以拒绝的。

const (
	// 队列深度。满了说明事件来得比端点应答快；溢出部分直接丢弃并计数，而不是无限
	// 排队——无界队列会把一个慢插件变成内存泄漏。
	dispatchQueueDepth = 512
	dispatchWorkers    = 4

	// hook 调用记录保留多久。这张表是运维遥测，不是历史归档：它回答的是“这个端点
	// 现在为什么在失败”，熔断器和限流器也只往回看几分钟。永久保留只会让一张只追加
	// 的表按“每个 hook 每分钟 120 次”的速度无限长大，还没有任何读者。
	invocationRetention  = 7 * 24 * time.Hour
	invocationSweepEvery = time.Hour
)

// PluginEventDispatcher 把领域事件扇出给订阅了它们的 hook。
type PluginEventDispatcher struct {
	service *PluginService
	queue   chan dispatchJob
	wg      sync.WaitGroup
	stop    chan struct{}
	once    sync.Once

	// dropped 统计在背压下被丢弃的事件数，供排查时看。
	mu      sync.Mutex
	dropped int
}

// dispatchJob 是排队的最小工作单元：一次“把某个事件投给某个 hook”的投递任务。
type dispatchJob struct {
	installation db.PluginInstallation
	hook         plugincontract.Hook
	eventType    string
	issueID      pgtype.UUID
	payload      any
}

// NewPluginEventDispatcher 建好队列与 worker：dispatchWorkers 个投递 worker 负责
// 处理事件，外加一个清扫 goroutine 定期清理过期的调用记录。
func NewPluginEventDispatcher(service *PluginService) *PluginEventDispatcher {
	dispatcher := &PluginEventDispatcher{
		service: service,
		queue:   make(chan dispatchJob, dispatchQueueDepth),
		stop:    make(chan struct{}),
	}
	for i := 0; i < dispatchWorkers; i++ {
		dispatcher.wg.Add(1)
		go dispatcher.work()
	}
	dispatcher.wg.Add(1)
	go dispatcher.sweepInvocations()
	return dispatcher
}

// sweepInvocations 让“这张表会按 TTL 清扫”的说法成真。
//
// 没有任何读者会去看比熔断窗口和限流窗口还老的行——两者都只往回看几分钟——所以
// 不清扫的话，这张表只会按每个 hook 每分钟最多 120 行的速度永远长下去。
//
// 清扫挂在分发器自己的生命周期上，因为它们是同一件事：给“由第三方行为驱动”的东西
// 配上有界资源。
//
// 第一趟清扫等第一次 tick 而不是构造时就跑。早期版本确实在构造后立刻清一次，理由是
// “下线一周的部署不该再等一小时”——结果在 cmd/server 的 router 测试里 panic 了：
// 那里分发器是架在一个连接池从未打开的 Queries 上构建的。当时换来的好处只是冷启动
// 时少保留一小时（对照 7 天 TTL），代价却是一个 goroutine 在“进程是否真有数据库”
// 还没底的时候就去摸库。
func (d *PluginEventDispatcher) sweepInvocations() {
	defer d.wg.Done()
	ticker := time.NewTicker(invocationSweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.sweepOnce()
		}
	}
}

// sweepOnce 删除已过期的记录。
//
// panic 必须就地兜住，理由和 runGuarded 一样：这跑在裸 goroutine 上，未捕获的
// panic 不是“一次失败的清扫”，而是“进程死掉”。这里只判 Queries 是否为 nil 根本
// 挡不住——sqlc 的 Queries 包着一个执行器，所以一个“架在未打开连接池上”的非 nil
// Queries 能通过此处所有检查，然后在 pgxpool 内部解引用时炸掉。cmd/server 的测试
// 正是这么被拖垮的，这也是为什么用 recover 兜底而不是再多一个 nil 判断。
func (d *PluginEventDispatcher) sweepOnce() {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("plugins: panic while sweeping hook invocations", "recovered", recovered)
		}
	}()
	if d.service == nil || d.service.Queries == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-invocationRetention), Valid: true}
	removed, err := d.service.Queries.DeleteExpiredPluginInvocations(ctx, cutoff)
	if err != nil {
		slog.Warn("plugins: invocation sweep failed", "error", err)
		return
	}
	if removed > 0 {
		slog.Info("plugins: swept expired hook invocations", "removed", removed)
	}
}

// Dispatch 是总线监听器要调用的入口。它必须尽快返回：调用方是一个还活着的请求，
// 正事已经干完了。
//
// 注意它不从这个请求拿 context。若把外发的 hook 绑在触发它的请求上，浏览器一拿到
// 响应 hook 就被取消——而那一刻恰恰是 hook 才刚刚开始的时候。
func (d *PluginEventDispatcher) Dispatch(eventType, workspaceID string, payload any) {
	if d == nil || d.service == nil || workspaceID == "" {
		return
	}
	parsedWorkspace, err := parseUUIDValue(workspaceID)
	if err != nil {
		return
	}

	// 这里什么实际工作都不做。查安装是数据库读、找 issue id 是 JSON 往返，两件都
	// 该放在 worker 上；而且都排在 feature flag 检查之后，插件关掉的部署这两件事
	// 一件都不用付。
	select {
	case d.queue <- dispatchJob{
		installation: db.PluginInstallation{WorkspaceID: parsedWorkspace},
		eventType:    eventType,
		payload:      payload,
	}:
	default:
		d.mu.Lock()
		d.dropped++
		dropped := d.dropped
		d.mu.Unlock()
		slog.Warn("plugins: event dispatch queue full, dropping event", "event_type", eventType, "dropped_total", dropped)
	}
}

// Dropped 返回在背压下被丢弃了多少事件。
func (d *PluginEventDispatcher) Dropped() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

func (d *PluginEventDispatcher) work() {
	defer d.wg.Done()
	for {
		select {
		case <-d.stop:
			return
		case job := <-d.queue:
			d.runGuarded(job)
		}
	}
}

// runGuarded 把 panic 限制在引发它的那一次投递内。
//
// Bus.Publish 会 recover 它监听器里的 panic，好让一个坏 handler 拖不垮发布事件的
// 那个请求。把投递搬到 worker goroutine 上就脱离了这层保护，而裸 goroutine 上的
// panic 不是“一次失败的 hook”，是“进程死掉”。把保护找回来，是这次“搬离”该付的
// 代价，不是可选项。
func (d *PluginEventDispatcher) runGuarded(job dispatchJob) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("plugins: panic while delivering an event hook",
				"event_type", job.eventType, "recovered", recovered)
		}
	}()
	d.run(job)
}

// run 解析出哪些 hook 想要这个事件，然后逐个调用。
func (d *PluginEventDispatcher) run(job dispatchJob) {
	if d.service.Queries == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// flag 也把这条路径卡住，而且“在投递这一刻检查”而非“订阅时检查”才是重点：
	// 一个装了插件之后才把插件关掉的部署，必须立刻停掉对外调用，而不是只藏起 UI。
	// 每次投递都读一次，意味着开关立即生效，不用等下次重启。
	//
	// 它还让“flag 关着”时的成本归零。没有这一层，每个被分发的事件都得先跑一遍
	// ListWorkspacePluginInstallations 查询、才发现根本没东西可调。
	if !featureflags.PluginsV1Enabled(ctx, d.service.FeatureFlags) {
		return
	}

	// 过了 flag 才做：issue id 是用来收窄回调授权的，而拿到它需要解析 payload。
	job.issueID = issueIDFromPayload(job.payload)

	installations, err := d.service.Queries.ListWorkspacePluginInstallations(ctx, job.installation.WorkspaceID)
	if err != nil {
		slog.Warn("plugins: event dispatch could not list installations", "error", err)
		return
	}
	for _, installation := range installations {
		if !installation.Enabled {
			continue
		}
		manifest, err := ParseInstallationManifest(installation)
		if err != nil {
			continue
		}
		for _, hook := range manifest.Contributes.Hooks {
			if !HookAllowsTrigger(hook, plugincontract.TriggerEvent) || !hookWantsEvent(hook, job.eventType) {
				continue
			}
			d.deliver(ctx, installation, hook, job)
		}
	}
}

// hookWantsEvent 判断该 hook 声明监听的事件里是否包含 eventType。
func hookWantsEvent(hook plugincontract.Hook, eventType string) bool {
	for _, declared := range hook.Events {
		if declared == eventType {
			return true
		}
	}
	return false
}

// deliver 按事件重试节奏跑一次 hook。
func (d *PluginEventDispatcher) deliver(ctx context.Context, installation db.PluginInstallation, hook plugincontract.Hook, job dispatchJob) {
	// 端点一直失败的 hook 不会每个事件都重试。没有这层保护，一个宕了一小时的端点
	// 会为工作区里的每个事件都收到一次注定失败的通告，永无止境。
	if d.service.HookBreakerOpen(ctx, installation.ID, hook.Key) {
		slog.Info("plugins: hook circuit open, skipping event", "hook", hook.Key, "event_type", job.eventType)
		return
	}

	invocation := HookInvocation{
		Installation: installation,
		Hook:         hook,
		Trigger:      plugincontract.TriggerEvent,
		EventType:    job.eventType,
		// 事件背后没有具体的人。它产生的写入都是插件自己的行为，归到安装头上。
		Actor:   HookActor{Type: "plugin", ID: installation.ID},
		IssueID: job.issueID,
		Input:   job.payload,
	}

	for attempt := 1; attempt <= hookEventAttempts; attempt++ {
		_, err := d.service.InvokeHook(ctx, invocation, attempt)
		if err == nil {
			return
		}
		// 被拒是一种“决定”而不是“故障”：对一个被禁用、越权或限流的 hook 重试，
		// 只是白烧预算。
		if hookFailureStatus(err) == "refused" {
			slog.Info("plugins: event hook refused", "hook", hook.Key, "error", redactHookError(err))
			return
		}
		if attempt == hookEventAttempts {
			slog.Warn("plugins: event hook failed after retries", "hook", hook.Key, "event_type", job.eventType, "error", redactHookError(err))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt) * hookEventBackoff):
		}
	}
}

// Close 停掉 worker。可安全地多次调用。
func (d *PluginEventDispatcher) Close() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		close(d.stop)
		d.wg.Wait()
	})
}
