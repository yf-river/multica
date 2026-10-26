package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (c *Client) RegisterTaskRuntimeCapability(ctx context.Context, task Task, capabilities []string) ([]string, error) {
	var response struct {
		Capabilities []string `json:"capabilities"`
	}
	err := c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/runtime-inputs/capabilities", task.ID), struct {
		protocol.RuntimeInputClaim
		Capabilities []string `json:"capabilities"`
	}{protocol.RuntimeInputClaim{RuntimeID: task.RuntimeID, DispatchedAt: task.DispatchedAt}, capabilities}, &response)
	if err != nil {
		return nil, err
	}
	if len(response.Capabilities) != len(capabilities) {
		return nil, fmt.Errorf("server did not confirm runtime capabilities")
	}
	seen := map[string]bool{}
	for _, value := range response.Capabilities {
		if seen[value] || !slices.Contains(capabilities, value) {
			return nil, fmt.Errorf("server confirmed unexpected runtime capability")
		}
		seen[value] = true
	}
	return response.Capabilities, nil
}

func (c *Client) ClaimTaskRuntimeInput(ctx context.Context, taskID string, claim protocol.RuntimeInputClaim, ready []string) (*protocol.TaskRuntimeInput, error) {
	var result *protocol.TaskRuntimeInput
	decode := responseDecoder(func(r io.Reader) error {
		var input protocol.TaskRuntimeInput
		err := json.NewDecoder(io.LimitReader(r, 256000)).Decode(&input)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: runtime input: %w", errInvalidResponseBody, err)
		}
		if input.ID == "" || input.TaskID != taskID || input.Status != "delivering" || !slices.Contains(ready, input.Kind) || protocol.ValidateRuntimeInput(input.Kind, input.Content) != nil {
			return fmt.Errorf("%w: invalid claimed runtime input", errInvalidResponseBody)
		}
		result = &input
		return nil
	})
	// 领取请求不重放；丢失回执的行停留在 delivering，由任务收尾显示投递结果未知。
	err := c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/runtime-inputs/claim", taskID), struct {
		protocol.RuntimeInputClaim
		ReadyKinds []string `json:"ready_kinds"`
	}{claim, ready}, decode)
	return result, err
}

func (c *Client) AckTaskRuntimeInput(ctx context.Context, taskID, inputID string, ack protocol.RuntimeInputAck) error {
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/runtime-inputs/%s/ack", taskID, inputID), ack, nil, []time.Duration{100 * time.Millisecond, 300 * time.Millisecond})
}

func (d *Daemon) runTaskRuntimeInputLoop(ctx context.Context, session *agent.Session, taskID string, claim protocol.RuntimeInputClaim, capabilities []string, taskLog *slog.Logger) {
	for ctx.Err() == nil {
		var ready []string
		if slices.Contains(capabilities, "text") && session.Supplement != nil && session.SupplementReady != nil && session.SupplementReady() {
			ready = append(ready, "text")
		}
		if slices.Contains(capabilities, "command") && session.RunCommand != nil && session.CommandReady != nil && session.CommandReady() {
			ready = append(ready, "command")
		}
		if len(ready) == 0 {
			if !waitTaskSupplement(ctx, nil, d.effectiveTaskSupplementReadyInterval()) {
				return
			}
			continue
		}
		call, stop := context.WithTimeout(ctx, 3*time.Second)
		input, err := d.client.ClaimTaskRuntimeInput(call, taskID, claim, ready)
		stop()
		if err != nil || input == nil {
			if taskSupplementEndpointUnsupported(err) {
				return
			}
			if !waitTaskSupplement(ctx, nil, time.Second) {
				return
			}
			continue
		}
		ack := protocol.RuntimeInputAck{RuntimeInputClaim: claim, Status: "delivered"}
		if input.Kind == "command" {
			ack.Output, err = session.RunCommand(ctx, input.Content)
		} else {
			err = session.Supplement(ctx, formatTaskSupplementInstruction("", input.Content))
		}
		if err != nil {
			// 传输错误不能证明原生侧没有应用输入；失败不触发第二次注入。
			ack.Status, ack.Output, ack.FailureReason = "failed", "", "delivery_unknown"
			taskLog.Warn("runtime input delivery failed", "input_id", input.ID, "kind", input.Kind, "error", err)
		}
		ackCtx, cancelAck := context.WithTimeout(context.WithoutCancel(ctx), taskSupplementAckTimeout)
		err = d.client.AckTaskRuntimeInput(ackCtx, taskID, input.ID, ack)
		cancelAck()
		if err != nil {
			taskLog.Warn("runtime input receipt failed", "input_id", input.ID, "error", err)
		}
	}
}
