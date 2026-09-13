package composio

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Composio 在每次 webhook 投递时都会带上的三个请求头名字。
const (
	HeaderWebhookID        = "webhook-id"
	HeaderWebhookTimestamp = "webhook-timestamp"
	HeaderWebhookSignature = "webhook-signature"
)

// DefaultWebhookTolerance 是默认的重放容忍窗口——与官方 Composio SDK 对齐
// （300 秒，见 Composio webhook 文档）。
const DefaultWebhookTolerance = 300 * time.Second

// [VerifyWebhook] 返回的哨兵错误，调用方可以用 errors.Is 区分失败原因。
var (
	ErrMissingWebhookHeaders   = errors.New("composio: missing webhook headers")
	ErrInvalidWebhookSignature = errors.New("composio: invalid webhook signature")
	ErrWebhookTimestampStale   = errors.New("composio: webhook timestamp outside tolerance")
	ErrWebhookSecretMissing    = errors.New("composio: webhook secret is empty")
)

// WebhookHeaders 装参与签名计算的三个请求头。直接把进来的 HTTP 请求头填进来即可。
type WebhookHeaders struct {
	ID        string
	Timestamp string
	Signature string
}

// HeadersFromHTTP 从 http.Header 里取出三个 webhook 头。
// http.Header 的键名不区分大小写（已统一归一化），所以这里直接按小写取。
func HeadersFromHTTP(h http.Header) WebhookHeaders {
	return WebhookHeaders{
		ID:        h.Get(HeaderWebhookID),
		Timestamp: h.Get(HeaderWebhookTimestamp),
		Signature: h.Get(HeaderWebhookSignature),
	}
}

// VerifyOptions 用来微调 [VerifyWebhook] 的行为。零值即默认值。
type VerifyOptions struct {
	// Tolerance 允许 webhook-timestamp 与「当前时间」最多差多远。
	// 0 表示用 [DefaultWebhookTolerance]；负值表示完全跳过时间校验
	// （只适合在测试里重放历史投递时用）。
	Tolerance time.Duration

	// Now 覆盖校验用的系统时钟。测试会用到；生产环境保持 nil。
	Now func() time.Time
}

// VerifyWebhook 校验 Composio 随每次 webhook 投递附上的 HMAC-SHA256 签名，
// 并强制一个重放容忍窗口。
//
// 参与签名的字符串按如下方式拼接：
//
//	"<webhook-id>.<webhook-timestamp>.<rawBody>"
//
// 用 secret 做 HMAC-SHA256，结果再 base64 编码。
//
// Composio 的 `webhook-signature` 头是一个逗号分隔的 `<版本>,<签名>` 对列表
// （例如 `v1,abc123…`）；本函数接受其中任何版本号以 "v" 开头的签名，
// 这样未来升级版本也能兼容。
//
// secret 必须是该 webhook 订阅对应的密钥——从 Composio 控制台或
// `GET /webhook_subscriptions/{id}` 接口拿。
func VerifyWebhook(secret string, headers WebhookHeaders, rawBody []byte, opts VerifyOptions) error {
	if secret == "" {
		return ErrWebhookSecretMissing
	}
	if headers.ID == "" || headers.Timestamp == "" || headers.Signature == "" {
		return ErrMissingWebhookHeaders
	}

	tolerance := opts.Tolerance
	if tolerance == 0 {
		tolerance = DefaultWebhookTolerance
	}
	if tolerance > 0 {
		ts, err := strconv.ParseInt(headers.Timestamp, 10, 64)
		if err != nil {
			// Composio 文档里时间戳是 Unix 秒；这里留一个回退，
			// 以防将来投递改用 RFC3339 格式。
			t, terr := time.Parse(time.RFC3339, headers.Timestamp)
			if terr != nil {
				return fmt.Errorf("composio: invalid webhook-timestamp %q: %w", headers.Timestamp, err)
			}
			ts = t.Unix()
		}
		now := time.Now().UTC()
		if opts.Now != nil {
			now = opts.Now().UTC()
		}
		delta := now.Sub(time.Unix(ts, 0))
		if delta < 0 {
			delta = -delta
		}
		if delta > tolerance {
			return fmt.Errorf("%w: drift=%s tolerance=%s", ErrWebhookTimestampStale, delta, tolerance)
		}
	}

	signingString := headers.ID + "." + headers.Timestamp + "." + string(rawBody)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingString))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Composio 的头形如 "v1,<sig>[ v2,<sig> ...]"——把逗号替换成空格后按字段
	// 切分，逐个接受带版本号的签名；同时也兼容裸 base64 形式，保证向前兼容。
	candidates := strings.Fields(strings.ReplaceAll(headers.Signature, ",", " "))
	if len(candidates) == 0 {
		return ErrInvalidWebhookSignature
	}
	want := []byte(expected)
	for _, cand := range candidates {
		// 跳过 "v1" / "v2" 这类版本标签。
		if len(cand) <= 3 && strings.HasPrefix(cand, "v") {
			continue
		}
		if hmac.Equal([]byte(cand), want) {
			return nil
		}
	}
	return ErrInvalidWebhookSignature
}

// VerifyHTTPRequest 是一个便捷封装：一次调用完成「读取并校验收到的
// *http.Request」。它会消费掉请求体，校验通过后再把 body 原样返回给调用方，
// 方便 handler 接着做 json 解析。
//
// 出错时（只要 body 读到了）返回的 body 也会带上，handler 可以选择记录日志。
func VerifyHTTPRequest(secret string, r *http.Request, opts VerifyOptions) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, errors.New("composio: VerifyHTTPRequest: request body is nil")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("composio: read webhook body: %w", err)
	}
	_ = r.Body.Close()
	if verr := VerifyWebhook(secret, HeadersFromHTTP(r.Header), body, opts); verr != nil {
		return body, verr
	}
	return body, nil
}

// --- 事件包络 -----------------------------------------------------------

// EventEnvelope 是 Composio 文档定义的 V3 webhook 负载。
//
// Spec: https://docs.composio.dev/docs/setting-up-triggers/subscribing-to-events#webhook-payload-versions
//
// `data` 与 `metadata` 两个块随事件类型而变；这里保留 json.RawMessage，
// 让调用方只把自己关心的 Type 解析成强类型结构体。
type EventEnvelope struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
}

// ParseEvent 解析 V3 事件包络。它不做签名校验——务必先调
// [VerifyWebhook] / [VerifyHTTPRequest]。
func ParseEvent(rawBody []byte) (*EventEnvelope, error) {
	var out EventEnvelope
	if err := json.Unmarshal(rawBody, &out); err != nil {
		return nil, fmt.Errorf("composio: parse webhook envelope: %w", err)
	}
	return &out, nil
}
