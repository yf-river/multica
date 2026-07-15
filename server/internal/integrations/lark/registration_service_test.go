package lark

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRegistrationServiceBeginInstallRejectsMissingRegionBeforeDatabaseAccess(t *testing.T) {
	validID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	service := &RegistrationService{}

	_, err := service.BeginInstall(context.Background(), BeginInstallParams{
		WorkspaceID: validID,
		AgentID:     validID,
		InitiatorID: validID,
	})
	if err == nil || !strings.Contains(err.Error(), "region must be feishu or lark") {
		t.Fatalf("BeginInstall missing region error = %v", err)
	}
}

func TestRegistrationServiceBeginInstallReplaysPendingSession(t *testing.T) {
	fake := newRegistrationFake(t)
	fake.stubBegin(map[string]any{
		"device_code":               "dedup-device-code",
		"verification_uri_complete": "https://accounts.feishu.cn/oauth/v1/qrcode?code=dedup",
		"interval":                  60,
		"expire_in":                 1,
	})
	service := &RegistrationService{
		cfg:    RegistrationServiceConfig{}.withDefaults(),
		client: NewRegistrationClient(RegistrationConfig{Domain: fake.URL()}),
		getAgentInWorkspace: func(context.Context, db.GetAgentInWorkspaceParams) (db.Agent, error) {
			return db.Agent{Name: "Replay Bot"}, nil
		},
		sessions: make(map[string]*registrationSession),
	}
	id := uuidFromStringSvc(t, "11111111-1111-1111-1111-111111111111")
	params := BeginInstallParams{
		WorkspaceID: id,
		AgentID:     id,
		InitiatorID: id,
		Region:      RegionFeishu,
	}

	first, err := service.BeginInstall(context.Background(), params)
	if err != nil {
		t.Fatalf("first begin: %v", err)
	}
	second, err := service.BeginInstall(context.Background(), params)
	if err != nil {
		t.Fatalf("replayed begin: %v", err)
	}
	if second != first {
		t.Fatalf("replayed begin = %+v, want exact pending session %+v", second, first)
	}
	if got := fake.beginN.Load(); got != 1 {
		t.Fatalf("external begin calls = %d, want 1", got)
	}

	const callers = 8
	results := make(chan BeginInstallResult, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.BeginInstall(context.Background(), params)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent begin: %v", err)
		}
	}
	for result := range results {
		if result != first {
			t.Fatalf("concurrent begin = %+v, want %+v", result, first)
		}
	}
	if got := fake.beginN.Load(); got != 1 {
		t.Fatalf("external begin calls after concurrency = %d, want 1", got)
	}
	otherUserParams := params
	otherUserParams.InitiatorID = uuidFromStringSvc(t, "22222222-2222-2222-2222-222222222222")
	otherUserResult, err := service.BeginInstall(context.Background(), otherUserParams)
	if err != nil {
		t.Fatalf("other-user begin: %v", err)
	}
	if otherUserResult.SessionID == first.SessionID {
		t.Fatalf("pending session leaked across initiating users: %+v", otherUserResult)
	}
	if got := fake.beginN.Load(); got != 2 {
		t.Fatalf("external begin calls after other user = %d, want 2", got)
	}

	service.mu.Lock()
	firstSession := service.sessions[first.SessionID]
	service.mu.Unlock()
	if firstSession == nil {
		t.Fatalf("pending session %q not stored", first.SessionID)
	}
	firstSession.markError(RegistrationReasonAccessDenied, "user denied", service.gcDeadline())
	restarted, err := service.BeginInstall(context.Background(), params)
	if err != nil {
		t.Fatalf("restart after terminal session: %v", err)
	}
	if restarted.SessionID == first.SessionID {
		t.Fatalf("terminal session was replayed instead of restarted: %+v", restarted)
	}
	if got := fake.beginN.Load(); got != 3 {
		t.Fatalf("external begin calls after explicit restart = %d, want 3", got)
	}
}

// These tests cover the pure-Go halves of RegistrationService —
// constructor validation, session-id security boundary, status code
// mapping — without touching the database. The polling goroutine's
// DB-write paths (UpsertLarkInstallation + BindInstallerTx in one tx)
// require a real Postgres + sqlc-generated *db.Queries and are
// covered by an integration test against the migration suite.

// TestRegistrationServiceConstructorValidatesDeps pins that every
// required dependency surfaces as a constructor error rather than a
// runtime panic inside BeginInstall — a half-init at startup would
// otherwise leave the install button returning 500s with no signal in
// the logs.
func TestRegistrationServiceConstructorValidatesDeps(t *testing.T) {
	client := NewRegistrationClient(RegistrationConfig{})
	api := NewHTTPAPIClient(HTTPClientConfig{})
	binder := func(context.Context, *db.Queries, InstallerBindParams) error { return nil }
	cases := []struct {
		name   string
		fn     func() error
		needle string
	}{
		{"missing client", func() error {
			_, err := NewRegistrationService(RegistrationServiceConfig{}, nil, api, &db.Queries{}, fakeTxStarter{}, &InstallationService{}, binder, events.New())
			return err
		}, "RegistrationClient"},
		{"missing api", func() error {
			_, err := NewRegistrationService(RegistrationServiceConfig{}, client, nil, &db.Queries{}, fakeTxStarter{}, &InstallationService{}, binder, events.New())
			return err
		}, "APIClient"},
		{"missing queries", func() error {
			_, err := NewRegistrationService(RegistrationServiceConfig{}, client, api, nil, fakeTxStarter{}, &InstallationService{}, binder, events.New())
			return err
		}, "queries"},
		{"missing tx", func() error {
			_, err := NewRegistrationService(RegistrationServiceConfig{}, client, api, &db.Queries{}, nil, &InstallationService{}, binder, events.New())
			return err
		}, "TxStarter"},
		{"missing installs", func() error {
			_, err := NewRegistrationService(RegistrationServiceConfig{}, client, api, &db.Queries{}, fakeTxStarter{}, nil, binder, events.New())
			return err
		}, "InstallationService"},
		{"missing binder", func() error {
			_, err := NewRegistrationService(RegistrationServiceConfig{}, client, api, &db.Queries{}, fakeTxStarter{}, &InstallationService{}, nil, events.New())
			return err
		}, "bind installer"},
		{"missing event bus", func() error {
			_, err := NewRegistrationService(RegistrationServiceConfig{}, client, api, &db.Queries{}, fakeTxStarter{}, &InstallationService{}, binder, nil)
			return err
		}, "event bus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil || !strings.Contains(err.Error(), tc.needle) {
				t.Errorf("want error mentioning %q, got %v", tc.needle, err)
			}
		})
	}
}

// TestBotNamePreset pins the bot-name pre-fill format that rides on the
// QR URL: "<agent> - Multica", with a blank agent name degrading to
// plain "Multica" rather than a dangling " - Multica".
func TestBotNamePreset(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Ada", "Ada - Multica"},
		{"  Ada  ", "Ada - Multica"},
		{"产品助手", "产品助手 - Multica"},
		{"", "Multica"},
		{"   ", "Multica"},
	}
	for _, tc := range cases {
		if got := botNamePreset(tc.in); got != tc.want {
			t.Errorf("botNamePreset(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRegistrationGetSessionNotFound pins both halves of the
// not-found path: unknown session id, and (the security-critical one)
// known session id but from a different workspace. Both must surface
// the same ErrRegistrationSessionNotFound — leaking "exists but wrong
// workspace" would let an attacker enumerate session ids across
// workspaces.
func TestRegistrationGetSessionNotFound(t *testing.T) {
	s := newRegistrationServiceForTest(t)
	ws := uuidFromStringSvc(t, "11111111-1111-1111-1111-111111111111")
	otherWs := uuidFromStringSvc(t, "22222222-2222-2222-2222-222222222222")

	if _, err := s.GetSession(ws, "nope"); !errors.Is(err, ErrRegistrationSessionNotFound) {
		t.Errorf("unknown session: want ErrRegistrationSessionNotFound, got %v", err)
	}

	// Plant a session by hand for the cross-workspace test (BeginInstall
	// requires a live DB; we are only exercising the lookup boundary).
	s.mu.Lock()
	s.sessions["plant-1"] = &registrationSession{
		id:          "plant-1",
		workspaceID: ws,
		status:      RegistrationStatusPending,
	}
	s.mu.Unlock()

	if _, err := s.GetSession(otherWs, "plant-1"); !errors.Is(err, ErrRegistrationSessionNotFound) {
		t.Errorf("cross-workspace lookup: want ErrRegistrationSessionNotFound, got %v", err)
	}

	state, err := s.GetSession(ws, "plant-1")
	if err != nil {
		t.Fatalf("same-workspace lookup: %v", err)
	}
	if state.Status != RegistrationStatusPending {
		t.Errorf("Status: got %q want pending", state.Status)
	}
}

// TestRegistrationGetSessionGCsExpiredEntries pins that a session
// whose gcAfter is in the past is dropped on the next lookup, so the
// in-memory map cannot grow unbounded across restarts of long-lived
// servers.
func TestRegistrationGetSessionGCsExpiredEntries(t *testing.T) {
	clock := &fakeClockSvc{now: time.Unix(1_700_000_000, 0)}
	s := newRegistrationServiceForTest(t)
	s.cfg.Now = clock.Now
	ws := uuidFromStringSvc(t, "11111111-1111-1111-1111-111111111111")

	s.mu.Lock()
	s.sessions["expired"] = &registrationSession{
		id:          "expired",
		workspaceID: ws,
		status:      RegistrationStatusError,
		gcAfter:     clock.Now().Add(-1 * time.Minute),
	}
	s.sessions["live"] = &registrationSession{
		id:          "live",
		workspaceID: ws,
		status:      RegistrationStatusSuccess,
		gcAfter:     clock.Now().Add(10 * time.Minute),
	}
	s.mu.Unlock()

	// Lookup of any id triggers gcExpiredLocked — the expired one
	// disappears, the live one stays.
	if _, err := s.GetSession(ws, "live"); err != nil {
		t.Errorf("live session lookup: %v", err)
	}
	if _, err := s.GetSession(ws, "expired"); !errors.Is(err, ErrRegistrationSessionNotFound) {
		t.Errorf("expired session lookup: want not-found, got %v", err)
	}
	s.mu.Lock()
	_, expiredExists := s.sessions["expired"]
	s.mu.Unlock()
	if expiredExists {
		t.Errorf("GC should have dropped the expired session from the map")
	}
}

func TestRegistrationSessionMarkErrorIsIdempotent(t *testing.T) {
	sess := &registrationSession{
		id:     "x",
		status: RegistrationStatusPending,
	}
	deadline := time.Unix(1_700_001_000, 0)
	sess.markError(RegistrationReasonAccessDenied, "user denied", deadline)
	sess.markError(RegistrationReasonExpired, "qr expired", deadline)
	st := sess.snapshot()
	if st.ErrorReason != RegistrationReasonAccessDenied {
		t.Errorf("first reason should win; got %q", st.ErrorReason)
	}
}

func TestRegistrationServicePublishInstalledEmitsCreatedEvent(t *testing.T) {
	bus := events.New()
	var caught []events.Event
	bus.Subscribe(protocol.EventLarkInstallationCreated, func(e events.Event) {
		caught = append(caught, e)
	})

	svc := newRegistrationServiceForTest(t)
	svc.bus = bus

	ws := uuidFromStringSvc(t, "11111111-1111-1111-1111-111111111111")
	inst := uuidFromStringSvc(t, "22222222-2222-2222-2222-222222222222")
	svc.publishInstalled(ws, inst)

	// Exactly one — guards against a future re-introduction of the
	// now-removed second emit in the status-poll handler.
	if len(caught) != 1 {
		t.Fatalf("expected exactly 1 lark_installation:created event, got %d", len(caught))
	}
	got := caught[0]
	if got.Type != protocol.EventLarkInstallationCreated {
		t.Errorf("type = %q, want %q", got.Type, protocol.EventLarkInstallationCreated)
	}
	if got.WorkspaceID != util.UUIDToString(ws) {
		t.Errorf("workspace_id = %q, want %q", got.WorkspaceID, util.UUIDToString(ws))
	}
	if got.ActorType != "system" {
		t.Errorf("actor_type = %q, want \"system\"", got.ActorType)
	}
	payload, ok := got.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", got.Payload)
	}
	if payload["installation_id"] != util.UUIDToString(inst) {
		t.Errorf("installation_id = %v, want %q", payload["installation_id"], util.UUIDToString(inst))
	}
}

// fakeTxStarter is a TxStarter stub for constructor tests — never
// actually called.
type fakeTxStarter struct{}

func (fakeTxStarter) Begin(_ context.Context) (pgx.Tx, error) {
	return nil, errors.New("fakeTxStarter Begin not implemented")
}

// newRegistrationServiceForTest constructs a service with all
// dependencies mocked / nil — the GetSession boundary does not exercise
// the polling goroutine, so the unused deps stay zero.
func newRegistrationServiceForTest(t *testing.T) *RegistrationService {
	t.Helper()
	return &RegistrationService{
		cfg:      RegistrationServiceConfig{}.withDefaults(),
		bus:      events.New(),
		sessions: make(map[string]*registrationSession),
	}
}

func uuidFromStringSvc(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("scan uuid: %v", err)
	}
	return u
}

type fakeClockSvc struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClockSvc) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
