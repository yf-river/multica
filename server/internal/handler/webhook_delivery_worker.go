// 本文件实现 webhook 投递的持久化 worker：把 webhook_delivery 表里「排队中」的行认领
// 出来，做限流、加载 trigger/autopilot、重放校验与事件过滤，最后交给
// AutopilotService 真正跑一次 autopilot run。
//
// 队列与租约都在 Postgres 里（SKIP LOCKED 认领），进程重启或副本故障只会回收过期行；
// 内存里的 notify channel 只是降低延迟的提示，不是正确性来源。worker 池有界
// （webhookWorkerConcurrency=4）；单条投递最多重试 webhookWorkerMaxAttempts=5 次，
// 用指数退避重排。迟到的 worker 输掉租约时（handleWebhookLeaseMutation）既不报错也
// 不记指标——终态迁移归新主人负责。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// worker 调参：轮询间隔、单条投递最大派发尝试次数、进程内 worker 并发数。
const (
	webhookWorkerPollInterval = time.Second
	webhookWorkerMaxAttempts  = 5
	webhookWorkerConcurrency  = 4
)

// WebhookDeliveryWorker 拥有排队中 webhook 投递的派发。队列与租约都在 Postgres，
// 因此进程重启或副本故障只是回收过期行；内存 notify 只是延迟提示。
type WebhookDeliveryWorker struct {
	h      *Handler
	notify chan struct{}
	done   chan struct{}
}

// NewWebhookDeliveryWorker 构造 worker。notify 缓冲区为并发数大小，避免唤醒风暴。
func NewWebhookDeliveryWorker(h *Handler) *WebhookDeliveryWorker {
	return &WebhookDeliveryWorker{
		h:      h,
		notify: make(chan struct{}, webhookWorkerConcurrency),
		done:   make(chan struct{}),
	}
}

// Notify 在本地有新投递入队时唤醒空闲 worker（非阻塞；缓冲满则丢弃，ticker 兜底）。
func (w *WebhookDeliveryWorker) Notify() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// Run 拥有一个有界的进程内 worker 池。SKIP LOCKED 把排队投递分发给本进程的 worker 与
// 其它服务器副本。ProcessNext 对包内公开，供 handler 测试无需起 goroutine 就能同步
// 驱动持久队列。
func (w *WebhookDeliveryWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	defer close(w.done)
	if w.h == nil || w.h.Queries == nil {
		return
	}

	var workers sync.WaitGroup
	workers.Add(webhookWorkerConcurrency)
	for range webhookWorkerConcurrency {
		go func() {
			defer workers.Done()
			w.runLoop(ctx)
		}()
	}
	workers.Wait()
}

// runLoop 既当恢复清扫器定期轮询，也会在本地入队后立刻被唤醒。每个 loop 有自己的
// ticker，这样即使突发通知被合并，空闲池仍能各自展开。
func (w *WebhookDeliveryWorker) runLoop(ctx context.Context) {
	ticker := time.NewTicker(webhookWorkerPollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("webhook worker: process delivery", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
		case <-ticker.C:
		}
	}
}

// WaitWithTimeout 等待 Run 结束；超时返回 false。主要供测试或优雅停机使用。
func (w *WebhookDeliveryWorker) WaitWithTimeout(timeout time.Duration) bool {
	if w == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		return false
	}
}

// ProcessNext 认领并处理至多一条排队投递，返回是否处理了工作。核心流程：认领 →
// 触发级限流（撞限就 defer 重排）→ 装载 trigger/autopilot 并校验归属 → 规范化存储的
// 载荷 → 视已认领状态做事件过滤 → DispatchAutopilotForWebhookDelivery 派发 → 按结果
// complete / retry / fail。
func (w *WebhookDeliveryWorker) ProcessNext(ctx context.Context) (bool, error) {
	delivery, err := w.h.Queries.ClaimQueuedWebhookDelivery(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim queued delivery: %w", err)
	}

	if w.h.WebhookRateLimiter != nil {
		key := uuidToString(delivery.TriggerID)
		if !w.h.WebhookRateLimiter.Allow(ctx, key) {
			w.h.Metrics.RecordWebhookRateLimited("worker_trigger")
			retryAfter := slidingWindowLimiterRetryAfter(ctx, w.h.WebhookRateLimiter, key)
			if retryAfter <= 0 {
				retryAfter = time.Second
			}
			_, err := w.h.Queries.DeferClaimedWebhookDelivery(ctx, db.DeferClaimedWebhookDeliveryParams{
				ID:          delivery.ID,
				LeaseToken:  delivery.LeaseToken,
				AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(retryAfter), Valid: true},
			})
			_, err = handleWebhookLeaseMutation("defer", delivery, err)
			return true, err
		}
	}

	trigger, err := w.h.Queries.GetAutopilotTrigger(ctx, delivery.TriggerID)
	if err != nil {
		return true, w.retryOrFail(ctx, delivery, fmt.Errorf("load trigger: %w", err))
	}
	autopilot, err := w.h.Queries.GetAutopilot(ctx, delivery.AutopilotID)
	if err != nil {
		return true, w.retryOrFail(ctx, delivery, fmt.Errorf("load autopilot: %w", err))
	}
	if uuidToString(trigger.AutopilotID) != uuidToString(delivery.AutopilotID) ||
		uuidToString(autopilot.WorkspaceID) != uuidToString(delivery.WorkspaceID) {
		return true, w.complete(ctx, delivery, deliveryStatusFailed, pgtype.UUID{}, "delivery ownership mismatch")
	}

	headers := headersFromSelected(delivery.SelectedHeaders)
	if delivery.ContentType.Valid {
		headers.Set("Content-Type", delivery.ContentType.String)
	}
	envelope, err := normalizeWebhookPayload(delivery.RawBody, headers)
	if err != nil {
		return true, w.complete(ctx, delivery, deliveryStatusFailed, pgtype.UUID{}, "normalize stored body: "+err.Error())
	}
	if delivery.ReceivedAt.Valid {
		envelope.Request.ReceivedAt = delivery.ReceivedAt.Time.UTC().Format(time.RFC3339)
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return true, w.retryOrFail(ctx, delivery, fmt.Errorf("encode envelope: %w", err))
	}

	// 一旦入站侧已同步准入一次运行并返回 accepted/skipped，那个决定就是持久的。只在
	// 「准入前崩溃窗口」恢复出来的投递上才需要重查可变的 trigger/autopilot 状态；否则
	// 响应后立刻暂停可能让一次运行悬空。
	_, admissionErr := w.h.Queries.GetAutopilotRunByWebhookDelivery(ctx, delivery.ID)
	hasAdmittedRun := admissionErr == nil
	if admissionErr != nil && !errors.Is(admissionErr, pgx.ErrNoRows) {
		return true, w.retryOrFail(ctx, delivery, fmt.Errorf("load admitted run: %w", admissionErr))
	}
	if !hasAdmittedRun {
		switch {
		case !trigger.Enabled:
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, "trigger_disabled")
		case autopilot.Status == "archived":
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, "autopilot_archived")
		case autopilot.Status != "active":
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, "autopilot_paused")
		case !webhookEventAllowedByTriggerScope(trigger.EventFilters, envelope):
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, "event_filtered")
		}
	}

	run, dispatchErr := w.h.AutopilotService.DispatchAutopilotForWebhookDelivery(
		ctx,
		autopilot,
		trigger.ID,
		payload,
		delivery.ID,
	)
	if dispatchErr != nil {
		var quotaErr *service.AutopilotQuotaExceededError
		if errors.As(dispatchErr, &quotaErr) {
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, quotaErr.Error(), "quota_exceeded")
		}
		if run != nil {
			return true, w.complete(ctx, delivery, deliveryStatusFailed, run.ID, dispatchErr.Error())
		}
		return true, w.retryOrFail(ctx, delivery, dispatchErr)
	}
	if run.Status == "failed" {
		reason := "autopilot run failed"
		if run.FailureReason.Valid {
			reason = run.FailureReason.String
		}
		return true, w.complete(ctx, delivery, deliveryStatusFailed, run.ID, reason)
	}

	if err := w.h.Queries.TouchAutopilotTriggerFiredAt(ctx, trigger.ID); err != nil {
		slog.Warn("webhook worker: touch last_fired_at",
			"delivery_id", uuidToString(delivery.ID),
			"trigger_id", uuidToString(trigger.ID),
			"error", err,
		)
	}
	return true, w.complete(ctx, delivery, deliveryStatusDispatched, run.ID, "")
}

// complete 把投递行终结为给定状态（dispatched/failed/ignored 等），可附 run id、
// 错误信息与稳定 reason code；租约已易主时静默返回 nil。
func (w *WebhookDeliveryWorker) complete(
	ctx context.Context,
	delivery db.WebhookDelivery,
	status string,
	runID pgtype.UUID,
	reason string,
	reasonCode ...string,
) error {
	params := db.CompleteClaimedWebhookDeliveryParams{
		ID:             delivery.ID,
		LeaseToken:     delivery.LeaseToken,
		Status:         status,
		AutopilotRunID: runID,
	}
	if reason != "" {
		params.Error = pgtype.Text{String: reason, Valid: true}
	}
	if len(reasonCode) > 0 && reasonCode[0] != "" {
		params.ReasonCode = pgtype.Text{String: reasonCode[0], Valid: true}
	}
	_, err := w.h.Queries.CompleteClaimedWebhookDelivery(ctx, params)
	lost, err := handleWebhookLeaseMutation("complete", delivery, err)
	if err != nil {
		return err
	}
	if lost {
		return nil
	}
	w.h.Metrics.RecordWebhookDelivery(delivery.Provider, status)
	return nil
}

// retryOrFail 在尝试次数没超限时把投递按指数退避重新排回队列；超限则直接 complete
// 为 failed。
func (w *WebhookDeliveryWorker) retryOrFail(ctx context.Context, delivery db.WebhookDelivery, cause error) error {
	if delivery.DispatchAttempts+1 >= webhookWorkerMaxAttempts {
		return w.complete(ctx, delivery, deliveryStatusFailed, pgtype.UUID{}, cause.Error())
	}
	backoff := time.Second * time.Duration(1<<min(delivery.DispatchAttempts, 6))
	_, err := w.h.Queries.RetryClaimedWebhookDelivery(ctx, db.RetryClaimedWebhookDeliveryParams{
		ID:          delivery.ID,
		LeaseToken:  delivery.LeaseToken,
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(backoff), Valid: true},
		Error:       pgtype.Text{String: cause.Error(), Valid: true},
	})
	lost, mutationErr := handleWebhookLeaseMutation("retry", delivery, err)
	if mutationErr != nil {
		return fmt.Errorf("retry claimed delivery after %v: %w", cause, mutationErr)
	}
	if lost {
		return nil
	}
	slog.Warn("webhook worker: delivery deferred",
		"delivery_id", uuidToString(delivery.ID),
		"attempt", delivery.DispatchAttempts+1,
		"backoff", backoff,
		"error", cause,
	)
	return nil
}

// handleWebhookLeaseMutation 归一化「慢 worker 活过了租约、新主人已认领或终结该行」
// 的预期竞态：终态迁移归新主人负责，所以迟到 worker 既不能报错，也不能记完成指标。
func handleWebhookLeaseMutation(operation string, delivery db.WebhookDelivery, err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Debug("webhook worker: lease ownership changed",
			"operation", operation,
			"delivery_id", uuidToString(delivery.ID),
		)
		return true, nil
	}
	return false, fmt.Errorf("%s claimed delivery: %w", operation, err)
}
