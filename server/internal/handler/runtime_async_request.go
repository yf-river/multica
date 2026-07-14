package handler

import (
	"errors"
	"sort"
	"sync"
	"time"
)

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

var errRuntimeAsyncRequestConflict = errors.New("runtime async request conflict")

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

type inMemoryRuntimeAsyncStore[T any] struct {
	mu           sync.Mutex
	requests     map[string]*T
	retention    time.Duration
	state        func(*T) *runtimeAsyncRequestState
	applyTimeout func(*runtimeAsyncRequestState, time.Time) bool
}

func newInMemoryRuntimeAsyncStore[T any](
	retention time.Duration,
	state func(*T) *runtimeAsyncRequestState,
	applyTimeout func(*runtimeAsyncRequestState, time.Time) bool,
) *inMemoryRuntimeAsyncStore[T] {
	return &inMemoryRuntimeAsyncStore[T]{
		requests:     make(map[string]*T),
		retention:    retention,
		state:        state,
		applyTimeout: applyTimeout,
	}
}

func (s *inMemoryRuntimeAsyncStore[T]) create(
	id string,
	build func(time.Time) *T,
	validateExisting func(*T) error,
) (*T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for requestID, request := range s.requests {
		if now.Sub(s.state(request).CreatedAt) > s.retention {
			delete(s.requests, requestID)
		}
	}

	if existing := s.requests[id]; existing != nil {
		if err := validateExisting(existing); err != nil {
			return nil, err
		}
		return existing, nil
	}

	request := build(now)
	s.requests[id] = request
	return request, nil
}

func (s *inMemoryRuntimeAsyncStore[T]) get(id string) *T {
	s.mu.Lock()
	defer s.mu.Unlock()

	request := s.requests[id]
	if request != nil {
		s.applyTimeout(s.state(request), time.Now())
	}
	return request
}

func (s *inMemoryRuntimeAsyncStore[T]) hasPending(runtimeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, request := range s.requests {
		state := s.state(request)
		s.applyTimeout(state, now)
		if state.RuntimeID == runtimeID && state.Status == runtimeAsyncPending {
			return true
		}
	}
	return false
}

func (s *inMemoryRuntimeAsyncStore[T]) popPending(runtimeID string, limit int) []*T {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	pending := make([]*T, 0)
	for _, request := range s.requests {
		state := s.state(request)
		s.applyTimeout(state, now)
		if state.RuntimeID == runtimeID && state.Status == runtimeAsyncPending {
			pending = append(pending, request)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		return s.state(pending[i]).CreatedAt.Before(s.state(pending[j]).CreatedAt)
	})
	if limit > len(pending) {
		limit = len(pending)
	}
	for _, request := range pending[:limit] {
		state := s.state(request)
		state.Status = runtimeAsyncRunning
		startedAt := now
		state.RunStartedAt = &startedAt
		state.UpdatedAt = now
	}
	return pending[:limit]
}

func (s *inMemoryRuntimeAsyncStore[T]) update(id string, apply func(*T, *runtimeAsyncRequestState, time.Time)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if request := s.requests[id]; request != nil {
		apply(request, s.state(request), time.Now())
	}
}

func (s *inMemoryRuntimeAsyncStore[T]) fail(id, message string) {
	s.update(id, func(_ *T, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncFailed
		state.Error = message
		state.UpdatedAt = now
	})
}
