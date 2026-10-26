package daemon

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// agentDiscoveryInterval 是运行中的 daemon 重新检查“装了哪些 agent CLI”的频率。一轮
// 只是几次 exec.LookPath 调用——登录 shell 兜底由长得多的 shellResolveTTL 单独限速——
// 因此它可以短到“装个 CLI 立刻就有感觉”。测试可覆盖。
var agentDiscoveryInterval = 2 * time.Minute

// agentConvergeMaxBackoff 给“某个发现的 provider 一直注册失败”（永久低于最低支持
// 版本、--version 永远不成功的 CLI、服务器一直拒绝注册）的重试延迟封顶。发现本身保持
// agentDiscoveryInterval；只有贵的那半——版本探测加每个 workspace 一次注册——做退避，
// 于是卡住的 provider 不会变成忙循环。测试可覆盖。
var agentConvergeMaxBackoff = 30 * time.Minute

// agentVersionRefreshInterval 是运行中的 daemon 重新探测“每个已注册 agent CLI 的
// 版本”的频率，让 codex/claude 的原地升级不用重启就能被感知。一轮是每装一个 CLI 就
// fork 一次 `--version`，扇出且是机器级——不随 workspace 数或 runtime 数扩展。测试可
// 覆盖。
//
// 这是唯一“成本随宿主机增长”的本地探测（每装一个 CLI 一次 fork），而且执行的是第三
// 方二进制——其中一些包装器运行时有可见副作用——所以如果要压低后台探测成本，值得拉长
// 的就是它。
//
// 但它没有比 selfReloadCheckInterval 拉得更长：这一轮做的远不止刷新展示用的版本串——
// 它还驱动“版本敏感”的启动策略，并且正是它确认某个 CLI 已跌破最低支持版本、必须停止
// 派活。因此这个间隔同时也是“一个不支持的 CLI 还在认领任务”的窗口——这就是为什么它
// 跟着重载检查走，而不是单凭成本理由被推得更远。
var agentVersionRefreshInterval = 10 * time.Minute

// agentDiscoveryLoop 让已注册的 runtime 集合收敛到“这台机器实际装的 agent CLI”，
// 这样 daemon 运行期间新装的 CLI 不用重启就能上线（MUL-5439）。
//
// 每个 tick 无条件做便宜的那半：重探可用性并发布任何新增。贵的那半（版本探测 + 注册）
// 只在“某个发现的 provider 还没有为每个受管 workspace 都注册上 runtime”时跑——它由
// 实时状态推导，而不是记着上一轮发现的结果。这正是“首次尝试失败会重试”的原因：版本
// 探测超时、或注册调用失败的 provider，下一 tick 仍是“缺失”，会再试一次。这也意味着
// “因低于最低版本而被拒”的 provider 在原地升级后能自行恢复。
//
// 第二个更慢的 ticker 保持已注册 provider 的版本新鲜（refreshAgentVersions）——收敛
// 那半只盯着*runtime 缺失*的 provider，没有它，一次原地 CLI 升级会一直不可见直到
// daemon 重启。
func (d *Daemon) agentDiscoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(agentDiscoveryInterval)
	defer ticker.Stop()
	versionTicker := time.NewTicker(agentVersionRefreshInterval)
	defer versionTicker.Stop()

	var (
		backoff   time.Duration
		nextRetry time.Time
		// lastMismatch is the DSH profile/runtime disagreement the previous tick
		// saw, for the one direction that is throttled to a single forced round.
		lastMismatch dshProfileMismatch
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-versionTicker.C:
			d.refreshAgentVersions(ctx)
		case now := <-ticker.C:
			if entry, ok := d.cfg.Agents["dsh"]; ok {
				if version := d.agentVersion("dsh"); version != "" {
					d.prepareManagedDsh(entry.Path, version)
				}
			}
			// Forced by disagreement between two live states, not by a change
			// since the last look: a profile removed between two observations,
			// or before the first one — which is what a restart produces —
			// would otherwise never be noticed. Observed in practice: a profile
			// removed a minute after startup stayed live for three minutes and
			// the server routed a chat task straight into it.
			mismatch := d.dshRuntimeProfileMismatch()
			force := mismatch.forcesEveryTick() || (mismatch != dshMismatchNone && mismatch != lastMismatch)
			lastMismatch = mismatch
			d.convergeAgentRuntimes(ctx, &backoff, &nextRetry, now, force)
		case <-d.agentDiscoveryKick:
			// A local state change (the DSH runtime profile appearing) makes a
			// provider registrable right now. Wait for nothing: the scheduled
			// attempt this replaces can be agentConvergeMaxBackoff away.
			d.convergeAgentRuntimes(ctx, &backoff, &nextRetry, time.Now(), true)
		}
	}
}

// convergeAgentRuntimes runs the expensive half of a discovery round: publish
// anything the cheap probe gained, then version-probe and register whatever is
// still missing a runtime.
//
// force ignores the backoff earned by previous failures, for a caller that
// knows the reason a provider could not register has just gone away — and, in
// the other direction, for one that knows a registered provider may just have
// gone bad. backoff and nextRetry are the loop's scheduling state, threaded
// through rather than owned here so the ticker and the kick share one backoff.
func (d *Daemon) convergeAgentRuntimes(
	ctx context.Context,
	backoff *time.Duration,
	nextRetry *time.Time,
	now time.Time,
	force bool,
) {
	gained := d.refreshAgentAvailability()
	missing := d.providersMissingRuntimes()
	// A forced round runs even with nothing missing: that is the shape of "a
	// registered provider just became unusable", which is exactly what the
	// verdict layer needs a round to notice.
	if len(missing) == 0 && !force {
		*backoff = 0
		return
	}
	// A newly discovered provider always gets an immediate attempt; otherwise
	// honor the backoff earned by previous failures unless the caller knows
	// the blocker is gone.
	if len(gained) == 0 && !force && now.Before(*nextRetry) {
		return
	}
	before := len(missing)
	d.convergeRuntimeRegistrations(ctx)
	after := len(d.providersMissingRuntimes())
	// backoff is earned by registration that keeps failing. A round that leaves
	// nothing missing has nothing to back off from — including a forced round
	// that only came here to condemn a registered provider, where before and
	// after are both zero.
	if after == 0 || after < before {
		*backoff = 0
	} else {
		*backoff = nextConvergeBackoff(*backoff)
	}
	*nextRetry = now.Add(*backoff)
}

// dshOnlyVerdicts narrows a round's demotable verdicts to dsh.
//
// Returns nil rather than an empty map when there is nothing, so the caller's
// length check reads the same either way.
func dshOnlyVerdicts(demotable map[string]runtimeVerdict) map[string]runtimeVerdict {
	verdict, ok := demotable["dsh"]
	if !ok {
		return nil
	}
	return map[string]runtimeVerdict{"dsh": verdict}
}

// dshProfileMismatch names a disagreement between the two states the daemon has
// to keep aligned: whether the Multica runtime profile is installed, and whether
// a dsh runtime is registered.
//
// The kind, rather than a bare bool, is what the caller needs, because the two
// directions have opposite termination behaviour — see forcesEveryTick.
type dshProfileMismatch string

const (
	// dshMismatchNone: the two states agree.
	dshMismatchNone dshProfileMismatch = ""
	// dshMismatchRuntimeWithoutProfile is a live runtime whose CLI can no
	// longer start. It is still "available" — discovered, and holding a
	// runtime — so nothing else in the loop looks at it, and the server keeps
	// routing work into a CLI that cannot start.
	dshMismatchRuntimeWithoutProfile dshProfileMismatch = "runtime-without-profile"
	// dshMismatchProfileWithoutRuntime is the mirror image, and is what a
	// manual `dsh plugin --profile multica add` produces.
	dshMismatchProfileWithoutRuntime dshProfileMismatch = "profile-without-runtime"
)

// forcesEveryTick reports whether this mismatch must force a converge round on
// every tick rather than only on the tick that first observed it.
//
// The asymmetry is about which direction a round is guaranteed to make progress
// on. Condemning a live runtime takes TWO probe rounds — the first only starts
// condemnedConfirmWindow — and nothing else schedules the second: with dsh still
// registered nothing is missing a runtime, so an unforced tick returns from
// convergeAgentRuntimes without probing at all. Forcing only the first sighting
// therefore leaves the runtime live and taking work forever, which is the exact
// failure the mismatch check exists to prevent.
//
// The other direction has no such guarantee and must NOT force every tick: a
// profile that is installed but answers nothing keeps that mismatch for as long
// as it is installed, and forcing on it would spend a full probe of every
// provider every agentDiscoveryInterval, permanently bypassing
// agentConvergeMaxBackoff.
func (m dshProfileMismatch) forcesEveryTick() bool {
	return m == dshMismatchRuntimeWithoutProfile
}

// dshRuntimeProfileMismatch names the current disagreement, or dshMismatchNone
// when the two states agree.
//
// Deliberately not a change detector: this is compared on every tick against
// live state, so a profile removed before the daemon's first look — or while it
// was not looking — is caught on the very next tick rather than never.
func (d *Daemon) dshRuntimeProfileMismatch() dshProfileMismatch {
	registered := d.dshRuntimeRegistered()
	if registered == dshMulticaProfilePresent() {
		return dshMismatchNone
	}
	if registered {
		return dshMismatchRuntimeWithoutProfile
	}
	// Only worth a round if there is a dsh CLI to register at all: without one
	// nothing this round does can resolve the mismatch.
	if _, discovered := d.agents()["dsh"]; discovered {
		return dshMismatchProfileWithoutRuntime
	}
	return dshMismatchNone
}

// dshRuntimeRegistered reports whether any tracked workspace still holds a
// built-in dsh runtime. Custom runtime profiles are excluded: they carry a
// ProfileID and are owned by the drift path, not by the built-in set.
func (d *Daemon) dshRuntimeRegistered() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ws := range d.workspaces {
		for _, id := range ws.runtimeIDs {
			rt, ok := d.runtimeIndex[id]
			if !ok || rt.ProfileID != "" {
				continue
			}
			if rt.Provider == "dsh" {
				return true
			}
		}
	}
	return false
}

// kickAgentDiscovery asks the discovery loop for an immediate convergence
// round. Safe on a zero-value Daemon and from any goroutine: the send is
// non-blocking, so a burst of producers collapses into one round and none of
// them waits on the loop.
func (d *Daemon) kickAgentDiscovery() {
	if d.agentDiscoveryKick == nil {
		return
	}
	select {
	case d.agentDiscoveryKick <- struct{}{}:
	default:
	}
}

// nextConvergeBackoff 让重试延迟翻倍，从“一个发现间隔”起步、以
// agentConvergeMaxBackoff 封顶。
func nextConvergeBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return agentDiscoveryInterval
	}
	next := current * 2
	if next > agentConvergeMaxBackoff {
		return agentConvergeMaxBackoff
	}
	return next
}

// agents 返回当前内置 agent 的可用集合。
//
// 返回的 map 是共享的，*严禁*修改：refreshAgentAvailability 发布的是整张新 map，而
// 不是就地编辑这张——这正是任务执行路径可以不加锁读它的原因。
func (d *Daemon) agents() map[string]AgentEntry {
	if m := d.agentsAvailable.Load(); m != nil {
		return *m
	}
	// 零值 Daemon（测试直接构造的）：回退到启动配置，让行为与引入 refresh 之前的代码
	// 路径一致。
	return d.cfg.Agents
}

// setSkippedAgents 替换 /health 上报的“发现但未注册”诊断集合。每轮注册结束时，用
// 该轮丢弃的 provider 调用。
//
// 纯诊断，且刻意如此：它在所有做探测的 goroutine 之间是“最后写入者胜”，因此任何行为
// 都不得以它为准。需要就结论采取行动的调用方，应取 detectBuiltinRuntimes 的返回值——
// 那描述的是它自己那一轮。
func (d *Daemon) setSkippedAgents(skipped map[string]string) {
	d.skippedAgentsMu.Lock()
	defer d.skippedAgentsMu.Unlock()
	d.skippedAgents = skipped
}

// skippedAgentsSnapshot 为健康处理器拷贝当前跳过原因。
func (d *Daemon) skippedAgentsSnapshot() map[string]string {
	d.skippedAgentsMu.RLock()
	defer d.skippedAgentsMu.RUnlock()
	if len(d.skippedAgents) == 0 {
		return nil
	}
	out := make(map[string]string, len(d.skippedAgents))
	for name, reason := range d.skippedAgents {
		out[name] = reason
	}
	return out
}

// refreshAgentAvailability 重跑 CLI 发现，并发布上次探测以来新出现的 provider。它不
// 做注册——那是 convergeRuntimeRegistrations 的事，由实时状态驱动使失败可重试。
//
// 刻意单向：只对“新增的”provider 行动。一个“停止解析成功”的 provider 会被保留——
// 否则一次瞬态环境差异（daemon 从更窄的 PATH 重启、版本管理器升级到一半）就会拆掉
// 一个正在工作、甚至可能正在执行任务的 runtime。移除仍是“显式重启”的职责——那时用户
// 自己选了环境。
//
// 返回被新增的 provider，供日志与测试使用。
func (d *Daemon) refreshAgentAvailability() []string {
	current := d.agents()
	probed := probeAgentCLIs()

	var gained []string
	for name := range probed {
		if _, known := current[name]; !known {
			gained = append(gained, name)
		}
	}
	if len(gained) == 0 {
		return nil
	}
	sort.Strings(gained)

	// 写时复制：先并出并集，再发布。已认识的 provider 的条目原样保留，这样本函数
	// 永远不会和运行中 provider 的“固定路径/自愈簿记”（resolvedPaths）打架。
	merged := make(map[string]AgentEntry, len(current)+len(gained))
	for name, entry := range current {
		merged[name] = entry
	}
	for _, name := range gained {
		merged[name] = probed[name]
	}
	d.agentsAvailable.Store(&merged)
	d.logger.Info("agent CLI discovered after startup", "providers", gained)
	return gained
}

// refreshAgentVersions 重新探测每个已装 agent CLI，并且在“服务器还没被告知本 daemon
// 已探测到的某个版本”时重注册内置 runtime，让服务器显示的内容与磁盘一致。
//
// 这是 trySelfReload 在 agent CLI 侧的对应物，而且刻意*不*重启。codex 或 claude
// 升级后用户要的是：后续任务跑新 CLI、受新版本规则约束——而不是 Multica 的可用性跟着
// 第三方发版节奏走。POSIX 的原地升级会换掉固定路径背后的二进制；Windows 安装器升级
// 会重定向 resolveAgentEntry 解析出的稳定 junction。剩下来变旧的两样是：缓存的版本
// （驱动版本敏感策略，如 Codex sandbox）与服务器展示的版本。两者都在这里刷新，且不
// 碰运行中的任务。
//
// 探测由 detectBuiltinRuntimes 做，白得三个性质：探测扇出、快速失败可重试
// （runtimeVersionProbeAttempts）、最低版本门槛依然生效。本轮探测失败的 provider 保留
// 上一版本、下轮再试——失败的探测绝不会看起来像“版本变了”。
//
// “发什么”按*每个 workspace* 从 workspaceState.builtinVersions 决定——那是该
// workspace 上次被服务器接受的注册调用实际携带的版本——绝不从共享版本缓存的
// before/after 差值推断。缓存会被每条做探测的路径写入，因此差值只有在“本轮恰好是
// 第一个写入者”时才看得到变化：一轮 converge、新 workspace 注册、或任务启动时的
// 自愈都会先动它，让本轮看着没事可做——而那些路径没碰过的 workspace 则无限期停在旧
// 版本上。
//
// 确认是 per-workspace 而不是全局 pending 集合，反过来也是同一道理：注册入口不串行，
// 因此“升级前探测”的注册可能落在“刷新轮已更新它能看到的每个 workspace”之后。此刻清
// 掉一个全局 "done" 标志，会把迟到的 workspace 永远搁在旧版本。范围缩到 workspace，
// 迟到的调用只是记下自己发过什么，记录与下一轮探测不一致，那一轮就精确重注册掉队的
// 那个 workspace。
//
// 重注册携带整个内置集合，因为那是注册端点 upsert 的形状，所以一个 provider 升级会
// 顺带重发邻居。这安全而无害：未变条目 upsert 结果相同，mergeBuiltinRegisterResponse
// 会把服务端 ID 轮换就地换掉而不是累积出重复——每个 workspace 每 provider 仍恰好一
// 个 runtime，没有任何运行中任务被碰。
// 本函数无条件运行，即使收敛那半有活干也不让路——尽管两者偶尔会在同一窗口探测。
// 一个永久低于最低支持版本的 provider 永远不会离开 providersMissingRuntimes——这是
// 有意的，让它升级后自行恢复——因此向它让路会让一个卡住的 CLI 永久悄悄禁掉机器上每
// 个健康 provider 的版本刷新。
func (d *Daemon) refreshAgentVersions(ctx context.Context) {
	// 还从未做过任何版本探测：daemon 仍在启动，初次注册是同步路径的职责。
	if !d.hasDetectedAgentVersions() {
		return
	}

	builtins, demotable, _ := d.detectBuiltinRuntimes(ctx)
	// 在下面的提前返回之前执行：被“判死刑”的 provider 会整个掉出 builtins，于是没有
	// 版本变化可探测——否则机器可能看似空闲，而一个不可用的 CLI 还在持续接活。
	if len(demotable) > 0 {
		d.demoteUnusableRuntimes(ctx, demotable)
	}
	if len(builtins) == 0 {
		return
	}
	// 本轮载荷实际携带的东西，按 provider 记。探测失败的 provider 缺席——只有携带了
	// 的 provider 才会被下面比较，这正阻止“已卸载的 CLI 触发注册”。它从不被移出可用
	// 集合（refreshAgentAvailability 刻意单向），于是它持续探测失败、永不重现于载荷；
	// 否则拿记录去对一份无法提及它的载荷，会变成“每个 workspace 每几分钟一次注册
	// 调用”，永无止境。
	carried := builtinVersionsFromPayload(builtins)

	// 当“某携带 provider 上次为该 workspace 被接受的注册调用”携带了不同版本时，该
	// workspace 落后。对某 workspace 没有记录的 provider 直接跳过：它从没在那注册过，
	// 把它带上线是 converge 路径的职责，不是版本变化。
	d.mu.Lock()
	var behind []string
	transitions := make(map[string]struct{})
	for id, ws := range d.workspaces {
		lagging := false
		for provider, version := range carried {
			sent, has := ws.builtinVersions[provider]
			if !has || sent == version {
				continue
			}
			lagging = true
			if sent == "" {
				transitions[fmt.Sprintf("%s %s", provider, version)] = struct{}{}
			} else {
				transitions[fmt.Sprintf("%s %s -> %s", provider, sent, version)] = struct{}{}
			}
		}
		if lagging {
			behind = append(behind, id)
		}
	}
	d.mu.Unlock()
	if len(behind) == 0 {
		return
	}
	sort.Strings(behind)
	advancing := make([]string, 0, len(transitions))
	for t := range transitions {
		advancing = append(advancing, t)
	}
	sort.Strings(advancing)
	d.logger.Info("agent CLI version not yet on the server; refreshing registration without a restart",
		"versions", advancing, "workspace_ids", behind)

	// 只注册落后的 workspace。失败不记录任何东西，所以该 workspace 保持落后、下轮
	// 重试。
	var changed bool
	for _, id := range behind {
		// 发送、合并、清理作为单个有序步骤——见 workspaceRegisterLock。
		_ = d.withWorkspaceRegisterLock(id, func() error {
			resp, err := d.registerBuiltinRuntimesForWorkspaceLocked(ctx, id, builtins)
			if err != nil {
				d.logger.Warn("re-register after agent CLI version change failed; will retry",
					"workspace_id", id, "error", err)
				return nil
			}
			newIDs, revived, ok := d.mergeBuiltinRegisterResponse(id, resp)
			if !ok {
				return nil
			}
			if len(newIDs) > 0 {
				changed = true
			}
			d.deregisterRevivedRuntimes(ctx, id, revived)
			return nil
		})
	}
	if changed {
		d.notifyRuntimeSetChanged()
	}
}

// demoteUnusableRuntimes 把“其复探对磁盘二进制得出确认结论”的 provider 的内置
// runtime 拉下线：版本低于最低支持版本，或文件本身被 OS 拒绝执行。
//
// 没有它，原地*降级*就是唯一“什么都不会反应”的机器变化。probeBuiltinRuntime 会把该
// provider 从注册载荷里剔除，于是 refreshAgentVersions 看不到可比对的版本；runtime 行
// 因内置合并是加法式而幸存；而该 provider 恰恰因还持有一个 runtime 而缺席
// providersMissingRuntimes——所以 converge 也忽略它。daemon 于是一边继续把任务路由给
// 它已证实跑不对的二进制，一边还按先前缓存的版本策略行事。
//
// 无法 exec 的 CLI 经另一条路也撞进同一死胡同（MUL-6164）：npm 占位 stub 能通过
// daemon 做的每一项“装没装”检查，因此 runtime 保持在线、它认领的每个任务都死在
// fork/exec。把它当瞬时故障，用户就会每任务看到一次同样的失败，而不是看到一次，且
// 毫无“是这个 runtime 的问题”的信号。
//
// 注销是最窄而诚实的回应。它阻止服务器往那里派活，原因已可见于 skipped_agents，
// 恢复也不需要新机制：provider 一旦升级好就没了 runtime，于是回到
// providersMissingRuntimes、让 converge 路径再注册它。另一种做法——保留 runtime 但
// 拒绝启动——把检查塞进热路径，还让 UI 宣传一个拒收一切任务的 runtime。
//
// 只对“确认的结论”行动，绝不对“仅仅失败的探测”行动：版本读不出来是瞬态的，为一个
// 就拆掉能工作的 runtime，正是 refreshAgentAvailability 单向规则存在的理由。
// detectBuiltinRuntimes 掌握这个区分，包括“不可执行”结论必须先清掉的确认窗口。
//
// causes 把每个被“判死刑”的 provider 映射到它的罪证——已在 skipped_agents 里对用户
// 可见，下面日志报的也是它。
func (d *Daemon) demoteUnusableRuntimes(ctx context.Context, causes map[string]runtimeVerdict) {
	// 像重启路径那样与任务认领串行：屏障只在“没有在途 claim、没有运行中任务”时才置
	// 位，因此 runtime 绝不会在仍有任务执行时被注销——否则服务器那边的清理会对该任务
	// 失败重试而本地进程照跑，一次“升级回头”还会把 runtime ID 复活成一次真正的重复
	// 执行。繁忙 daemon 顺延到下一刷新 tick；过老的 CLI 多撑一会儿 runtime，这是降级前
	// 的现状，不是新的暴露面。
	if !d.trySetClaimBarrier() {
		d.logger.Info("defer runtime demotion: task or claim in flight",
			"providers", causes)
		return
	}
	defer d.releaseClaimBarrier()

	d.mu.Lock()
	var demoted []string
	var installWaits []dshInstallWait
	// Grouped per workspace because the cleanup below has to run under each
	// workspace's register lock — see deregisterDroppedRuntimes.
	demotedByWorkspace := make(map[string][]string)
	demotedProviders := make(map[string]string)
	offlineReasons := make(map[string]RuntimeOfflineReason)
	for workspaceID, ws := range d.workspaces {
		// 用新数组而不是 ws.runtimeIDs[:0]：健康处理器会在 d.mu 下拷贝这个切片头、
		// 释放锁后再序列化它（removeStaleRuntime 守同一条规矩），原地过滤等于写进另一
		// 个 goroutine 正在读的切片。
		kept := ws.runtimeIDs[:0:0]
		for _, rid := range ws.runtimeIDs {
			rt, ok := d.runtimeIndex[rid]
			if !ok || rt.ProfileID != "" {
				kept = append(kept, rid)
				continue
			}
			cause, condemned := causes[rt.Provider]
			if !condemned {
				kept = append(kept, rid)
				continue
			}
			delete(d.runtimeIndex, rid)
			demoted = append(demoted, rid)
			demotedByWorkspace[workspaceID] = append(demotedByWorkspace[workspaceID], rid)
			demotedProviders[rt.Provider] = cause.reason
			// 逐 runtime 行带给服务器：问“我的 agent 为什么离线”的客户端从那里读，
			// 而只有这一侧同时知道结论与修复命令（MUL-6164）。
			if cause.offline != nil {
				offlineReasons[rid] = *cause.offline
				// A reason that says an install is running is a promise this
				// row comes back on its own. Remember it so the install can
				// take the promise back if it ends without a profile.
				if cause.offline.Installing {
					installWaits = append(installWaits, dshInstallWait{
						workspaceID: workspaceID, runtimeID: rid, reason: *cause.offline,
					})
				}
			}
			// runtime 已不在，为它登记的记录也一起删。provider 恢复时，converge 重注册
			// 并重新播种记录——首次现身归 converge，不是让刷新轮去追的版本变化。
			delete(ws.builtinVersions, rt.Provider)
		}
		ws.runtimeIDs = kept
	}
	// 在释放 d.mu 前记住结论。删行不够：本降级之前发出的注册仍在途，其响应落地瞬间就
	// 会把该 provider 重新索引。apply 路径在同一把锁下查这份记录，所以两者全序。
	d.markProvidersDemotedLocked(causes)
	d.dshInstallWaits = append(d.dshInstallWaits, installWaits...)
	d.mu.Unlock()

	if len(demoted) == 0 {
		return
	}
	d.logger.Warn("agent CLI is no longer usable on this machine; taking its runtimes offline",
		"providers", demotedProviders, "runtime_ids", demoted)

	// 一次一个 workspace，各自在自己的注册锁下：行已在 d.mu 下删掉，但请求是在无锁
	// 情况下发出的，因此“中间升级恢复了 provider”绝不能被我这份更旧的清理撤销。排好
	// 序让日志顺序确定；绝不同时持两把锁，就没有可死锁的顺序。
	workspaceIDs := make([]string, 0, len(demotedByWorkspace))
	for workspaceID := range demotedByWorkspace {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	sort.Strings(workspaceIDs)
	for _, workspaceID := range workspaceIDs {
		_ = d.withWorkspaceRegisterLock(workspaceID, func() error {
			d.deregisterDroppedRuntimes(ctx, workspaceID, demotedByWorkspace[workspaceID],
				"agent CLI unusable", offlineReasons)
			return nil
		})
	}
	d.notifyRuntimeSetChanged()
}

// dshInstallWait is one runtime row whose offline reason claims an automatic
// profile install will bring it back.
type dshInstallWait struct {
	workspaceID string
	runtimeID   string
	reason      RuntimeOfflineReason
}

// withdrawDshInstallWait rewrites the offline reason of every runtime that was
// taken offline while an automatic profile install was still running, once that
// install has ended without producing a profile.
//
// Without it the claim is permanent. `installing` is what tells the server this
// particular offline runtime is worth queueing behind (MUL-6164), and the row
// carrying it is written exactly once, by the demotion — which also removes the
// runtime from the index, so no later round finds a runtime to condemn and no
// later deregistration is sent. An install that fails after that point leaves
// every assignment and @mention for the agent queueing behind a wait that has
// already given up, which is the failure the structured reason exists to
// prevent, not a new one to introduce.
//
// Best-effort, like every other deregistration path: the reason is a hint on a
// row that is already offline, and the row is corrected again the moment the
// provider registers.
func (d *Daemon) withdrawDshInstallWait(ctx context.Context) {
	d.mu.Lock()
	waits := d.dshInstallWaits
	d.dshInstallWaits = nil
	d.mu.Unlock()
	if len(waits) == 0 {
		return
	}

	byWorkspace := make(map[string][]dshInstallWait, len(waits))
	for _, wait := range waits {
		byWorkspace[wait.workspaceID] = append(byWorkspace[wait.workspaceID], wait)
	}
	workspaceIDs := make([]string, 0, len(byWorkspace))
	for workspaceID := range byWorkspace {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	sort.Strings(workspaceIDs)

	for _, workspaceID := range workspaceIDs {
		_ = d.withWorkspaceRegisterLock(workspaceID, func() error {
			runtimeIDs := make([]string, 0, len(byWorkspace[workspaceID]))
			reasons := make(map[string]RuntimeOfflineReason, len(byWorkspace[workspaceID]))
			for _, wait := range byWorkspace[workspaceID] {
				reason := wait.reason
				reason.Installing = false
				reason.Detail = dshInstallGaveUpReason
				runtimeIDs = append(runtimeIDs, wait.runtimeID)
				reasons[wait.runtimeID] = reason
			}
			// Same tracking re-check every deregistration path makes: a
			// register that brought the runtime back between the demotion and
			// now is newer than this correction, and its row already says
			// online.
			runtimeIDs = d.untrackedRuntimeIDs(runtimeIDs)
			if len(runtimeIDs) == 0 {
				return nil
			}
			d.logger.Warn("automatic DSH profile install gave up; withdrawing the wait from its runtimes",
				"workspace_id", workspaceID, "runtime_ids", runtimeIDs)
			if err := d.client.Deregister(ctx, runtimeIDs, reasons); err != nil {
				d.logger.Warn("withdrawing the DSH install wait failed",
					"workspace_id", workspaceID, "runtime_ids", runtimeIDs, "error", err)
			}
			return nil
		})
	}
}

// deregisterRevivedRuntimes takes offline the rows a register response brought
// back for a provider that was demoted while that register was in flight.
//
// 本地 apply 已经拒收它们，因此没有本函数，服务器就会是唯一还相信该 runtime 在线的
// 一侧：它会保留行、把活路由给一个已不再跟踪它的 daemon，那些任务会一直没人认领直到
// 过期心跳清理。与其它每条注销路径一样尽力而为——清理是兜底。
//
// 调用方必须持有该 workspace 的注册锁（workspaceRegisterLock）。
func (d *Daemon) deregisterRevivedRuntimes(ctx context.Context, workspaceID string, revived revivedRuntimes) {
	runtimeIDs := revived.ids
	// 先复查跟踪：拒绝发生在 d.mu 下，但本调用在无锁下跑，那段缝隙里一次合法的恢复
	// 注册会重建同一行——通常还是同一个 ID。这时注销等于拿一个更旧的裁决把恢复好的
	// runtime 拉下线。daemon 现在仍跟踪的任何东西都比这份清理新，而注册锁能阻止恢复
	// 在本复查与下面请求之间溜进来。
	runtimeIDs = d.untrackedRuntimeIDs(runtimeIDs)
	if len(runtimeIDs) == 0 {
		return
	}
	d.logger.Warn("register response revived a demoted runtime; taking it offline again",
		"workspace_id", workspaceID, "runtime_ids", runtimeIDs)
	// 重发原因：那次注册的 upsert 覆盖了 runtime 行的元数据，降级时存的原因没了，否则
	// 服务器会留下一个光秃秃的 "offline"——在每条准入路径上都读作“等这台机器”
	// （MUL-6164）。
	if err := d.client.Deregister(ctx, runtimeIDs, revived.reasonsFor(runtimeIDs)); err != nil {
		d.logger.Warn("deregister of revived demoted runtimes failed",
			"workspace_id", workspaceID, "runtime_ids", runtimeIDs, "error", err)
	}
}

// deregisterDroppedRuntimes 把“一次收敛决定该 workspace 不应再托管”的服务端行拉下
// 线——从 daemon 配置里移除的 provider、被禁用的自定义 profile、收敛到零的 workspace。
//
// 与 deregisterRevivedRuntimes 同一条契约：调用方必须持有该 workspace 的注册锁，让
// “跟踪复查”与“请求”成为该 workspace 对其它一切注册的一个有序步骤。没有锁这就是
// TOCTOU——两步之间完成的一次恢复注册把行放回去，而这份更旧的清理又把它打掉，留下
// daemon 跟踪着服务器已离线的 runtime。
//
// 尽力而为：daemon 已停止对这些行发心跳，调用失败时服务器的过期心跳清理是兜底。
func (d *Daemon) deregisterDroppedRuntimes(ctx context.Context, workspaceID string, runtimeIDs []string, reason string, offlineReasons map[string]RuntimeOfflineReason) {
	runtimeIDs = d.untrackedRuntimeIDs(runtimeIDs)
	if len(runtimeIDs) == 0 {
		return
	}
	if err := d.client.Deregister(ctx, runtimeIDs, offlineReasons); err != nil {
		d.logger.Warn("deregister of dropped runtimes failed",
			"workspace_id", workspaceID, "runtime_ids", runtimeIDs, "reason", reason, "error", err)
	}
}

// providersMissingRuntimes 返回“没有为每个受管 workspace 都注册内置 runtime”的已
// 发现 provider。
//
// 这是发现循环的重试信号，且由实时状态推导而不是记住：provider 只有等 daemon 真在它
// 看的每个 workspace 里持有一行 runtime 才会离开本集合。自定义 profile runtime
// （ProfileID 已设）被忽略——它们从 workspace 的 profile 列表注册，不是从 CLI 发现
// 注册，而恰好与某个内置共享 protocol_family 的 profile 绝不能掩盖内置。
//
// 还没有任何 workspace 被跟踪时返回空：没有可注册的对象，初次注册会携带完整集合。
func (d *Daemon) providersMissingRuntimes() []string {
	available := d.agents()
	if len(available) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.workspaces) == 0 {
		return nil
	}
	missing := make(map[string]struct{})
	for _, ws := range d.workspaces {
		registered := make(map[string]struct{}, len(ws.runtimeIDs))
		for _, id := range ws.runtimeIDs {
			rt, ok := d.runtimeIndex[id]
			if !ok || rt.ProfileID != "" {
				continue
			}
			registered[rt.Provider] = struct{}{}
		}
		for name := range available {
			if _, ok := registered[name]; !ok {
				missing[name] = struct{}{}
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	out := make([]string, 0, len(missing))
	for name := range missing {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// convergeRuntimeRegistrations 为每个“缺少某个已发现内置 runtime”的受管 workspace
// 注册当前内置集合。
//
// 一轮版本探测服务所有 workspace（内置 CLI 是机器级，不是 workspace 级）。失败记日志、
// 留给下一发现 tick——这里什么都不记 “already handled”，因此跨 workspace 的部分失败
// 只会重试仍需要它的那些 workspace。
//
// 严格内置：本路径绝不抓取、发送或观察自定义 runtime profile。自定义 profile 的增/改/
// 禁用仍归 drift 路径（refreshWorkspaceRuntimeProfiles）——它是唯一允许缓存
// profile-set 签名的位置。
//
// 刻意不调用 RecoverOrphans：与 runtime_gone 恢复路径不同，幸存的 runtime ID 可能
// 仍在为用户执行任务，把它们当孤儿失败会杀死活的产出（MUL-3332）。
func (d *Daemon) convergeRuntimeRegistrations(ctx context.Context) {
	// detectBuiltinRuntimes version-gates the availability set and publishes
	// this round's drops for /health, so a provider that cannot register still
	// gets a visible reason even though registration is skipped.
	builtins, demotable, _ := d.detectBuiltinRuntimes(ctx)
	// Condemned providers drop out of builtins entirely, so acting on them
	// before the early return is what keeps a dsh that goes bad mid-flight from
	// staying online: the profile removed while dsh is registered leaves nothing
	// missing a runtime, and a round reached only for that has no registration
	// left to do. demoteUnusableRuntimes owns the claim barrier and the
	// seq-stamped hold, so running it here is safe.
	//
	// Restricted to dsh on purpose. Every other provider is demoted from
	// refreshAgentVersions alone, on its own ten-minute cadence, and that is the
	// schedule their verdicts were tuned against; letting a converge round
	// condemn them too would move Claude Code, Codex and the rest onto a
	// different one as a side effect of a DSH fix. dsh is the only provider with
	// a precondition that can change without any version changing, which is why
	// it is the only one that needs a round it can be forced into.
	if dshDemotable := dshOnlyVerdicts(demotable); len(dshDemotable) > 0 {
		d.demoteUnusableRuntimes(ctx, dshDemotable)
	}
	if len(builtins) == 0 {
		return
	}
	detected := make(map[string]struct{}, len(builtins))
	for _, rt := range builtins {
		detected[rt["type"]] = struct{}{}
	}

	d.mu.Lock()
	type target struct {
		id      string
		missing []string
	}
	var targets []target
	for id, ws := range d.workspaces {
		registered := make(map[string]struct{}, len(ws.runtimeIDs))
		for _, rid := range ws.runtimeIDs {
			rt, ok := d.runtimeIndex[rid]
			if !ok || rt.ProfileID != "" {
				continue
			}
			registered[rt.Provider] = struct{}{}
		}
		var missing []string
		for name := range detected {
			if _, ok := registered[name]; !ok {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			targets = append(targets, target{id: id, missing: missing})
		}
	}
	d.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].id < targets[j].id })

	var changed bool
	for _, t := range targets {
		// 发送、合并、清理作为单个有序步骤——见 workspaceRegisterLock。
		_ = d.withWorkspaceRegisterLock(t.id, func() error {
			resp, err := d.registerBuiltinRuntimesForWorkspaceLocked(ctx, t.id, builtins)
			if err != nil {
				// 有意留在缺失集合里：下一 tick 会重试。
				d.logger.Warn("register newly available runtimes failed; will retry",
					"workspace_id", t.id, "providers", t.missing, "error", err)
				return nil
			}
			newIDs, revived, ok := d.mergeBuiltinRegisterResponse(t.id, resp)
			if !ok {
				return nil
			}
			if len(newIDs) > 0 {
				changed = true
				d.logger.Info("registered runtime for newly installed agent CLI",
					"workspace_id", t.id, "providers", t.missing, "runtime_ids", newIDs)
			}
			d.deregisterRevivedRuntimes(ctx, t.id, revived)
			return nil
		})
	}
	if changed {
		d.notifyRuntimeSetChanged()
	}
}
