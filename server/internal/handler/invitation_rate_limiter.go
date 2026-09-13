// 职责定位：工作区邀请（invitation）入口的滥用限频闸。防的是「用邀请邮件骚扰他人」这类
// 邮件滥用，所以从三个相互独立的维度各设一道滑动窗口预算：发起人（actor，短窗口）、
// 目标工作区（workspace，长窗口）、收件邮箱（recipient，按邮箱哈希做 key，长窗口）。
// 具体额度见 DefaultInvitationRateLimits。
//
// 两阶段语义（Check 与 Allow 分离）：创建邀请前先对三道闸做「不消费预算」的检查
// （checkInvitationAdmission），任何一道被拒就返回 429 并写 Retry-After；只有真正要
// 落库时才逐道「消费」配额（consumeInvitationAdmission）。后端（Redis）出故障时这道闸
// 是失败关闭（fail-closed）：检查阶段任一闸查不到配额就整体回 503，宁可暂时不可用，
// 也不让邀请绕过限频（与 webhook 入口的 fail-open 正好相反，邀请是安全边界）。
//
// 实现：底层复用 webhook_rate_limiter.go 的滑动窗口限流器（内存版/Redis 版），这里只是
// 按邀请的三个维度装配 key。Redis 版各维度用独立 key 前缀隔离命名空间，可跨副本共享配额。
package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/redis/go-redis/v9"
)

const (
	// Redis 版限频 key 前缀：三个维度各占一段命名空间，互不串扰。
	invitationActorLimiterKeyPrefix     = "mul:invitation:actor:"
	invitationWorkspaceLimiterKeyPrefix = "mul:invitation:workspace:"
	invitationRecipientLimiterKeyPrefix = "mul:invitation:recipient:"
)

// InvitationRateLimits 配置邀请入口三档相互独立的准入预算。
type InvitationRateLimits struct {
	Actor     SlidingWindowRateLimit // 按发起人计
	Workspace SlidingWindowRateLimit // 按目标工作区计
	Recipient SlidingWindowRateLimit // 按被邀请邮箱计
}

// DefaultInvitationRateLimits 返回保守的邮件滥用预算。
//
// 取值逻辑：发起人 10 分钟 10 条，限制单个用户短时间内轰炸式发邀请；工作区 24 小时
// 50 条，卡住一个工作区被反复恶意邀请；同一邮箱 24 小时 6 条，阻止拿同一地址骚扰。
// 这些都是偏保守的额度——正常邀请节奏远够用，被攻击时损失可控。
func DefaultInvitationRateLimits() InvitationRateLimits {
	return InvitationRateLimits{
		Actor:     SlidingWindowRateLimit{Limit: 10, Window: 10 * time.Minute},
		Workspace: SlidingWindowRateLimit{Limit: 50, Window: 24 * time.Hour},
		Recipient: SlidingWindowRateLimit{Limit: 6, Window: 24 * time.Hour},
	}
}

// InvitationRateLimiters 持有三个维度各自独立的限流器实现（内存版或 Redis 版）。
type InvitationRateLimiters struct {
	Actor     SlidingWindowRateLimiter
	Workspace SlidingWindowRateLimiter
	Recipient SlidingWindowRateLimiter
}

// invitationRateLimitDimension 标记一道闸代表哪个维度，用于区分「谁被限了」。
type invitationRateLimitDimension string

const (
	invitationRateLimitActor     invitationRateLimitDimension = "actor"
	invitationRateLimitWorkspace invitationRateLimitDimension = "workspace"
	invitationRateLimitRecipient invitationRateLimitDimension = "recipient"
)

// NewMemoryInvitationRateLimiters 构造单进程内生效的限流器组，供本地开发和无 Redis 的
// 单实例部署使用（配额不跨进程共享）。
func NewMemoryInvitationRateLimiters(limits InvitationRateLimits) InvitationRateLimiters {
	return InvitationRateLimiters{
		Actor:     newMemorySlidingWindowRateLimiter(limits.Actor),
		Workspace: newMemorySlidingWindowRateLimiter(limits.Workspace),
		Recipient: newMemorySlidingWindowRateLimiter(limits.Recipient),
	}
}

// NewRedisInvitationRateLimiters 构造基于 Redis 的限流器组：多个副本共享同一份配额，
// 各维度用彼此隔离的 key 前缀（见文件顶部常量）。
func NewRedisInvitationRateLimiters(rdb *redis.Client, limits InvitationRateLimits) InvitationRateLimiters {
	return InvitationRateLimiters{
		Actor:     newRedisSlidingWindowRateLimiter(rdb, limits.Actor, invitationActorLimiterKeyPrefix),
		Workspace: newRedisSlidingWindowRateLimiter(rdb, limits.Workspace, invitationWorkspaceLimiterKeyPrefix),
		Recipient: newRedisSlidingWindowRateLimiter(rdb, limits.Recipient, invitationRecipientLimiterKeyPrefix),
	}
}

// invitationRateLimitGate 是「一道闸 + 它这次要限的 key」的组合。key 的具体含义随
// dimension 而定：actor/workspace 维度直接是用户 ID / 工作区 ID，recipient 维度是
// 邮箱的哈希（见 invitationRecipientKey）。
type invitationRateLimitGate struct {
	dimension invitationRateLimitDimension
	key       string
	limiter   SlidingWindowRateLimiter
}

// invitationAdmission 是一次邀请请求通过检查后拿到的「放行凭证」：记录着该次请求要
// 消费的三道闸，供稍后真正落库时统一扣配额。
type invitationAdmission struct {
	gates []invitationRateLimitGate
}

// invitationRecipientKey 把收件邮箱归一化（小写 + 去空白）后做 SHA-256 摘要，作为
// recipient 维度的限频 key。之所以存哈希而不是明文：邮箱是个人数据，限流器 key 会落进
// Redis/日志，哈希后即使泄露也无法反推出真实地址。
func invitationRecipientKey(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:])
}

// checkInvitationAdmission 对三道限频闸做非消耗性检查（第一道检查阶段）。
//
// 语义：任一闸的限流后端报错即整体失败关闭——回 503 invitation_rate_limiter_unavailable
// 并写 Retry-After: 5，同时返回 false；有闸被拒（配额用尽）则回 429，Retry-After 取所有
// 被拒闸里的最大值，并给每道被拒闸记一条限频埋点。全部通过才返回带全部三道闸的
// admission 供随后消费。
func (h *Handler) checkInvitationAdmission(w http.ResponseWriter, r *http.Request, actorID, workspaceID, email string) (*invitationAdmission, bool) {
	gates := []invitationRateLimitGate{
		{dimension: invitationRateLimitActor, key: actorID, limiter: h.InvitationRateLimiters.Actor},
		{dimension: invitationRateLimitWorkspace, key: workspaceID, limiter: h.InvitationRateLimiters.Workspace},
		{dimension: invitationRateLimitRecipient, key: invitationRecipientKey(email), limiter: h.InvitationRateLimiters.Recipient},
	}

	var checkErr error
	var checkErrorGates []string
	var deniedGates []invitationRateLimitGate
	var retryAfter time.Duration
	for _, gate := range gates {
		allowed, err := slidingWindowLimiterCheckWithError(r.Context(), gate.limiter, gate.key)
		if err != nil {
			if checkErr == nil {
				checkErr = err
			}
			checkErrorGates = append(checkErrorGates, string(gate.dimension))
			continue
		}
		if allowed {
			continue
		}

		deniedGates = append(deniedGates, gate)
		if retry := slidingWindowLimiterRetryAfter(r.Context(), gate.limiter, gate.key); retry > retryAfter {
			retryAfter = retry
		}
	}

	if checkErr != nil {
		slog.Error("invitation rate limiter unavailable", append(logger.RequestAttrs(r), "gates", strings.Join(checkErrorGates, ","), "error", checkErr)...)
		writeInvitationLimiterUnavailable(w)
		return nil, false
	}
	if len(deniedGates) > 0 {
		for _, gate := range deniedGates {
			h.Metrics.RecordEmailRateLimited("workspace_invitation", string(gate.dimension))
		}
		writeInvitationRateLimited(w, retryAfter)
		return nil, false
	}
	return &invitationAdmission{gates: gates}, true
}

// consumeInvitationAdmission 在邀请真正落库时，把这次请求对应的三道闸配额全部扣掉
// （第二道消费阶段）。与 check 分离是为了让「检查通过但因故没建成邀请」的请求不白扣配额。
func (h *Handler) consumeInvitationAdmission(r *http.Request, admission *invitationAdmission) {
	if admission == nil {
		return
	}
	h.consumeInvitationGates(r, admission.gates)
}

// consumeInvitationActorAdmission 只扣发起人那一档预算、不碰 workspace 与收件邮箱预算。
// 它用于「邀请被持久性的席位容量拒绝」这种场景：该工作区在容量事实变化前都进不了人，
// 反复尝试属于发起人自己的滥用，记到发起人头上；而 workspace/邮箱预算应该留给未来
// 真正能建成的有效邀请。
func (h *Handler) consumeInvitationActorAdmission(r *http.Request, admission *invitationAdmission) {
	if admission == nil {
		return
	}
	for _, gate := range admission.gates {
		if gate.dimension == invitationRateLimitActor {
			h.consumeInvitationGates(r, []invitationRateLimitGate{gate})
			return
		}
	}
}

// consumeInvitationGates 逐道闸执行消费配额（Allow）。两道闸之间的竞态处理遵循
// 「宁可轻微超发、也不拒绝已通过检查的在途请求」的原则，理由见函数体内注释。
func (h *Handler) consumeInvitationGates(r *http.Request, gates []invitationRateLimitGate) {
	for _, gate := range gates {
		allowed, err := slidingWindowLimiterAllow(r.Context(), gate.limiter, gate.key)
		if err != nil {
			// 三道闸前面都通过了非消耗检查，这里失败只可能发生在「检查 → 消费」这段窄
			// 窗口里后端恰好故障。与其在别的闸可能已扣了配额之后又返回错误，不如放行这
			// 个已在途的请求，只允许轻微超发。若后端是持续故障，之后的新请求会在第一道
			// 检查阶段（checkInvitationAdmission）被整体拦下并回 503，超发不会无限累积。
			slog.Warn("invitation rate limiter consume failed after successful checks; allowing bounded overshoot", append(logger.RequestAttrs(r), "gate", string(gate.dimension), "error", err)...)
			continue
		}
		if !allowed {
			// 消费被拒说明「检查」到「消费」之间配额被别的副本抢先填满。同样选择继续
			// 而不是拒绝：前面可能已有闸扣过配额，此时再拒绝只会制造用户可见的失败，
			// 而放行造成的超发是有上限的（最多一次请求的额度）。
			continue
		}
	}
}

// writeInvitationLimiterUnavailable 回 503「限流服务暂不可用」：失败关闭路径的标准响应，
// 同时带 Cache-Control: no-store 与 Retry-After: 5。
func writeInvitationLimiterUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "5")
	writeErrorCode(w, http.StatusServiceUnavailable, "invitation_rate_limiter_unavailable", "invitation service temporarily unavailable")
}

// writeInvitationRateLimited 回 429「邀请太频繁」：把建议重试秒数放进 Retry-After 头与
// 响应体的 retry_after_seconds 字段，方便前端倒计时。Cache-Control: no-store 防止
// 代理缓存 429 响应后误伤后续请求。
func writeInvitationRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	retryAfterSeconds := int64((retryAfter + time.Second - 1) / time.Second)
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":               "too many invitation requests",
		"code":                "invitation_rate_limited",
		"retry_after_seconds": retryAfterSeconds,
	})
}
