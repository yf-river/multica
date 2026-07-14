package handler

import "time"

type runtimeAsyncRequestStatus string

const (
	runtimeAsyncPending   runtimeAsyncRequestStatus = "pending"
	runtimeAsyncRunning   runtimeAsyncRequestStatus = "running"
	runtimeAsyncCompleted runtimeAsyncRequestStatus = "completed"
	runtimeAsyncFailed    runtimeAsyncRequestStatus = "failed"
	runtimeAsyncTimeout   runtimeAsyncRequestStatus = "timeout"
	runtimeAsyncConflict  runtimeAsyncRequestStatus = "conflict"

	runtimeAsyncRunningTimeout = 60 * time.Second
)

type runtimeAsyncRequestState struct {
	ID           string                    `json:"id"`
	RuntimeID    string                    `json:"runtime_id"`
	Status       runtimeAsyncRequestStatus `json:"status"`
	Error        string                    `json:"error,omitempty"`
	CreatedAt    time.Time                 `json:"created_at"`
	UpdatedAt    time.Time                 `json:"updated_at"`
	RunStartedAt *time.Time                `json:"-"`
}

func applyRuntimeAsyncTimeout(req *runtimeAsyncRequestState, now time.Time, pendingTimeout time.Duration, pendingError string) bool {
	switch req.Status {
	case runtimeAsyncPending:
		if now.Sub(req.CreatedAt) > pendingTimeout {
			req.Status = runtimeAsyncTimeout
			req.Error = pendingError
			req.UpdatedAt = now
			return true
		}
	case runtimeAsyncRunning:
		if req.RunStartedAt != nil && now.Sub(*req.RunStartedAt) > runtimeAsyncRunningTimeout {
			req.Status = runtimeAsyncTimeout
			req.Error = "daemon did not finish within 60 seconds"
			req.UpdatedAt = now
			return true
		}
	}
	return false
}

func runtimeAsyncRequestTerminal(status runtimeAsyncRequestStatus) bool {
	return status == runtimeAsyncCompleted || status == runtimeAsyncFailed ||
		status == runtimeAsyncTimeout || status == runtimeAsyncConflict
}
