// posthog.go 实现走 PostHog /batch/ 端点的真实 Client。
package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultQueueSize    = 1024
	defaultBatchSize    = 64
	defaultFlushEvery   = 10 * time.Second
	defaultFlushTimeout = 5 * time.Second
)

// PostHogConfig 配置真实的 PostHog client。
type PostHogConfig struct {
	APIKey      string
	Host        string
	Environment string

	// 可选覆盖项。零值会回退到合理默认值。
	QueueSize  int
	BatchSize  int
	FlushEvery time.Duration
	HTTPClient *http.Client
}

// PostHogClient 把事件投递到 PostHog 的 /batch/ 端点。事件先进有界缓冲
// （Capture 非阻塞），再由后台 worker 批量刷出。
type PostHogClient struct {
	cfg  PostHogConfig
	ch   chan Event
	done chan struct{}
	wg   sync.WaitGroup

	dropped atomic.Uint64 // 队列满被丢弃的事件数
	sent    atomic.Uint64
	failed  atomic.Uint64
}

// NewPostHogClient 启动后台刷出 worker。停机时调用方必须 Close 以排空待发事件。
func NewPostHogClient(cfg PostHogConfig) *PostHogClient {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = defaultFlushEvery
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: defaultFlushTimeout}
	}
	if cfg.Environment == "" {
		cfg.Environment = EnvironmentFromEnv()
	}
	c := &PostHogClient{
		cfg:  cfg,
		ch:   make(chan Event, cfg.QueueSize),
		done: make(chan struct{}),
	}
	c.wg.Add(1)
	go c.run()
	return c
}

// Capture 入队一条事件并立即返回；队列满时丢弃该事件并计数。
// 分析绝不能阻塞请求 handler。
func (c *PostHogClient) Capture(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	select {
	case c.ch <- e:
	default:
		n := c.dropped.Add(1)
		// 每丢 100 条记一次日志——让"管道坏了"可见，但持续高负载下不会刷屏。
		if n%100 == 1 {
			slog.Warn("analytics: queue full, dropping event", "event", e.Name, "total_dropped", n)
		}
	}
}

// Close 停止接收新事件，并排空已排队的事件。
func (c *PostHogClient) Close() {
	close(c.done)
	c.wg.Wait()
	slog.Info("analytics: posthog client closed",
		"sent", c.sent.Load(),
		"dropped", c.dropped.Load(),
		"failed", c.failed.Load(),
	)
}

// run 是后台 worker：攒批到 BatchSize 或定时（FlushEvery）就刷出，收到 done 时
// 把剩余事件排空后退出。
func (c *PostHogClient) run() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.cfg.FlushEvery)
	defer ticker.Stop()

	batch := make([]Event, 0, c.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		c.send(batch)
		batch = batch[:0]
	}

	for {
		select {
		case e := <-c.ch:
			batch = append(batch, e)
			if len(batch) >= c.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-c.done:
			// 排空剩余事件。Close 不关 channel，是为了避免与 Capture 竞争；
			// 因此这里循环读到空为止。
			for {
				select {
				case e := <-c.ch:
					batch = append(batch, e)
					if len(batch) >= c.cfg.BatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// capturePayload 对应 PostHog /batch/ 的 JSON 形状。
type capturePayload struct {
	APIKey string        `json:"api_key"`
	Batch  []captureItem `json:"batch"`
}

// captureItem 是 /batch/ 里的单条事件载荷。
type captureItem struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
	Timestamp  string         `json:"timestamp"`
}

// send 把一批事件序列化并 POST 到 PostHog；任一环节失败都记入 failed 计数。
func (c *PostHogClient) send(batch []Event) {
	items := make([]captureItem, 0, len(batch))
	for _, e := range batch {
		props := make(map[string]any, len(e.Properties)+2)
		for k, v := range e.Properties {
			props[k] = v
		}
		if e.WorkspaceID != "" {
			props["workspace_id"] = e.WorkspaceID
		}
		props["event_schema_version"] = EventSchemaVersion
		props["environment"] = c.cfg.Environment
		if _, ok := props["is_demo"]; !ok {
			props["is_demo"] = false
		}
		if _, ok := props["user_id"]; !ok && e.DistinctID != "" && !strings.Contains(e.DistinctID, ":") {
			props["user_id"] = e.DistinctID
		}
		if len(e.SetOnce) > 0 {
			props["$set_once"] = e.SetOnce
		}
		if len(e.Set) > 0 {
			props["$set"] = e.Set
		}
		items = append(items, captureItem{
			Event:      e.Name,
			DistinctID: e.DistinctID,
			Properties: props,
			Timestamp:  e.Timestamp.UTC().Format(time.RFC3339Nano),
		})
	}

	body, err := json.Marshal(capturePayload{APIKey: c.cfg.APIKey, Batch: items})
	if err != nil {
		c.failed.Add(uint64(len(batch)))
		slog.Error("analytics: marshal batch", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultFlushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Host+"/batch/", bytes.NewReader(body))
	if err != nil {
		c.failed.Add(uint64(len(batch)))
		slog.Error("analytics: build request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		c.failed.Add(uint64(len(batch)))
		slog.Warn("analytics: send batch failed", "error", err, "events", len(batch))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		c.failed.Add(uint64(len(batch)))
		slog.Warn("analytics: posthog rejected batch", "status", resp.StatusCode, "events", len(batch))
		return
	}
	c.sent.Add(uint64(len(batch)))
}
