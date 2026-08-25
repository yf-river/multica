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

// postHogClient ships events to PostHog's /batch/ endpoint. It enqueues events
// into a bounded buffer (non-blocking Capture) and flushes them from a
// background worker.
type postHogClient struct {
	apiKey      string
	host        string
	environment string
	ch          chan Event
	done        chan struct{}
	wg          sync.WaitGroup
	http        *http.Client

	dropped atomic.Uint64 // events dropped because the queue was full
	sent    atomic.Uint64
	failed  atomic.Uint64
}

// newPostHogClient starts the background flush worker. Caller must call Close
// on shutdown to drain pending events.
func newPostHogClient(apiKey, host, environment string) *postHogClient {
	c := &postHogClient{
		apiKey:      apiKey,
		host:        host,
		environment: environment,
		ch:          make(chan Event, defaultQueueSize),
		done:        make(chan struct{}),
		http:        &http.Client{Timeout: defaultFlushTimeout},
	}
	c.wg.Add(1)
	go c.run()
	return c
}

// Capture enqueues an event. Returns immediately; on a full queue the event
// is dropped and counted. Analytics must never block a request handler.
func (c *postHogClient) Capture(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	select {
	case c.ch <- e:
	default:
		n := c.dropped.Add(1)
		// Log periodically — every 100 drops — so a broken pipe is visible but
		// doesn't spam logs under sustained load.
		if n%100 == 1 {
			slog.Warn("analytics: queue full, dropping event", "event", e.Name, "total_dropped", n)
		}
	}
}

// Close stops accepting events and drains whatever is already queued.
func (c *postHogClient) Close() {
	close(c.done)
	c.wg.Wait()
	slog.Info("analytics: posthog client closed",
		"sent", c.sent.Load(),
		"dropped", c.dropped.Load(),
		"failed", c.failed.Load(),
	)
}

func (c *postHogClient) run() {
	defer c.wg.Done()
	ticker := time.NewTicker(defaultFlushEvery)
	defer ticker.Stop()

	batch := make([]Event, 0, defaultBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		c.send(batch)
		batch = batch[:0]
	}
	appendEvent := func(e Event) {
		batch = append(batch, e)
		if len(batch) >= defaultBatchSize {
			flush()
		}
	}

	for {
		select {
		case e := <-c.ch:
			appendEvent(e)
		case <-ticker.C:
			flush()
		case <-c.done:
			// Drain remaining events. The channel is not closed by Close() to
			// avoid racing with Capture, so we loop until it's empty.
			for {
				select {
				case e := <-c.ch:
					appendEvent(e)
				default:
					flush()
					return
				}
			}
		}
	}
}

// capturePayload mirrors the PostHog /batch/ JSON shape.
type capturePayload struct {
	APIKey string        `json:"api_key"`
	Batch  []captureItem `json:"batch"`
}

type captureItem struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
	Timestamp  string         `json:"timestamp"`
}

func (c *postHogClient) send(batch []Event) {
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
		props["environment"] = c.environment
		if _, ok := props["is_demo"]; !ok {
			props["is_demo"] = false
		}
		if _, ok := props["user_id"]; !ok && e.DistinctID != "" && !strings.Contains(e.DistinctID, ":") {
			props["user_id"] = e.DistinctID
		}
		if len(e.SetOnce) > 0 {
			props["$set_once"] = e.SetOnce
		}
		items = append(items, captureItem{
			Event:      e.Name,
			DistinctID: e.DistinctID,
			Properties: props,
			Timestamp:  e.Timestamp.UTC().Format(time.RFC3339Nano),
		})
	}

	body, err := json.Marshal(capturePayload{APIKey: c.apiKey, Batch: items})
	if err != nil {
		c.failed.Add(uint64(len(batch)))
		slog.Error("analytics: marshal batch", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultFlushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/batch/", bytes.NewReader(body))
	if err != nil {
		c.failed.Add(uint64(len(batch)))
		slog.Error("analytics: build request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		c.failed.Add(uint64(len(batch)))
		slog.Warn("analytics: send batch failed", "error", err, "events", len(batch))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		c.failed.Add(uint64(len(batch)))
		slog.Warn("analytics: posthog rejected batch", "status", resp.StatusCode, "events", len(batch))
		return
	}
	c.sent.Add(uint64(len(batch)))
}
