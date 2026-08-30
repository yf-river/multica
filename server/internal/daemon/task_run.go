package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

const governedLifeTaskTimeout = 10 * time.Minute

func governedLifeMCPConfig() (json.RawMessage, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve multica executable for life MCP: %w", err)
	}
	config, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"life": map[string]any{"command": executable, "args": []string{"life", "mcp"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build life MCP config: %w", err)
	}
	return config, nil
}

func taskExecutionTimeout(configured time.Duration, governedLifeTask bool) time.Duration {
	if governedLifeTask && (configured <= 0 || configured > governedLifeTaskTimeout) {
		return governedLifeTaskTimeout
	}
	return configured
}

func (d *Daemon) runTask(ctx context.Context, task Task, provider string, slot int, taskLog *slog.Logger) (TaskResult, error) {
	// Refuse to spawn an agent without a workspace. An empty workspace_id
	// here would make MULTICA_WORKSPACE_ID empty in the agent env, and the
	// CLI would otherwise silently fall back to the user-global config — a
	// path that can leak operations into an unrelated workspace when
	// multiple workspaces share a host.
	if task.WorkspaceID == "" {
		return TaskResult{}, fmt.Errorf("refusing to spawn agent: task has no workspace_id (task_id=%s)", task.ID)
	}
	governedLifeTask := isGovernedLifeTask(task)
	governedLifeCognition := task.LifeJobID != ""
	if governedLifeTask {
		// Life history is reconstructed from Multica's governed context on every
		// run. Provider-native sessions would retain corrected or permanently
		// deleted material outside that governance boundary.
		task.PriorSessionID = ""
	}

	// task.Repos is the authoritative repo list for this task — when the
	// claimed task belongs to a project with github_repo resources the server
	// has already narrowed it to project repos only. Make sure those URLs are
	// in the per-workspace allowlist and the local cache, otherwise
	// `multica repo checkout` would reject project-only URLs that aren't also
	// bound at the workspace level.
	d.registerTaskRepos(task.WorkspaceID, task.Repos)

	entry, ok := d.cfg.Agents[provider]
	var profileFixedArgs []string
	// A custom runtime profile (MUL-3284) overrides the executable path: the
	// runtime's protocol_family is the provider (so agent.New still selects
	// the right backend), but the actual binary on PATH is the profile's
	// command_name, resolved at registration time and keyed by RuntimeID here.
	// Critically, a custom runtime can live on a host that has NO built-in
	// agent of the same provider installed, so when the runtime is custom we
	// synthesize an AgentEntry instead of hard-failing on the !ok lookup.
	if launch, isCustom := d.customLaunchForRuntime(task.RuntimeID); isCustom {
		entry.Path = launch.CommandPath
		profileFixedArgs = launch.FixedArgs
		ok = true
		d.logger.Info("task uses custom runtime profile command",
			"task_id", task.ID, "runtime_id", task.RuntimeID,
			"provider", provider, "command_path", launch.CommandPath,
			"fixed_args_count", len(profileFixedArgs))
	}
	if !ok {
		return TaskResult{}, fmt.Errorf("no agent configured for provider %q", provider)
	}

	agentName := "agent"
	var agentID string
	var skills []protocol.TaskSkill
	var instructions string
	if task.Agent != nil {
		agentID = task.Agent.ID
		agentName = task.Agent.Name
		skills = task.Agent.Skills
		instructions = task.Agent.Instructions
	}
	executionPolicy := effectiveTaskExecutionPolicy(task)

	// Prepare isolated execution environment.
	// Repos are passed as metadata only — the agent checks them out on demand
	// via `multica repo checkout <url>`.
	taskCtx := execenv.TaskContextForEnv{
		IssueID:                          task.IssueID,
		TriggerCommentID:                 task.TriggerCommentID,
		TriggerThreadID:                  task.TriggerThreadID,
		NewCommentCount:                  task.NewCommentCount,
		NewCommentsSince:                 task.NewCommentsSince,
		PriorSessionResumed:              task.PriorSessionID != "",
		AgentID:                          agentID,
		AgentName:                        agentName,
		AgentInstructions:                instructions,
		AgentSkills:                      convertSkillsForEnv(skills),
		Repos:                            task.Repos,
		ProjectID:                        task.ProjectID,
		ProjectTitle:                     task.ProjectTitle,
		ProjectResources:                 convertProjectResourcesForEnv(task.ProjectResources),
		ExecutionPolicy:                  executionPolicy,
		ChatSessionID:                    task.ChatSessionID,
		AutopilotRunID:                   task.AutopilotRunID,
		AutopilotID:                      task.AutopilotID,
		AutopilotTitle:                   task.AutopilotTitle,
		AutopilotDescription:             task.AutopilotDescription,
		AutopilotSource:                  task.AutopilotSource,
		AutopilotTriggerPayload:          strings.TrimSpace(string(task.AutopilotTriggerPayload)),
		QuickCreatePrompt:                task.QuickCreatePrompt,
		SourceSummaryPrompt:              task.SourceSummaryPrompt,
		IsSquadLeader:                    hasSquadLeaderBriefing(instructions),
		RequestingUserName:               task.RequestingUserName,
		RequestingUserProfileDescription: task.RequestingUserProfileDescription,
		InitiatorType:                    task.InitiatorType,
		InitiatorID:                      task.InitiatorID,
		InitiatorName:                    task.InitiatorName,
		InitiatorAccount:                 task.InitiatorAccount,
		WorkspaceContext:                 task.WorkspaceContext,
	}

	// Mark candidate env roots as active before any env work so the GC loop
	// can't reclaim artifacts inside them mid-execution. We mark both the
	// predicted root for a fresh Prepare and the prior root for Reuse — they
	// usually differ (Reuse keeps the original task's directory).
	predictedRoot := execenv.PredictRootDir(d.cfg.WorkspacesRoot, task.WorkspaceID, task.ID)
	d.markActiveEnvRoot(predictedRoot)
	defer d.unmarkActiveEnvRoot(predictedRoot)
	if task.PriorWorkDir != "" {
		priorRoot := filepath.Dir(task.PriorWorkDir)
		if priorRoot != predictedRoot {
			d.markActiveEnvRoot(priorRoot)
			defer d.unmarkActiveEnvRoot(priorRoot)
		}
	}

	// Try to reuse the workdir from a previous task on the same (agent, issue) pair.
	var env *execenv.Environment
	var agentMcpConfig json.RawMessage
	if task.Agent != nil {
		agentMcpConfig = task.Agent.McpConfig
	}
	var issueSpace *preparedIssueExecutionSpace
	if issueExecutionSpaceEnabled(task) && executionPolicy.CanAccessRepo {
		var err error
		issueSpace, err = d.prepareIssueExecutionSpace(task, agentName)
		if err != nil {
			return TaskResult{}, fmt.Errorf("prepare issue execution space: %w", err)
		}
		projectSkills, err := loadProjectSkillsForPolicy(issueSpace.PrimaryRepoDir, executionPolicy)
		if err != nil {
			return TaskResult{}, fmt.Errorf("load project skills overlay: %w", err)
		}
		if len(projectSkills) > 0 {
			taskCtx.AgentSkills = mergeSkillContexts(taskCtx.AgentSkills, projectSkills)
			taskLog.Info("project skills overlay loaded",
				"repo_dir", issueSpace.PrimaryRepoDir,
				"mode", executionPolicy.ProjectSkillMode,
				"skills", len(projectSkills),
			)
		}
	}
	if task.PriorWorkDir != "" && issueSpace == nil {
		var err error
		env, err = execenv.Reuse(execenv.ReuseParams{
			WorkDir:   task.PriorWorkDir,
			Provider:  provider,
			McpConfig: agentMcpConfig,
			Task:      taskCtx,
		}, d.logger)
		if err != nil {
			return TaskResult{}, fmt.Errorf("reuse execution environment: %w", err)
		}
	}
	if env == nil {
		var err error
		prepParams := execenv.PrepareParams{
			WorkspacesRoot: d.cfg.WorkspacesRoot,
			WorkspaceID:    task.WorkspaceID,
			TaskID:         task.ID,
			AgentName:      agentName,
			Provider:       provider,
			McpConfig:      agentMcpConfig,
			Task:           taskCtx,
		}
		if issueSpace != nil {
			prepParams.ManagedWorkDir = managedWorkDirForIssueSpace(issueSpace)
		}
		env, err = execenv.Prepare(prepParams, d.logger)
		if err != nil {
			return TaskResult{}, fmt.Errorf("prepare execution environment: %w", err)
		}
	}
	// Belt-and-suspenders: also mark whatever root we ended up with, in case
	// future changes diverge from PredictRootDir.
	if env.RootDir != predictedRoot && env.RootDir != "" {
		d.markActiveEnvRoot(env.RootDir)
		defer d.unmarkActiveEnvRoot(env.RootDir)
	}
	if issueSpace != nil {
		d.markActiveEnvRoot(issueSpace.RootDir)
		defer d.unmarkActiveEnvRoot(issueSpace.RootDir)
	}

	// Expose running state only after the work directory exists.
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		if isTaskStartConflictError(err) {
			return TaskResult{}, errTaskStartConflict
		}
		return TaskResult{}, fmt.Errorf("start task failed: %w", err)
	}
	_ = d.client.ReportProgress(ctx, task.ID, fmt.Sprintf("Launching %s", provider), 1, 2)

	reused := gateResumeToReusedWorkdir(&task, &taskCtx, env.WorkDir, taskLog)

	// Inject runtime-specific config (meta skill) so the agent discovers .agent_context/.
	runtimeBrief, err := execenv.InjectRuntimeConfig(env.WorkDir, provider, taskCtx)
	if err != nil {
		return TaskResult{}, fmt.Errorf("inject runtime config: %w", err)
	}
	// Workdir is preserved for reuse by future cloud tasks on the same
	// (agent, issue) pair only when the server does not provide an
	// issue-scoped managed worktree. The work_dir path is stored in DB on
	// task completion and passed back via PriorWorkDir on the next claim.
	//
	// Managed worktrees and local-directory paths are cleaned on exit
	// so the runtime config marker does not leak into repository files.
	if env.LocalDirectory || env.ManagedWorktree {
		defer func() {
			if cerr := execenv.CleanupRuntimeConfig(env.WorkDir, provider); cerr != nil {
				d.logger.Warn("execenv: cleanup runtime config failed (non-fatal)", "error", cerr)
			}
			// Excise the sidecar tree (.agent_context/, .multica/,
			// provider-specific .claude/skills/ etc.) that Prepare wrote
			// into the user's repo. Without this pass the user's tree
			// accumulates one directory layer per task — see MUL-2784.
			// CleanupRuntimeConfig handles the runtime brief inside
			// CLAUDE.md / AGENTS.md / GEMINI.md; CleanupSidecars handles
			// every other file Prepare placed under WorkDir. Together
			// they round-trip the workdir to its exact pre-task bytes.
			if cerr := execenv.CleanupSidecars(env.RootDir); cerr != nil {
				d.logger.Warn("execenv: cleanup sidecars failed (non-fatal)", "error", cerr)
			}
		}()
	}

	prompt := BuildPrompt(task)

	// Pass task-scoped auth credentials and context so the spawned agent CLI
	// can call the Multica API and the local daemon (e.g. `multica repo checkout`).
	// MULTICA_TASK_SLOT is allocated from the daemon-wide concurrency pool, not
	// per-agent. When one daemon hosts multiple agents, slots index shared
	// daemon-level resources such as GPUs.
	// MULTICA_TOKEN is bound to (agent, task) by the server. Never fall back
	// to the daemon's own credential here: doing so lets agent CLI writes land
	// as the runtime owner's member actor and can retrigger the same agent.
	agentToken, err := taskScopedAuthToken(task)
	if err != nil {
		taskLog.Error("task auth token invalid; refusing to start agent", "error", err)
		return TaskResult{}, err
	}
	agentEnv := map[string]string{
		"MULTICA_TOKEN":        agentToken,
		"MULTICA_SERVER_URL":   d.cfg.ServerBaseURL,
		"MULTICA_DAEMON_PORT":  fmt.Sprintf("%d", d.cfg.HealthPort),
		"MULTICA_WORKSPACE_ID": task.WorkspaceID,
		"MULTICA_AGENT_NAME":   agentName,
		"MULTICA_AGENT_ID":     task.AgentID,
		"MULTICA_TASK_ID":      task.ID,
		"MULTICA_TASK_SLOT":    strconv.Itoa(slot),
	}
	if issueSpace != nil {
		agentEnv["MULTICA_ISSUE_SPACE_ROOT"] = issueSpace.RootDir
		agentEnv["MULTICA_ARTIFACT_DIR"] = issueSpace.ArtifactDir
		if executionPolicy.CanAccessRepo {
			agentEnv["MULTICA_PRIMARY_REPO_DIR"] = issueSpace.PrimaryRepoDir
		}
	}
	if task.AutopilotRunID != "" {
		agentEnv["MULTICA_AUTOPILOT_RUN_ID"] = task.AutopilotRunID
	}
	if task.AutopilotID != "" {
		agentEnv["MULTICA_AUTOPILOT_ID"] = task.AutopilotID
	}
	// Quick-create marker — when set, the multica CLI's `issue create`
	// command stamps the new issue with origin_type=quick_create +
	// origin_id=<task_id> so the completion handler can find it
	// deterministically (see GetIssueByOrigin).
	if task.QuickCreatePrompt != "" {
		agentEnv["MULTICA_QUICK_CREATE_TASK_ID"] = task.ID
		if len(task.QuickCreateAttachmentIDs) > 0 {
			if raw, err := json.Marshal(task.QuickCreateAttachmentIDs); err == nil {
				agentEnv["MULTICA_QUICK_CREATE_ATTACHMENT_IDS"] = string(raw)
			} else {
				taskLog.Warn("quick-create attachment ids: marshal failed; skipping env injection", "error", err)
			}
		}
	}
	// Ensure the multica CLI is on PATH inside the agent's environment.
	// Some runtimes (e.g. Codex) run in an isolated sandbox that may not
	// inherit the daemon's PATH. Prepend the directory of the running
	// multica binary so that `multica` commands in the agent always resolve.
	if selfBin, err := os.Executable(); err == nil {
		binDir := filepath.Dir(selfBin)
		agentEnv["PATH"] = binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	// Point Codex to the per-task CODEX_HOME so it discovers skills natively
	// without polluting the system ~/.codex/skills/.
	if env.CodexHome != "" {
		agentEnv["CODEX_HOME"] = env.CodexHome
	}
	// Point Cursor at per-task project state when managed MCP is present.
	// The workdir .cursor/mcp.json carries the managed server list, while
	// CURSOR_DATA_DIR isolates the matching project approvals from the user's
	// persistent ~/.cursor/projects state.
	if env.CursorDataDir != "" {
		agentEnv["CURSOR_DATA_DIR"] = env.CursorDataDir
	}
	// Inject user-configured custom environment variables (e.g. ANTHROPIC_API_KEY,
	// ANTHROPIC_BASE_URL for router/proxy mode, or CLAUDE_CODE_USE_BEDROCK for
	// Bedrock). These are set per-agent via the agent settings UI.
	// Critical internal variables are blocklisted to prevent accidental or
	// malicious override of daemon-set values.
	if task.Agent != nil {
		for k, v := range task.Agent.CustomEnv {
			if isBlockedEnvKey(k) {
				d.logger.Warn("custom_env: blocked key skipped", "key", k)
				continue
			}
			agentEnv[k] = v
		}
	}
	if provider == "codebuddy" && governedLifeTask {
		applyCodeBuddyLifeIsolation(agentEnv, env.RootDir)
	}
	if provider == "codex" {
		// Codex keeps one app-server process per runtime and does not change its
		// working directory between turns. Point the governed MCP child at the
		// task-local context file without putting the task token in the broker
		// process environment.
		agentEnv["MULTICA_TASK_CONTEXT_FILE"] = filepath.Join(env.WorkDir, taskContextEnvRelPath)
		cleanupTaskEnv, err := writeTaskContextEnv(env.WorkDir, agentEnv)
		if err != nil {
			return TaskResult{}, fmt.Errorf("write codex task context env: %w", err)
		}
		defer cleanupTaskEnv()
		agentEnv, err = prepareCodexBrokerProcessEnv(agentEnv, env.CodexHome, d.logger)
		if err != nil {
			return TaskResult{}, err
		}
	}
	agentCfg := agent.Config{
		ExecutablePath: entry.Path,
		Env:            agentEnv,
		Logger:         d.logger,
	}
	var backend agent.Backend
	if provider == "codex" {
		backend = d.codexBrokerBackend(task.RuntimeID, agentCfg)
	} else {
		var err error
		backend, err = agent.New(provider, agentCfg)
		if err != nil {
			return TaskResult{}, fmt.Errorf("create agent backend: %w", err)
		}
	}

	taskLog.Info("starting agent",
		"provider", provider,
		"workdir", env.WorkDir,
		"model", entry.Model,
		"reused", reused,
	)
	if task.PriorSessionID != "" {
		taskLog.Info("resuming session", "session_id", task.PriorSessionID)
	}

	taskStart := time.Now()
	repoStatusBefore := ""
	if issueSpace != nil && !executionPolicy.CanEditRepo {
		if status, err := gitPorcelainStatus(ctx, issueSpace.PrimaryRepoDir); err == nil {
			repoStatusBefore = status
		} else {
			taskLog.Warn("repo edit guard: before status unavailable", "error", err, "repo_dir", issueSpace.PrimaryRepoDir)
		}
	}

	var agentCustomArgs []string
	extraArgs := defaultArgsForProvider(d.cfg, provider)
	var mcpConfig json.RawMessage
	if task.Agent != nil {
		agentCustomArgs = task.Agent.CustomArgs
		mcpConfig = task.Agent.McpConfig
	}
	if governedLifeCognition {
		var err error
		mcpConfig, err = governedLifeMCPConfig()
		if err != nil {
			return TaskResult{}, err
		}
	}
	customArgs := runtimeProfileCustomArgs(profileFixedArgs, agentCustomArgs)
	if provider == "codebuddy" && governedLifeTask {
		customArgs = append(customArgs, "--no-session-persistence")
	}
	// Two-tier model resolution: an explicit agent.model wins,
	// then the daemon-wide MULTICA_<PROVIDER>_MODEL env var. If
	// both are empty we deliberately pass "" through — each
	// backend omits `--model` from the CLI invocation, so the
	// provider picks its own default (Claude Code's shipped
	// default, codex app-server's account-scoped default, etc.).
	// Baking a Go-side "recommended default" here is how the
	// cursor regression happened — static guesses drift from
	// whatever the upstream CLI actually accepts.
	model := ""
	if task.Agent != nil && task.Agent.Model != "" {
		model = task.Agent.Model
	}
	if model == "" {
		model = entry.Model
	}
	thinkingLevel := ""
	if task.Agent != nil {
		thinkingLevel = task.Agent.ThinkingLevel
	}
	// Per-model guard: the server validates the literal token against the
	// provider's enum, but per-model gaps (Claude's `xhigh` on a non-Opus
	// model, Codex's per-model `supported_reasoning_levels`) only resolve
	// here, against the daemon's local CLI catalog. Invalid combinations
	// log a warning and drop the level rather than failing the task, so a
	// stale persisted value never blocks execution. Empty model is passed
	// through unchanged — ValidateThinkingLevel resolves it to the
	// provider's default model internally so default-model tasks aren't
	// misjudged. Discovery errors fail open: if we can't list models, we
	// keep the persisted level and let the CLI surface any objection.
	if thinkingLevel != "" {
		ok, err := agent.ValidateThinkingLevel(ctx, provider, entry.Path, model, thinkingLevel)
		if err != nil {
			taskLog.Warn("thinking_level: catalog lookup failed; passing through",
				"provider", provider,
				"model", model,
				"thinking_level", thinkingLevel,
				"error", err,
			)
		} else if !ok {
			taskLog.Warn("thinking_level: not valid for this (provider, model); skipping injection",
				"provider", provider,
				"model", model,
				"thinking_level", thinkingLevel,
			)
			thinkingLevel = ""
		}
	}
	toolPolicy := executionPolicyForToolEnvelope(task, executionPolicy)
	executionTimeout := taskExecutionTimeout(d.cfg.AgentTimeout, governedLifeTask)
	execOpts := agent.ExecOptions{
		Cwd:                       env.WorkDir,
		Model:                     model,
		ThreadName:                deriveTaskThreadName(task),
		MaxTurns:                  maxTurnsForExecutionPolicy(0, toolPolicy),
		Timeout:                   executionTimeout,
		SemanticInactivityTimeout: d.cfg.CodexSemanticInactivityTimeout,
		ResumeSessionID:           task.PriorSessionID,
		DisableSessionPersistence: governedLifeTask,
		ExtraArgs:                 extraArgs,
		CustomArgs:                customArgs,
		AllowedBuiltinTools:       allowedBuiltinToolsForExecutionPolicy(provider, toolPolicy),
		AllowedTools:              allowedToolsForExecutionPolicy(provider, toolPolicy),
		DisallowedTools:           disallowedToolsForExecutionPolicy(provider, toolPolicy),
		PermissionMode:            permissionModeForExecutionPolicy(provider, toolPolicy),
		McpConfig:                 mcpConfig,
		ThinkingLevel:             thinkingLevel,
	}
	// Kiro/Kimi cannot rely on workdir runtime files; repository-less Claude
	// coordinators also need the brief because their tool envelope cannot read it.
	if needsInlineSystemPrompt(provider, toolPolicy) {
		execOpts.SystemPrompt = runtimeBrief
	}

	taskLog.Debug("invoking backend",
		"provider", provider,
		"model", model,
		"prompt_bytes", len(prompt),
		"custom_args", len(customArgs),
		"extra_args", len(extraArgs),
		"allowed_builtin_tools", execOpts.AllowedBuiltinTools,
		"allowed_tools", execOpts.AllowedTools,
		"disallowed_tools", execOpts.DisallowedTools,
		"permission_mode", execOpts.PermissionMode,
		"mcp_config", len(mcpConfig) > 0,
		"inline_system_prompt", execOpts.SystemPrompt != "",
		"resume_session", execOpts.ResumeSessionID != "",
		"timeout", execOpts.Timeout,
	)

	result, tools, err := d.executeAndDrain(ctx, backend, prompt, execOpts, taskLog, task.ID)
	if err != nil {
		return TaskResult{}, err
	}
	if governedLifeTask {
		result.SessionID = ""
	}

	// Fallback: if session resume failed before establishing a session, retry
	// with a fresh session. We check SessionID == "" to distinguish a resume
	// failure (no session established) from a failure during actual execution.
	if result.Status == "failed" && task.PriorSessionID != "" && result.SessionID == "" {
		firstUsage := result.Usage
		taskLog.Warn("session resume failed, retrying with fresh session", "error", result.Error)
		execOpts.ResumeSessionID = ""
		retryResult, retryTools, retryErr := d.executeAndDrain(ctx, backend, prompt, execOpts, taskLog, task.ID)
		if retryErr != nil {
			taskLog.Error("fresh session also failed to start", "error", retryErr)
		} else {
			result = retryResult
			result.Usage = mergeUsage(firstUsage, result.Usage)
			tools = retryTools
		}
	}
	repoEditViolation := ""
	if issueSpace != nil && !executionPolicy.CanEditRepo && repoStatusBefore != "" {
		if status, err := gitPorcelainStatus(ctx, issueSpace.PrimaryRepoDir); err != nil {
			taskLog.Warn("repo edit guard: after status unavailable", "error", err, "repo_dir", issueSpace.PrimaryRepoDir)
		} else if status != repoStatusBefore {
			repoEditViolation = fmt.Sprintf("角色 %s 不允许修改仓库，但本次运行改变了 %s 的 git 工作区状态。请由 04-开发等允许编辑的角色执行代码改动。", firstNonEmptyString(executionPolicy.RoleKey, executionPolicy.RoleKind, "current"), issueSpace.PrimaryRepoDir)
			taskLog.Warn("repo edit guard blocked non-editing role",
				"role_key", executionPolicy.RoleKey,
				"role_kind", executionPolicy.RoleKind,
				"repo_dir", issueSpace.PrimaryRepoDir,
			)
		}
	}

	elapsed := time.Since(taskStart).Round(time.Second)
	taskLog.Info("agent finished",
		"status", result.Status,
		"duration", elapsed.String(),
		"tools", tools,
	)
	taskLog.Debug("agent result detail",
		"status", result.Status,
		"output_bytes", len(result.Output),
		"session_id", result.SessionID,
		"models_with_usage", len(result.Usage),
		"agent_error", result.Error,
	)

	// Convert agent usage map to task usage entries.
	var usageEntries []protocol.TaskUsage
	for model, u := range result.Usage {
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.CacheWriteTokens == 0 {
			continue
		}
		usageEntries = append(usageEntries, protocol.TaskUsage{
			Provider:         provider,
			Model:            model,
			InputTokens:      u.InputTokens,
			OutputTokens:     u.OutputTokens,
			CacheReadTokens:  u.CacheReadTokens,
			CacheWriteTokens: u.CacheWriteTokens,
		})
	}

	resultBranchName := ""
	resultArtifactDir := ""
	if issueSpace != nil {
		resultBranchName = issueSpace.BranchName
		resultArtifactDir = issueSpace.ArtifactDir
	}
	if repoEditViolation != "" {
		return TaskResult{
			Status:        "blocked",
			Comment:       repoEditViolation,
			BranchName:    resultBranchName,
			SessionID:     result.SessionID,
			WorkDir:       env.WorkDir,
			ArtifactDir:   resultArtifactDir,
			EnvRoot:       env.RootDir,
			FailureReason: "role_policy_violation",
			Usage:         usageEntries,
		}, nil
	}

	switch result.Status {
	case "completed":
		if result.Output == "" {
			// The agent completed successfully but produced no text output.
			// This is valid — the agent may have done all its work via tool
			// calls (e.g. posting comments via CLI, pushing code). Treat as
			// a normal completion so the task is not incorrectly marked as
			// blocked.
			return TaskResult{
				Status:      "completed",
				Comment:     "",
				BranchName:  resultBranchName,
				SessionID:   result.SessionID,
				WorkDir:     env.WorkDir,
				ArtifactDir: resultArtifactDir,
				EnvRoot:     env.RootDir,
				Usage:       usageEntries,
			}, nil
		}
		// Detect "poisoned" terminal output: the agent didn't reach a real
		// conclusion but emitted a known fallback marker (iteration limit,
		// fallback meta message). Route through the blocked path with a
		// specific failure_reason so the server can exclude this session
		// from the (agent_id, issue_id) resume lookup — otherwise a manual
		// rerun would inherit the same poisoned session and reproduce the
		// same bad output.
		if reason, ok := classifyPoisonedOutput(result.Output); ok {
			taskLog.Warn("agent finished with poisoned fallback output, classifying as blocked",
				"failure_reason", reason,
			)
			return TaskResult{
				Status:        "blocked",
				Comment:       result.Output,
				BranchName:    resultBranchName,
				SessionID:     result.SessionID,
				WorkDir:       env.WorkDir,
				ArtifactDir:   resultArtifactDir,
				EnvRoot:       env.RootDir,
				Usage:         usageEntries,
				FailureReason: reason,
			}, nil
		}
		return TaskResult{
			Status:      "completed",
			Comment:     result.Output,
			BranchName:  resultBranchName,
			SessionID:   result.SessionID,
			WorkDir:     env.WorkDir,
			ArtifactDir: resultArtifactDir,
			EnvRoot:     env.RootDir,
			Usage:       usageEntries,
		}, nil
	case "timeout":
		// Surface session_id/work_dir so the task-owned resume pointer survives
		// when the agent times out after building a session.
		// We mark as "blocked" (not a hard error return) so handleTask
		// goes through the FailTask path that forwards session info.
		comment := result.Error
		if comment == "" {
			comment = fmt.Sprintf("%s timed out after %s", provider, execOpts.Timeout)
		}
		failureReason := "timeout"
		if reason, ok := classifyResumeUnsafeTimeout(provider, comment); ok {
			taskLog.Warn("agent timed out with resume-unsafe session, classifying as blocked",
				"failure_reason", reason,
			)
			failureReason = reason
		}
		return TaskResult{
			Status:        "blocked",
			Comment:       comment,
			BranchName:    resultBranchName,
			SessionID:     result.SessionID,
			WorkDir:       env.WorkDir,
			ArtifactDir:   resultArtifactDir,
			EnvRoot:       env.RootDir,
			FailureReason: failureReason,
			Usage:         usageEntries,
		}, nil
	case "idle_watchdog":
		// The idle watchdog force-stopped the run because the backend
		// went silent (e.g. claude blocked on a tool call against a
		// frozen child process). Route through the blocked path with a
		// dedicated failure_reason so the run leaves "running" state and
		// operators can tell idle-stop apart from a real timeout.
		comment := result.Error
		if comment == "" {
			comment = idleWatchdogReason(d.cfg.AgentIdleWatchdog)
		}
		return TaskResult{
			Status:        "blocked",
			Comment:       comment,
			BranchName:    resultBranchName,
			SessionID:     result.SessionID,
			WorkDir:       env.WorkDir,
			ArtifactDir:   resultArtifactDir,
			EnvRoot:       env.RootDir,
			FailureReason: "idle_watchdog",
			Usage:         usageEntries,
		}, nil
	case "cancelled":
		// Server cancelled the task (e.g. issue reassignment, user cancel).
		// handleTask's cancelledByPoll branch already discards this result,
		// so this case is mainly defensive — and preserves the "cancelled"
		// status string for the "agent finished" log line so operators can
		// distinguish "task cancelled by server" from a real timeout.
		return TaskResult{
			Status:      "cancelled",
			Comment:     "task cancelled by server",
			BranchName:  resultBranchName,
			SessionID:   result.SessionID,
			WorkDir:     env.WorkDir,
			ArtifactDir: resultArtifactDir,
			EnvRoot:     env.RootDir,
			Usage:       usageEntries,
		}, nil
	default:
		errMsg := agentFailureMessage(provider, result)
		// Forward SessionID/WorkDir on the blocked path: backends commonly
		// emit a real session_id before failing (rate-limit, tool error,
		// model reject, …). Without this the task row would lose the pointer
		// needed by the next chat turn.
		//
		// Request bodies embedded in a rejected session are not resumable.
		failureReason, _ := classifyPoisonedError(errMsg)
		if failureReason != "" {
			taskLog.Warn("agent failed with poisoned API error, classifying as blocked",
				"failure_reason", failureReason,
			)
		} else {
			// All other failures use the canonical agent-error classifier.
			failureReason = taskfailure.Classify(errMsg).String()
		}
		return TaskResult{
			Status:        "blocked",
			Comment:       errMsg,
			BranchName:    resultBranchName,
			SessionID:     result.SessionID,
			WorkDir:       env.WorkDir,
			ArtifactDir:   resultArtifactDir,
			EnvRoot:       env.RootDir,
			Usage:         usageEntries,
			FailureReason: failureReason,
		}, nil
	}
}

func runtimeProfileCustomArgs(fixedArgs, agentArgs []string) []string {
	args := make([]string, 0, len(fixedArgs)+len(agentArgs))
	args = append(args, fixedArgs...)
	return append(args, agentArgs...)
}

func isGovernedLifeTask(task Task) bool {
	return task.IsCompanion || task.LifeJobID != ""
}

func applyCodeBuddyLifeIsolation(env map[string]string, rootDir string) {
	env["CODEBUDDY_CONFIG_DIR"] = filepath.Join(rootDir, "codebuddy-life")
	env["CODEBUDDY_CODE_DISABLE_AUTO_MEMORY"] = "1"
	env["CODEBUDDY_DISABLE_AUTO_MEMORY"] = "1"
	env["CODEBUDDY_MEMORY_ENABLED"] = "false"
}

func agentFailureMessage(provider string, result agent.Result) string {
	if result.Error != "" {
		return result.Error
	}
	if result.Output != "" {
		return result.Output
	}
	return fmt.Sprintf("%s execution %s", provider, result.Status)
}

// executeAndDrain runs a backend, drains its message stream (forwarding to the
// server), and waits for the final result.
