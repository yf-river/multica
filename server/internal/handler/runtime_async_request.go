package handler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type runtimeAsyncRequestStatus string

const (
	runtimeAsyncPending   runtimeAsyncRequestStatus = "pending"
	runtimeAsyncRunning   runtimeAsyncRequestStatus = "running"
	runtimeAsyncCompleted runtimeAsyncRequestStatus = "completed"
	runtimeAsyncFailed    runtimeAsyncRequestStatus = "failed"
	runtimeAsyncTimeout   runtimeAsyncRequestStatus = "timeout"
	runtimeAsyncConflict  runtimeAsyncRequestStatus = "conflict"

	runtimeAsyncRunningTimeout  = 60 * time.Second
	runtimeAsyncRedisPopRetries = 5
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

// runtimeListRequestStore is the one lifecycle contract shared by runtime
// inventory requests. Model and local-skill discovery keep distinct payloads,
// permissions and persistence namespaces, but they do not own separate state
// machines.
type runtimeListRequestStore[T any, Item any] interface {
	Create(ctx context.Context, runtimeID, requestID string) (*T, error)
	Get(ctx context.Context, id string) (*T, error)
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	PopPending(ctx context.Context, runtimeID string) (*T, error)
	Complete(ctx context.Context, id string, items []Item, supported bool) error
	Fail(ctx context.Context, id string, message string) error
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

type inMemoryRuntimeListStore[T any, Item any] struct {
	*inMemoryRuntimeAsyncStore[T]
	newRequest func(runtimeID, requestID string, now time.Time) *T
	setResult  func(*T, []Item, bool)
}

func newInMemoryRuntimeListStore[T any, Item any](
	retention time.Duration,
	state func(*T) *runtimeAsyncRequestState,
	applyTimeout func(*runtimeAsyncRequestState, time.Time) bool,
	newRequest func(runtimeID, requestID string, now time.Time) *T,
	setResult func(*T, []Item, bool),
) *inMemoryRuntimeListStore[T, Item] {
	return &inMemoryRuntimeListStore[T, Item]{
		inMemoryRuntimeAsyncStore: newInMemoryRuntimeAsyncStore(retention, state, applyTimeout),
		newRequest:                newRequest,
		setResult:                 setResult,
	}
}

func (s *inMemoryRuntimeListStore[T, Item]) Create(_ context.Context, runtimeID, requestID string) (*T, error) {
	return s.create(requestID, func(now time.Time) *T {
		return s.newRequest(runtimeID, requestID, now)
	}, func(existing *T) error {
		if s.state(existing).RuntimeID != runtimeID {
			return errRuntimeAsyncRequestConflict
		}
		return nil
	})
}

func (s *inMemoryRuntimeListStore[T, Item]) Get(_ context.Context, id string) (*T, error) {
	return s.get(id), nil
}

func (s *inMemoryRuntimeListStore[T, Item]) HasPending(_ context.Context, runtimeID string) (bool, error) {
	return s.hasPending(runtimeID), nil
}

func (s *inMemoryRuntimeListStore[T, Item]) PopPending(_ context.Context, runtimeID string) (*T, error) {
	pending := s.popPending(runtimeID, 1)
	if len(pending) == 0 {
		return nil, nil
	}
	return pending[0], nil
}

func (s *inMemoryRuntimeListStore[T, Item]) Complete(_ context.Context, id string, items []Item, supported bool) error {
	s.update(id, func(request *T, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncCompleted
		s.setResult(request, items, supported)
		state.UpdatedAt = now
	})
	return nil
}

func (s *inMemoryRuntimeListStore[T, Item]) Fail(_ context.Context, id, message string) error {
	s.fail(id, message)
	return nil
}

// redisRuntimeAsyncStore owns the Redis lifecycle shared by runtime requests.
// Domain stores provide only their key namespaces, wire codec, timeout policy,
// and idempotency rule.
type redisRuntimeAsyncStore[T any] struct {
	rdb           *redis.Client
	recordPrefix  string
	pendingPrefix string
	retention     time.Duration
	state         func(*T) *runtimeAsyncRequestState
	encode        func(*T) ([]byte, error)
	decode        func([]byte) (*T, error)
	applyTimeout  func(*runtimeAsyncRequestState, time.Time) bool
	errorLabel    string
}

func newRedisRuntimeAsyncStore[T any](
	rdb *redis.Client,
	recordPrefix string,
	pendingPrefix string,
	retention time.Duration,
	state func(*T) *runtimeAsyncRequestState,
	encode func(*T) ([]byte, error),
	decode func([]byte) (*T, error),
	applyTimeout func(*runtimeAsyncRequestState, time.Time) bool,
	errorLabel string,
) *redisRuntimeAsyncStore[T] {
	return &redisRuntimeAsyncStore[T]{
		rdb:           rdb,
		recordPrefix:  recordPrefix,
		pendingPrefix: pendingPrefix,
		retention:     retention,
		state:         state,
		encode:        encode,
		decode:        decode,
		applyTimeout:  applyTimeout,
		errorLabel:    errorLabel,
	}
}

func (s *redisRuntimeAsyncStore[T]) recordKey(id string) string {
	return s.recordPrefix + id
}

func (s *redisRuntimeAsyncStore[T]) pendingKey(runtimeID string) string {
	return s.pendingPrefix + runtimeID
}

var createRuntimeAsyncPendingScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
    return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', tonumber(ARGV[3]))
redis.call('ZADD', KEYS[2], ARGV[2], ARGV[4])
redis.call('EXPIRE', KEYS[2], tonumber(ARGV[5]))
return 1
`)

var claimRuntimeAsyncPendingScript = redis.NewScript(`
local removed = redis.call('ZREM', KEYS[1], ARGV[1])
if removed == 0 then
    return 0
end
redis.call('SET', KEYS[2], ARGV[2], 'EX', tonumber(ARGV[3]))
return 1
`)

func (s *redisRuntimeAsyncStore[T]) create(
	ctx context.Context,
	request *T,
	validateExisting func(*T) error,
	disappearedMessage string,
) (*T, error) {
	state := s.state(request)
	data, err := s.encode(request)
	if err != nil {
		return nil, err
	}

	created, err := createRuntimeAsyncPendingScript.Run(ctx, s.rdb,
		[]string{s.recordKey(state.ID), s.pendingKey(state.RuntimeID)},
		data, state.CreatedAt.UnixNano(), int(s.retention/time.Second), state.ID,
		int((s.retention*2)/time.Second),
	).Int()
	if err != nil {
		return nil, fmt.Errorf("persist %s: %w", s.errorLabel, err)
	}
	if created == 1 {
		return request, nil
	}

	existing, err := s.load(ctx, state.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, errors.New(disappearedMessage)
	}
	if err := validateExisting(existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *redisRuntimeAsyncStore[T]) load(ctx context.Context, id string) (*T, error) {
	raw, err := s.rdb.Get(ctx, s.recordKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", s.errorLabel, err)
	}

	request, err := s.decode(raw)
	if err != nil {
		return nil, err
	}
	state := s.state(request)
	if s.applyTimeout(state, time.Now()) {
		if err := s.persist(ctx, request); err != nil {
			return nil, err
		}
		s.rdb.ZRem(ctx, s.pendingKey(state.RuntimeID), state.ID)
	}
	return request, nil
}

func (s *redisRuntimeAsyncStore[T]) persist(ctx context.Context, request *T) error {
	data, err := s.encode(request)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, s.recordKey(s.state(request).ID), data, s.retention).Err(); err != nil {
		return fmt.Errorf("persist %s: %w", s.errorLabel, err)
	}
	return nil
}

func (s *redisRuntimeAsyncStore[T]) hasPending(ctx context.Context, runtimeID string) (bool, error) {
	count, err := s.rdb.ZCard(ctx, s.pendingKey(runtimeID)).Result()
	if err != nil {
		return false, fmt.Errorf("zcard pending: %w", err)
	}
	return count > 0, nil
}

func (s *redisRuntimeAsyncStore[T]) claim(
	ctx context.Context,
	pendingKey string,
	id string,
	errorLabel string,
) (*T, bool, error) {
	request, err := s.load(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if request == nil {
		s.rdb.ZRem(ctx, pendingKey, id)
		return nil, false, nil
	}

	state := s.state(request)
	if state.Status != runtimeAsyncPending {
		s.rdb.ZRem(ctx, pendingKey, id)
		return nil, false, nil
	}

	now := time.Now()
	state.Status = runtimeAsyncRunning
	state.RunStartedAt = &now
	state.UpdatedAt = now
	data, err := s.encode(request)
	if err != nil {
		return nil, false, err
	}

	claimed, err := claimRuntimeAsyncPendingScript.Run(
		ctx,
		s.rdb,
		[]string{pendingKey, s.recordKey(id)},
		id,
		data,
		int(s.retention/time.Second),
	).Int64()
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", errorLabel, err)
	}
	return request, claimed == 1, nil
}

func (s *redisRuntimeAsyncStore[T]) popPending(ctx context.Context, runtimeID string) (*T, error) {
	pendingKey := s.pendingKey(runtimeID)
	for range runtimeAsyncRedisPopRetries {
		ids, err := s.rdb.ZRange(ctx, pendingKey, 0, 0).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange pending: %w", err)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		request, claimed, err := s.claim(ctx, pendingKey, ids[0], "claim pending")
		if err != nil {
			return nil, err
		}
		if claimed {
			return request, nil
		}
	}
	return nil, nil
}

func (s *redisRuntimeAsyncStore[T]) popPendingBatch(ctx context.Context, runtimeID string, limit int) ([]*T, error) {
	pendingKey := s.pendingKey(runtimeID)
	ids, err := s.rdb.ZRange(ctx, pendingKey, 0, int64(limit)-1).Result()
	if err != nil {
		return nil, fmt.Errorf("zrange pending batch: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	var requests []*T
	for _, id := range ids {
		request, claimed, err := s.claim(ctx, pendingKey, id, "claim pending batch")
		if err != nil {
			return requests, err
		}
		if claimed {
			requests = append(requests, request)
		}
	}
	return requests, nil
}

func (s *redisRuntimeAsyncStore[T]) update(
	ctx context.Context,
	id string,
	apply func(*T, *runtimeAsyncRequestState, time.Time),
) error {
	request, err := s.load(ctx, id)
	if err != nil || request == nil {
		return err
	}
	apply(request, s.state(request), time.Now())
	return s.persist(ctx, request)
}

func (s *redisRuntimeAsyncStore[T]) fail(ctx context.Context, id, message string) error {
	return s.update(ctx, id, func(_ *T, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncFailed
		state.Error = message
		state.UpdatedAt = now
	})
}

type redisRuntimeListStore[T any, Item any] struct {
	*redisRuntimeAsyncStore[T]
	newRequest         func(runtimeID, requestID string, now time.Time) *T
	setResult          func(*T, []Item, bool)
	disappearedMessage string
}

func newRedisRuntimeListStore[T any, Item any](
	store *redisRuntimeAsyncStore[T],
	newRequest func(runtimeID, requestID string, now time.Time) *T,
	setResult func(*T, []Item, bool),
	disappearedMessage string,
) *redisRuntimeListStore[T, Item] {
	return &redisRuntimeListStore[T, Item]{
		redisRuntimeAsyncStore: store,
		newRequest:             newRequest,
		setResult:              setResult,
		disappearedMessage:     disappearedMessage,
	}
}

func (s *redisRuntimeListStore[T, Item]) Create(ctx context.Context, runtimeID, requestID string) (*T, error) {
	request := s.newRequest(runtimeID, requestID, time.Now())
	return s.create(ctx, request, func(existing *T) error {
		if s.state(existing).RuntimeID != runtimeID {
			return errRuntimeAsyncRequestConflict
		}
		return nil
	}, s.disappearedMessage)
}

func (s *redisRuntimeListStore[T, Item]) Get(ctx context.Context, id string) (*T, error) {
	return s.load(ctx, id)
}

func (s *redisRuntimeListStore[T, Item]) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	return s.hasPending(ctx, runtimeID)
}

func (s *redisRuntimeListStore[T, Item]) PopPending(ctx context.Context, runtimeID string) (*T, error) {
	return s.popPending(ctx, runtimeID)
}

func (s *redisRuntimeListStore[T, Item]) Complete(ctx context.Context, id string, items []Item, supported bool) error {
	return s.update(ctx, id, func(request *T, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncCompleted
		s.setResult(request, items, supported)
		state.UpdatedAt = now
	})
}

func (s *redisRuntimeListStore[T, Item]) Fail(ctx context.Context, id, message string) error {
	return s.fail(ctx, id, message)
}
