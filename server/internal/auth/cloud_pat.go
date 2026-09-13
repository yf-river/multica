// 本文件实现「Multica Cloud 节点 PAT（mcn_）」的远程校验器：收到 mcn_ token 后，
// 调 Multica Cloud Fleet 服务的 /api/v1/pat/verify 端点换取「token 归哪个用户
// owner_id 所有」等身份信息，并在 Redis 里缓存已通过的结果。
// token 的生命周期/状态/owner 绑定以 Cloud 为权威，本库不做本地判断。
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// CloudPATPrefix 是标识 mcn_（Multica Cloud Node）PAT 的字面前缀。带此前缀的 token
// 通过调用 Multica Cloud Fleet 服务校验，而不是查本地 personal_access_tokens 表——
// Cloud 是 token 生命周期、状态与 (owner_id, instance_id) 绑定的权威持有者。
const CloudPATPrefix = "mcn_"

// cloudPATCachePrefix 给云 PAT 缓存键加独立命名空间，与 mul_/mdt_ 的缓存键空间分开，
// 三类 token 不会意外共用键。末尾斜杠沿用 patCachePrefix / daemonTokenCachePrefix
// 的现有约定。
const cloudPATCachePrefix = "mul:auth:mcn:"

// cloudPATCacheTTL 限定一个已校验的 mcn_ token 在再次问 Fleet 前能缓存多久。
// Cloud 文档明确建议「如果上游确实要缓存，用 30–60s」——更长只会把吊销窗口拉大到
// Cloud 无法接受的程度。取上限 60s：短到被吊销/实例被终止的 PAT 约 1 分钟内失效，
// 长到节点上忙碌的 daemon/CLI 每个 token 每分钟只需调一次校验。
//
// 刻意不复用 AuthCacheTTL（10 分钟）——那是给我们自己 DB 支撑的 PAT/daemon-token
// 路径调的：那两条路径吊销时能直接失效缓存键。而 Cloud 吊销 PAT 时我们无从收到通知
// 去失效缓存，所以这个 TTL 本身就是吊销延迟的上界。
const cloudPATCacheTTL = 60 * time.Second

// cloudPATVerifyPath 是我们要 POST 的 Fleet 端点。Cloud 文档把它放在
// /api/v1/pat/verify 下；上游端点没有应用层鉴权（访问控制靠网络层 VPC 隔离），
// 所以我们直接把 token 明文发过去，由 Fleet 裁断。
const cloudPATVerifyPath = "/api/v1/pat/verify"

// cloudPATVerifyRequestMaxBytes 是文档记载的 Fleet 请求体硬上限（4 KiB）。
// 我们序列化后的请求远小于此——这个常量存在，是为了将来有人加可选字段时，
// 让意图可见。
const cloudPATVerifyRequestMaxBytes = 4 * 1024

// cloudPATVerifyResponseMaxBytes 限定我们会读多少 Fleet 响应。成功载荷远小于 1 KiB；
// 取 64 KiB 既远离病态响应，又不给「不守规矩的 Fleet 造成内存耗尽」留缺口。
const cloudPATVerifyResponseMaxBytes = 64 * 1024

// cloudPATDefaultTimeout 是调用方不提供 *http.Client 时，每次 verify 请求的 HTTP
// 超时。鉴权必须轻快：Fleet 应在几十毫秒内应答，一个挂死的 verify 会堵住后面所有
// 请求。比 cloudruntime 的 35s 紧——因为那条通道代理的是任意用户流量，而这里只做
// 一次小型 JSON 交换。
const cloudPATDefaultTimeout = 5 * time.Second

// 验证器的哨兵错误。调用方（Auth / DaemonAuth 中间件）按它们分支，把 Cloud 的结果
// 映射到 HTTP 状态码：
//
//   - ErrCloudPATInvalid       → 401（Fleet 说 token 无效）
//   - ErrCloudPATUnavailable   → 503（Fleet 不可达 / 5xx）
//   - ErrCloudPATNotConfigured → 401（服务器没配 Fleet URL；不暴露「mcn_ 受支持但被
//     关闭」这个事实——fail closed 避免把配错的机器当启用态处理）
var (
	ErrCloudPATInvalid       = errors.New("cloud pat invalid")
	ErrCloudPATUnavailable   = errors.New("cloud pat verifier unavailable")
	ErrCloudPATNotConfigured = errors.New("cloud pat verifier not configured")
)

// CloudPATIdentity 是一次成功 verify 的产物。只保留鉴权路径真正需要的字段：
//
//   - OwnerID 是这次请求所属的用户（映射成 X-User-ID）。
//   - InstanceID / InstanceRecordID 被记下来，供下游代码把请求与某个云节点对应；
//     目前不参与授权，但此刻就留存，能让线格式对「将来想断言特定实例绑定」的调用方
//     保持稳定。
//
// 刻意丢弃 token_last4、status、issued_at 等——那是诊断字段，不该进缓存的鉴权状态。
type CloudPATIdentity struct {
	OwnerID          string `json:"o"`
	InstanceID       string `json:"i"`
	InstanceRecordID string `json:"r"`
}

// CloudPATInvalidError 携带 Fleet 对 valid=false 响应报告的原因。中间件用它记日志
// 说明 mcn_ token 为何被拒，却不把原因暴露在 401 响应体里——按 Cloud 文档，
// 调用方不应区分 token_not_found 与 token_revoked 做安全决策。
//
// "owner_unknown" 这个原因也由 Verify 在本地产出：Cloud 接受了 token，但它返回的
// owner_id 在我们的数据库里对不上真实用户。把它当作 Cloud 风格的 "invalid" 处理，
// 让中间件的响应形态保持统一——结果一样：401，丢弃该 token。
type CloudPATInvalidError struct {
	Reason string
}

// Error 实现 error 接口；无原因时给通用文案。
func (e *CloudPATInvalidError) Error() string {
	if e == nil || e.Reason == "" {
		return "cloud pat invalid"
	}
	return "cloud pat invalid: " + e.Reason
}

// Is 让 errors.Is(err, ErrCloudPATInvalid) 能匹配任意 CloudPATInvalidError——
// 调用方按类别分支即可，不必关心具体原因串。
func (e *CloudPATInvalidError) Is(target error) bool {
	return target == ErrCloudPATInvalid
}

// CloudPATInvalidReasonOwnerUnknown 是合成的拒绝原因：Cloud 已验证 token，但返回的
// owner_id 在本地 users 表里找不到。Cloud 的 `owner_id` 与我们的 `users.id` 按契约
// 共享同一 UUID 空间；对不上说明：要么用户在我们这侧、节点签发之后被删了，要么
// （更糟）有人冒充 Cloud、试图抛出一个伪造的 owner_id。两种情况都必须拒绝请求。
const CloudPATInvalidReasonOwnerUnknown = "owner_unknown"

// OwnerLookupFunc 是 Verify 在缓存/返回身份之前，拿 Cloud 的 owner_id 做的
// 「本地用户是否存在」检查。调用方（通常是中间件闭包）把它接到 queries.GetUser。
//
// 返回语义：
//   - (true, nil)  → owner_id 是合法本地用户；Verify 返回成功。
//   - (false, nil) → owner_id 本地不存在；Verify 返回 ErrCloudPATInvalid 且
//     reason="owner_unknown"，并且不缓存（用户可能稍后被重建，不能因为一条缓存
//     把这个重试锁死一个 TTL 窗口）。
//   - (_, err)     → 基础设施错误（DB 不可达等）；Verify 包成 ErrCloudPATUnavailable，
//     让调用方回 503 而非 401。
type OwnerLookupFunc func(ctx context.Context, ownerID string) (bool, error)

// CloudPATVerifier 通过 POST <fleetURL>/api/v1/pat/verify 解析 mcn_ PAT，
// 并在 Redis 里缓存已通过的结果（TTL 为 cloudPATCacheTTL）。
//
// nil 的 *CloudPATVerifier 也可安全使用——Verify 返回 ErrCloudPATNotConfigured。
// Auth/DaemonAuth 中间件把「verifier 为 nil」与「fleet URL 为空」等同看待，
// 所以没配 MULTICA_CLOUD_URL 的服务器会在前缀分支直接拒绝 mcn_ token，
// 而不是解引用 nil。
type CloudPATVerifier struct {
	baseURL string
	http    *http.Client
	rdb     *redis.Client // 可为 nil——此时禁用缓存
}

// CloudPATVerifierConfig 组装 NewCloudPATVerifier 的依赖。做成结构体（而非位置参数）
// 是为将来的旋钮（自定义 TTL、expected_owner_id 绑定）留余地，而不必改动每个调用点。
type CloudPATVerifierConfig struct {
	// FleetBaseURL 是 Cloud Fleet 的基地址（如 https://fleet.multica.cloud），
	// 末尾斜杠会被去掉。为空即禁用验证器——NewCloudPATVerifier 返回 nil。
	FleetBaseURL string

	// HTTPClient 是 verify 请求用的客户端。可选——为 nil 时会创建一个带
	// cloudPATDefaultTimeout 的客户端。想用连接池/按部署调传输层时，传共享客户端。
	HTTPClient *http.Client

	// Redis 支撑「已通过结果」的缓存。nil 即禁用缓存——每次 Verify 都打 Fleet。
	// 与 PATCache / DaemonTokenCache 相同的 nil 安全契约。
	Redis *redis.Client
}

// NewCloudPATVerifier 为 cfg.FleetBaseURL 构造验证器。URL 去空白并去掉尾部斜杠后
// 为空则返回 nil——调用方（router/中间件）把 nil 当作「该部署不支持 mcn_」。
func NewCloudPATVerifier(cfg CloudPATVerifierConfig) *CloudPATVerifier {
	base := strings.TrimRight(strings.TrimSpace(cfg.FleetBaseURL), "/")
	if base == "" {
		return nil
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cloudPATDefaultTimeout}
	}
	return &CloudPATVerifier{
		baseURL: base,
		http:    client,
		rdb:     cfg.Redis,
	}
}

// Configured 报告验证器是否有 Fleet URL。给遥测用的便捷方法——nil 接收者也返回
// false。中间件在热路径上用普通 nil 判断而不是这个方法。
func (v *CloudPATVerifier) Configured() bool {
	return v != nil && v.baseURL != ""
}

// Verify 把 token 解析成 CloudPATIdentity，依次走：
//
//  1. Redis 缓存，键为 sha256(token)。命中 → 直接返回缓存身份，既不打 Fleet 也不查
//     库。缓存里只可能是「已经同时通过 Cloud 校验 + 本地 owner 存在性检查」的身份，
//     所以缓存命中即完整放行。
//  2. Fleet POST /api/v1/pat/verify。响应分三种：
//     - HTTP 200 + valid=true   → 进入第 3 步
//     - HTTP 200 + valid=false  → CloudPATInvalidError{Reason:...}
//     （经 Is 同时可被 errors.Is 判为 ErrCloudPATInvalid）
//     - HTTP 4xx/5xx、网络、超时、解码失败 → ErrCloudPATUnavailable
//  3. 经 lookup(owner_id) 做本地 owner 存在性检查：
//     - 存在  → 缓存 + 返回成功
//     - 缺失  → ErrCloudPATInvalid（reason="owner_unknown"），不缓存
//     - 出错  → ErrCloudPATUnavailable
//
// `lookup` 可以为 nil——Verify 此时跳过第 3 步。生产调用方（Auth / DaemonAuth）
// 总会提供；nil 模式供与 DB 隔离地测试验证器的单元测试用。
//
// 刻意不缓存 valid=false 或 owner_unknown 这类否定结果——按 Cloud 文档，否定结果
// 可能翻回肯定（懒吊销对账、之后我们库里才建出 owner），一条过期的否定会把刚铸造的
// token 在 TTL 窗口内永久锁死。
//
// nil 接收者时返回 ErrCloudPATNotConfigured，让没配 Fleet URL 的部署也能把 mcn_
// token 干净地映射成 401。
func (v *CloudPATVerifier) Verify(ctx context.Context, token string, lookup OwnerLookupFunc) (CloudPATIdentity, error) {
	if v == nil || v.baseURL == "" {
		return CloudPATIdentity{}, ErrCloudPATNotConfigured
	}
	if token == "" {
		return CloudPATIdentity{}, ErrCloudPATInvalid
	}

	hash := HashToken(token)
	if id, ok := v.cacheGet(ctx, hash); ok {
		return id, nil
	}

	id, err := v.fetch(ctx, token)
	if err != nil {
		return CloudPATIdentity{}, err
	}

	if lookup != nil {
		exists, lookupErr := lookup(ctx, id.OwnerID)
		if lookupErr != nil {
			// DB/基础设施错误与 Cloud 故障同等对待：回 503 让调用方重试，而不是
			// 把一个仍然有效的 token 扔掉。这里不写缓存，所以瞬时抖动会在下一个请求
			// 自愈。
			slog.Warn("cloud_pat: owner lookup failed; treating as unavailable", "error", lookupErr)
			return CloudPATIdentity{}, ErrCloudPATUnavailable
		}
		if !exists {
			// Cloud 接受了 token，但它返回的 owner_id 不是我们认识的用户。不缓存直接
			// 拒绝——若该用户之后被创建，下一个请求不必等 TTL 过期就能成功。
			slog.Warn("cloud_pat: cloud-verified owner_id has no local user", "owner_id", id.OwnerID)
			return CloudPATIdentity{}, &CloudPATInvalidError{Reason: CloudPATInvalidReasonOwnerUnknown}
		}
	}

	v.cacheSet(ctx, hash, id)
	return id, nil
}

// fleetVerifyRequest 对应 Cloud 文档的请求结构。目前只发 `token`——`expected_owner_id`
// / `expected_instance_id` 能用来让验证器拒绝「绑定到别的用户」的 token，但在这层我们
// 还不知道请求自称的用户是谁。把它们接进来是未来的加固项。
type fleetVerifyRequest struct {
	Token string `json:"token"`
}

// fleetVerifyResponse 是并集式的响应结构。`Valid` 区分两个分支；valid:false 时只有
// `Reason` 有意义（按 Cloud 文档，mismatch 响应刻意省略绑定信息，避免变成探测 oracle）。
type fleetVerifyResponse struct {
	Valid            bool   `json:"valid"`
	Reason           string `json:"reason,omitempty"`
	OwnerID          string `json:"owner_id,omitempty"`
	InstanceID       string `json:"instance_id,omitempty"`
	InstanceRecordID string `json:"instance_record_id,omitempty"`
}

// fetch 发起一次到 Fleet 的 verify POST 并解析响应身份。任何异常都归为
// ErrCloudPATUnavailable 或带原因的 CloudPATInvalidError（见函数内注释）。
func (v *CloudPATVerifier) fetch(ctx context.Context, token string) (CloudPATIdentity, error) {
	body, err := json.Marshal(fleetVerifyRequest{Token: token})
	if err != nil {
		// 固定结构 + 一个字符串字段的 json.Marshal 实际不可能失败；归到 Unavailable
		// 是为了让调用方始终走「当作云错误处理」的分支。
		return CloudPATIdentity{}, fmt.Errorf("%w: marshal request: %v", ErrCloudPATUnavailable, err)
	}
	if len(body) > cloudPATVerifyRequestMaxBytes {
		// 纵深防御：只有将来有人加 expected_* 字段并传入病态输入时才会触发。
		return CloudPATIdentity{}, fmt.Errorf("%w: request body exceeds %d bytes", ErrCloudPATUnavailable, cloudPATVerifyRequestMaxBytes)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.baseURL+cloudPATVerifyPath, bytes.NewReader(body))
	if err != nil {
		return CloudPATIdentity{}, fmt.Errorf("%w: build request: %v", ErrCloudPATUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		// 网络错误/超时/DNS/拨号失败。刻意不把 err.Error() 暴露给 HTTP 响应——让
		// 中间件回笼统的 503。本地日志仍能在调用点经 slog 看到真实原因。
		slog.Warn("cloud_pat: verify request failed", "error", err)
		return CloudPATIdentity{}, ErrCloudPATUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 按 Cloud 文档：400 是我们请求写错，500 是 Fleet 自身坏了。无论哪种，
		// 上游的正确动作都是「此刻把 token 当作无法校验，回 503」。读一小段响应体
		// 只为日志上下文。
		var snippet string
		if buf, _ := io.ReadAll(io.LimitReader(resp.Body, 512)); len(buf) > 0 {
			snippet = strings.TrimSpace(string(buf))
		}
		slog.Warn("cloud_pat: verify returned non-200", "status", resp.StatusCode, "body", snippet)
		return CloudPATIdentity{}, ErrCloudPATUnavailable
	}

	// 限长读取：+1 是为了能区分「正好 64KiB」与「超过上限」。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, cloudPATVerifyResponseMaxBytes+1))
	if err != nil {
		slog.Warn("cloud_pat: read response failed", "error", err)
		return CloudPATIdentity{}, ErrCloudPATUnavailable
	}
	if len(raw) > cloudPATVerifyResponseMaxBytes {
		slog.Warn("cloud_pat: verify response too large", "limit", cloudPATVerifyResponseMaxBytes)
		return CloudPATIdentity{}, ErrCloudPATUnavailable
	}

	var parsed fleetVerifyResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		slog.Warn("cloud_pat: decode response failed", "error", err)
		return CloudPATIdentity{}, ErrCloudPATUnavailable
	}

	if !parsed.Valid {
		// 把原因带进错误对象，让中间件能记录「为什么」，同时对客户端仍回笼统的 401。
		return CloudPATIdentity{}, &CloudPATInvalidError{Reason: parsed.Reason}
	}

	if parsed.OwnerID == "" {
		// 防御：Fleet 声称 valid:true 却漏了 owner_id——没有 owner_id，X-User-ID 就没
		// 内容可写，等于不可用。宁可 fail closed 也不把空用户 ID 传给下游。
		slog.Warn("cloud_pat: verify returned valid=true with empty owner_id")
		return CloudPATIdentity{}, ErrCloudPATUnavailable
	}

	return CloudPATIdentity{
		OwnerID:          parsed.OwnerID,
		InstanceID:       parsed.InstanceID,
		InstanceRecordID: parsed.InstanceRecordID,
	}, nil
}

// cloudPATCacheKey 拼缓存键：固定前缀 + token 哈希。
func cloudPATCacheKey(hash string) string { return cloudPATCachePrefix + hash }

// cacheGet 读缓存身份。任何 Redis 错误/解码失败都按 miss 处理，落回 Fleet。
func (v *CloudPATVerifier) cacheGet(ctx context.Context, hash string) (CloudPATIdentity, bool) {
	if v == nil || v.rdb == nil {
		return CloudPATIdentity{}, false
	}
	raw, err := v.rdb.Get(ctx, cloudPATCacheKey(hash)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			slog.Warn("cloud_pat: cache get failed; falling back to fleet", "error", err)
		}
		return CloudPATIdentity{}, false
	}
	var id CloudPATIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		slog.Warn("cloud_pat: cache entry malformed; falling back to fleet", "error", err)
		return CloudPATIdentity{}, false
	}
	if id.OwnerID == "" {
		// 安全网：没有 owner_id 的畸形缓存条目（如旧 schema 遗留）绝不能算命中，
		// 否则中间件会把 X-User-ID 设成空串。
		return CloudPATIdentity{}, false
	}
	return id, true
}

// cacheSet 写入缓存；写失败只记日志（缓存写失败不是鉴权失败）。
func (v *CloudPATVerifier) cacheSet(ctx context.Context, hash string, id CloudPATIdentity) {
	if v == nil || v.rdb == nil {
		return
	}
	raw, err := json.Marshal(id)
	if err != nil {
		slog.Warn("cloud_pat: cache marshal failed", "error", err)
		return
	}
	if err := v.rdb.Set(ctx, cloudPATCacheKey(hash), raw, cloudPATCacheTTL).Err(); err != nil {
		slog.Warn("cloud_pat: cache set failed", "error", err)
	}
}
