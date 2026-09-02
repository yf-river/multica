package execenv

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const preparationHelperTestMode = "execenv-preparation-helper"

func preparationHelperTestCommand() []string {
	return []string{
		os.Args[0],
		"-test.run=^TestPreparationHelperProcess$",
		"--",
		preparationHelperTestMode,
	}
}

// TestPreparationHelperProcess is both a no-op parent-side test and the child
// entry point used by isolation tests. Keeping it in the package test binary
// exercises the same stdin/stdout protocol as the real multica helper.
func TestPreparationHelperProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != preparationHelperTestMode {
		return
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := RunPreparationHelper(os.Stdin, os.Stdout, logger); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestPreparationHelperRoundTripsReuse(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := PrepareParams{
		WorkspacesRoot: t.TempDir(),
		WorkspaceID:    "ws-helper-reuse",
		TaskID:         "99999999-8888-7777-6666-555555555555",
		Provider:       "claude",
		Task:           TaskContextForEnv{IssueID: "issue-helper-reuse"},
	}
	env, err := PrepareIsolated(ctx, preparationHelperTestCommand(), params, logger)
	if err != nil {
		t.Fatalf("PrepareIsolated: %v", err)
	}
	reused, err := ReuseIsolated(ctx, preparationHelperTestCommand(), ReuseParams{
		WorkspacesRoot: params.WorkspacesRoot,
		WorkDir:        env.WorkDir,
		Provider:       params.Provider,
		Task: TaskContextForEnv{
			IssueID:         "issue-helper-reuse",
			NewCommentCount: 1,
			ProjectID:       "project-helper-reuse",
			ProjectResources: []ProjectResourceForEnv{
				{
					ID:           "resource-helper-reuse",
					ResourceType: "github_repo",
					ResourceRef:  json.RawMessage(`{"url":"https://github.com/multica-ai/multica"}`),
				},
			},
		},
	}, logger)
	if err != nil {
		t.Fatalf("ReuseIsolated: %v", err)
	}
	if reused == nil || reused.RootDir != env.RootDir || reused.WorkDir != env.WorkDir {
		t.Fatalf("reused environment = %#v, want root %q workdir %q", reused, env.RootDir, env.WorkDir)
	}
}

func TestPreparationHelperRoundTripsProjectResources(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := PrepareParams{
		WorkspacesRoot: t.TempDir(),
		WorkspaceID:    "ws-helper-project-resource",
		TaskID:         "88888888-7777-6666-5555-444444444444",
		Provider:       "claude",
		Task: TaskContextForEnv{
			IssueID:   "issue-helper-project-resource",
			ProjectID: "project-helper-project-resource",
			ProjectResources: []ProjectResourceForEnv{
				{
					ID:           "resource-helper-project-resource",
					ResourceType: "github_repo",
					ResourceRef:  json.RawMessage(`{"url":"https://github.com/multica-ai/multica"}`),
					Label:        "Multica",
				},
			},
		},
	}

	env, err := PrepareIsolated(ctx, preparationHelperTestCommand(), params, logger)
	if err != nil {
		t.Fatalf("PrepareIsolated: %v", err)
	}
	defer env.Cleanup(true)

	data, err := os.ReadFile(filepath.Join(env.WorkDir, ".multica", "project", "resources.json"))
	if err != nil {
		t.Fatalf("read project resources: %v", err)
	}
	var got projectResourceFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode project resources: %v", err)
	}
	if len(got.Resources) != 1 {
		t.Fatalf("project resources = %#v, want one resource", got.Resources)
	}
	resource := got.Resources[0]
	var ref struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil {
		t.Fatalf("decode resource ref: %v", err)
	}
	if resource.ID != "resource-helper-project-resource" ||
		resource.ResourceType != "github_repo" ||
		ref.URL != "https://github.com/multica-ai/multica" ||
		resource.Label != "Multica" {
		t.Fatalf("project resource = %#v, want all fields preserved", resource)
	}
}

func TestPrepareIsolatedKeepsTheClaimWithTheParent(t *testing.T) {
	t.Parallel()
	workspacesRoot := t.TempDir()
	const (
		workspaceID = "ws-isolated"
		taskID      = "01a01ec0-e69d-7000-8000-0123456789ab"
	)

	rootParams := RootDirParams{
		WorkspacesRoot:  workspacesRoot,
		WorkspaceID:     workspaceID,
		WorkspaceSlug:   "Readable Workspace",
		TaskID:          taskID,
		IssueIdentifier: "MUL-6063",
	}
	claim, err := ClaimEnvRoot(rootParams)
	if err != nil {
		t.Fatalf("parent claim: %v", err)
	}
	defer claim.Release()

	env, err := PrepareIsolated(context.Background(), preparationHelperTestCommand(), PrepareParams{
		WorkspacesRoot:    workspacesRoot,
		WorkspaceID:       workspaceID,
		WorkspaceSlug:     "Readable Workspace",
		TaskID:            taskID,
		IssueIdentifier:   "MUL-6063",
		AgentName:         "Isolated",
		EnvRootPreclaimed: true,
		Task:              TaskContextForEnv{IssueID: taskID},
	}, nil)
	if err != nil {
		t.Fatalf("PrepareIsolated: %v", err)
	}
	if env == nil || env.WorkDir == "" {
		t.Fatal("PrepareIsolated returned no environment")
	}
	if env.RootDir != claim.RootDir() {
		t.Fatalf("helper prepared %q while parent claimed %q", env.RootDir, claim.RootDir())
	}

	// The helper has exited. If the claim had been taken inside it, the lock
	// would be gone and this second claim would succeed.
	if second, err := ClaimEnvRoot(rootParams); err == nil {
		second.Release()
		t.Fatal("production PrepareIsolated returned without retaining the execution lock")
	}

	// And releasing it must hand the env root back for a later dispatch.
	claim.Release()
	next, err := ClaimEnvRoot(rootParams)
	if err != nil {
		t.Fatalf("env root stayed locked after release: %v", err)
	}
	next.Release()
}

// TestLockEnvRootForReuseExcludesConcurrentContinuations covers the lock
// primitive the reuse path is built on. The composed decision — validate the
// prior workdir, then lock only the canonical root — is pinned by
// TestLockReusablePriorEnvRoot* in the daemon package, which is where the
// ordering lives.
func TestLockEnvRootForReuseExcludesConcurrentContinuations(t *testing.T) {
	t.Parallel()
	priorRoot := filepath.Join(t.TempDir(), "ws", "0123456789ab")
	if err := os.MkdirAll(filepath.Join(priorRoot, "workdir"), 0o755); err != nil {
		t.Fatalf("seed prior root: %v", err)
	}

	wsRoot, err := os.OpenRoot(filepath.Dir(filepath.Dir(priorRoot)))
	if err != nil {
		t.Fatalf("open workspaces root: %v", err)
	}
	defer wsRoot.Close()
	rel := filepath.Join(filepath.Base(filepath.Dir(priorRoot)), filepath.Base(priorRoot))

	first, _, err := LockEnvRootForReuse(wsRoot, rel, priorRoot)
	if err != nil {
		t.Fatalf("first continuation: %v", err)
	}
	if first == nil {
		t.Fatal("expected a claim for an existing prior root")
	}

	if second, _, err := LockEnvRootForReuse(wsRoot, rel, priorRoot); err == nil {
		second.Release()
		t.Fatal("two continuations locked the same prior workdir at once")
	}

	first.Release()
	again, _, err := LockEnvRootForReuse(wsRoot, rel, priorRoot)
	if err != nil {
		t.Fatalf("prior root stayed locked after release: %v", err)
	}
	again.Release()

	// A missing root is not an error — the caller falls through to a fresh
	// Prepare, and there is nothing to exclude on.
	base := t.TempDir()
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		t.Fatalf("open base: %v", err)
	}
	defer baseRoot.Close()
	missing, _, err := LockEnvRootForReuse(baseRoot, "absent", filepath.Join(base, "absent"))
	if err != nil || missing != nil {
		t.Fatalf("missing prior root: claim=%v err=%v, want nil/nil", missing, err)
	}
}

// TestPrepareIsolatedFailsLoudlyWhenPreclaimIsNotDeclared pins what happens if
// a caller ever holds the claim but forgets EnvRootPreclaimed. Parent and
// helper then contend for the same lock, and the important property is that
// this fails immediately and says so, rather than proceeding with preparation
// that silently believes it is protected.
func TestPrepareIsolatedFailsLoudlyWhenPreclaimIsNotDeclared(t *testing.T) {
	t.Parallel()
	workspacesRoot := t.TempDir()
	const (
		workspaceID = "ws-preclaim"
		taskID      = "01a01ec0-e69d-7000-8000-0123456789ab"
	)

	claim, err := ClaimEnvRoot(RootDirParams{WorkspacesRoot: workspacesRoot, WorkspaceID: workspaceID, TaskID: taskID})
	if err != nil {
		t.Fatalf("parent claim: %v", err)
	}
	defer claim.Release()

	_, err = PrepareIsolated(context.Background(), preparationHelperTestCommand(), PrepareParams{
		WorkspacesRoot: workspacesRoot,
		WorkspaceID:    workspaceID,
		TaskID:         taskID,
		AgentName:      "Forgetful",
		// EnvRootPreclaimed deliberately left false while the parent holds it.
		Task: TaskContextForEnv{IssueID: taskID},
	}, nil)
	if err == nil {
		t.Fatal("preparation ran without declaring the parent's claim")
	}
	if !strings.Contains(err.Error(), "running execution") {
		t.Fatalf("error = %v, want it to name the held claim", err)
	}
}
