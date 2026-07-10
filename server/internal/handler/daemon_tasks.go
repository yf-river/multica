package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) ClaimTaskByRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	start := time.Now()

	var (
		outcome                  = "unauth"
		authMs, claimMs, buildMs int64
		buildStart               time.Time
	)
	defer func() {
		// Emit at function exit so error / unauth paths also carry timing.
		// build_ms is computed from buildStart only when we entered the
		// response-build phase (otherwise stays 0).
		if !buildStart.IsZero() {
			buildMs = time.Since(buildStart).Milliseconds()
		}
		logClaimEndpointSlow(runtimeID, outcome, start, authMs, claimMs, buildMs)
	}()

	// Verify the caller owns this runtime's workspace. The runtime's
	// workspace_id is the authoritative value a claimed task must match
	// below — a task whose resolved workspace doesn't equal this runtime's
	// workspace is rejected even if it was enqueued against this
	// runtime_id (defense-in-depth against upstream routing bugs).
	runtime, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	runtimeWorkspaceID := uuidToString(runtime.WorkspaceID)
	authMs = time.Since(start).Milliseconds()

	claimStart := time.Now()
	task, err := h.TaskService.ClaimTaskForRuntime(r.Context(), parseUUID(runtimeID))
	claimMs = time.Since(claimStart).Milliseconds()
	if err != nil {
		outcome = "error_claim"
		writeError(w, http.StatusInternalServerError, "failed to claim task: "+err.Error())
		return
	}

	if task == nil {
		slog.Debug("no task to claim", "runtime_id", runtimeID)
		writeJSON(w, http.StatusOK, map[string]any{"task": nil})
		outcome = "no_task"
		return
	}

	outcome = "claimed"
	buildStart = time.Now()

	// Build response with fresh agent data (name + skills + custom_env + custom_args).
	resp := taskToResponse(*task, runtimeWorkspaceID)
	if agent, err := h.Queries.GetAgent(r.Context(), task.AgentID); err == nil {
		executionPolicy := taskExecutionPolicyForAgent(agent, false)
		// Workspace-bound skills first, then platform built-in skills. Built-in
		// names carry a "multica-" prefix so their on-disk slugs never collide
		// with a user-authored workspace skill (see writeSkillFiles).
		skills := h.TaskService.LoadAgentSkills(r.Context(), task.AgentID)
		skills = filterAgentSkillsForExecutionPolicy(skills, executionPolicy)
		skills = append(skills, filterBuiltinSkillsForExecutionPolicy(h.TaskService.BuiltinSkills(), executionPolicy)...)
		var customEnv map[string]string
		if agent.CustomEnv != nil {
			if err := json.Unmarshal(agent.CustomEnv, &customEnv); err != nil {
				slog.Warn("failed to unmarshal agent custom_env", "agent_id", uuidToString(agent.ID), "error", err)
			}
		}
		var customArgs []string
		if agent.CustomArgs != nil {
			if err := json.Unmarshal(agent.CustomArgs, &customArgs); err != nil {
				slog.Warn("failed to unmarshal agent custom_args", "agent_id", uuidToString(agent.ID), "error", err)
			}
		}
		var mcpConfig json.RawMessage
		if agent.McpConfig != nil {
			mcpConfig = json.RawMessage(agent.McpConfig)
		}
		// runtime_config is stored as JSONB and may legitimately be the
		// empty object `{}` for agents that haven't opted into any
		// provider-specific tuning. Forward only non-empty payloads so the
		// daemon's per-provider decoders treat absent-or-empty identically.
		var runtimeConfig json.RawMessage
		if rc := bytes.TrimSpace(agent.RuntimeConfig); len(rc) > 0 && !bytes.Equal(rc, []byte("{}")) && !bytes.Equal(rc, []byte("null")) {
			runtimeConfig = json.RawMessage(agent.RuntimeConfig)
		}
		resp.Agent = &TaskAgentData{
			ID:            uuidToString(agent.ID),
			Name:          agent.Name,
			Instructions:  agent.Instructions,
			Skills:        skills,
			CustomEnv:     customEnv,
			CustomArgs:    customArgs,
			McpConfig:     mcpConfig,
			Model:         agent.Model.String,
			ThinkingLevel: agent.ThinkingLevel.String,
			RuntimeConfig: runtimeConfig,
		}
		resp.ExecutionPolicy = &executionPolicy
	}

	// Resolve the runtime owner's profile description so the daemon can
	// inject "## Requesting User" into the brief. Empty fields short-circuit
	// the heading entirely on the daemon side; cloud / system runtimes with
	// no owner stay anonymous. Failure here must not block claim — the agent
	// can still run without the user-context section.
	if runtime.OwnerID.Valid {
		if owner, err := h.Queries.GetUser(r.Context(), runtime.OwnerID); err == nil {
			resp.RequestingUserName = owner.Name
			resp.RequestingUserProfileDescription = owner.ProfileDescription
		} else {
			slog.Debug("failed to load runtime owner for brief injection",
				"runtime_id", runtimeID,
				"owner_id", uuidToString(runtime.OwnerID),
				"error", err,
			)
		}
	}

	// Stored task initiator: chat tasks persist the real message sender at
	// enqueue time (web: request user; Lark: inbound sender — NOT the chat
	// session creator, which for Lark groups is the installer). When set, it is
	// the authoritative initiator for this run; resolve the live name/account so
	// the daemon can render `## Task Initiator`. Comment-triggered tasks instead
	// resolve their initiator from the triggering comment's author below; the
	// two paths are mutually exclusive (a task is either chat or issue-bound).
	// See MUL-2645.
	if task.InitiatorUserID.Valid {
		resp.InitiatorType = "member"
		resp.InitiatorID = uuidToString(task.InitiatorUserID)
		if u, err := h.Queries.GetUser(r.Context(), task.InitiatorUserID); err == nil {
			resp.InitiatorName = u.Name
			resp.InitiatorAccount = u.Account
		}
	}

	// Include workspace ID and repos so the daemon can set up worktrees.
	//
	// Repo precedence: project-bound github_repo resources override workspace
	// repos when present. Mixing both would just confuse the agent — if a
	// project explicitly attached its repos, those are the authoritative set
	// for issues inside that project. When the project has no github_repo
	// resources (or no project at all), we fall back to the workspace repos.
	var issueForSource db.Issue
	hasIssueForSource := false
	suppressIssueReposForRole := false
	if task.IssueID.Valid {
		if issue, err := h.Queries.GetIssue(r.Context(), task.IssueID); err == nil {
			issueForSource = issue
			hasIssueForSource = true
			resp.WorkspaceID = uuidToString(issue.WorkspaceID)
			resp.ThreadName = issue.Title

			// Squad-leader briefing injection: when the issue is assigned
			// to a squad and the claiming agent is that squad's current
			// leader, append a full briefing (Operating Protocol + Roster
			// + user Instructions) to the agent's own Instructions. We
			// append (not replace) so per-agent instructions remain
			// authoritative for general behavior; the squad briefing
			// stacks on top as task-specific squad context.
			if resp.Agent != nil && issue.AssigneeType.Valid && issue.AssigneeType.String == "squad" && issue.AssigneeID.Valid {
				if squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
					ID:          issue.AssigneeID,
					WorkspaceID: issue.WorkspaceID,
				}); err == nil && uuidToString(squad.LeaderID) == resp.Agent.ID {
					briefing := buildSquadLeaderBriefing(r.Context(), h.Queries, squad)
					if strings.TrimSpace(resp.Agent.Instructions) == "" {
						resp.Agent.Instructions = briefing
					} else {
						resp.Agent.Instructions = resp.Agent.Instructions + "\n\n" + briefing
					}
					leaderPolicy := taskExecutionPolicyForRole("", resp.Agent.Name, true)
					resp.ExecutionPolicy = &leaderPolicy
					resp.Agent.Skills = filterAgentSkillsForExecutionPolicy(resp.Agent.Skills, leaderPolicy)
					slog.Debug("injected squad leader briefing",
						"squad_id", uuidToString(squad.ID),
						"squad_name", squad.Name,
						"leader_agent_id", resp.Agent.ID,
					)
				}
			}
			if resp.ExecutionPolicy != nil && !resp.ExecutionPolicy.CanAccessRepo {
				suppressIssueReposForRole = true
			}

			var projectRepos []RepoData
			projectRepoRef := ""
			if issue.ProjectID.Valid {
				resp.ProjectID = uuidToString(issue.ProjectID)
				if proj, err := h.Queries.GetProject(r.Context(), issue.ProjectID); err == nil {
					resp.ProjectTitle = proj.Title
				}
				if rows := h.listProjectResourcesForProject(r.Context(), issue.ProjectID); len(rows) > 0 && !suppressIssueReposForRole {
					out := make([]ProjectResourceData, 0, len(rows))
					for _, row := range rows {
						label := ""
						if row.Label.Valid {
							label = row.Label.String
						}
						ref := json.RawMessage(row.ResourceRef)
						if len(ref) == 0 {
							ref = json.RawMessage("{}")
						}
						out = append(out, ProjectResourceData{
							ID:           uuidToString(row.ID),
							ResourceType: row.ResourceType,
							ResourceRef:  ref,
							Label:        label,
						})
						// Lift git-backed project resources into the daemon's repo list
						// so `multica repo checkout` and the meta-skill render
						// them as the issue's repos.
						if row.ResourceType == "github_repo" {
							var payload struct {
								URL               string `json:"url"`
								DefaultBranchHint string `json:"default_branch_hint"`
							}
							if json.Unmarshal(row.ResourceRef, &payload) == nil && payload.URL != "" {
								projectRepos = append(projectRepos, RepoData{URL: payload.URL})
								if projectRepoRef == "" {
									projectRepoRef = strings.TrimSpace(payload.DefaultBranchHint)
								}
							}
						} else if row.ResourceType == "gongfeng_repo" {
							var payload struct {
								URL         string `json:"url"`
								ProjectPath string `json:"project_path"`
								Ref         string `json:"ref"`
								Branch      string `json:"branch"`
							}
							if json.Unmarshal(row.ResourceRef, &payload) == nil {
								if cloneURL := canonicalGongfengCloneURL(payload.URL, payload.ProjectPath); cloneURL != "" {
									projectRepos = append(projectRepos, RepoData{URL: cloneURL})
									if projectRepoRef == "" {
										projectRepoRef = firstNonEmpty(strings.TrimSpace(payload.Branch), strings.TrimSpace(payload.Ref))
									}
								}
							}
						}
					}
					resp.ProjectResources = out
				}
			}

			if suppressIssueReposForRole {
				resp.ProjectResources = nil
				resp.Repos = nil
				resp.IssueExecutionSpace = nil
				slog.Debug("suppressed issue repos for squad leader task",
					"task_id", uuidToString(task.ID),
					"issue_id", uuidToString(issue.ID),
					"agent_id", uuidToString(task.AgentID),
				)
			} else if len(projectRepos) > 0 {
				resp.Repos = projectRepos
			} else if ws, err := h.Queries.GetWorkspace(r.Context(), issue.WorkspaceID); err == nil && ws.Repos != nil {
				var repos []RepoData
				if json.Unmarshal(ws.Repos, &repos) == nil && len(repos) > 0 {
					resp.Repos = repos
				}
			}
			if !suppressIssueReposForRole && len(projectRepos) > 0 {
				resp.IssueExecutionSpace = &IssueExecutionSpaceData{
					Enabled:        true,
					IssueID:        uuidToString(issue.ID),
					PrimaryRepoURL: projectRepos[0].URL,
					Ref:            projectRepoRef,
				}
			}
		}

		// Fetch the triggering comment content so the daemon can embed it
		// directly in the agent prompt (prevents the agent from ignoring comments
		// when stale output files exist in a reused workdir). Also surface the
		// comment author's kind and display name so the agent knows whether it
		// was triggered by a human or by another agent — a signal used by the
		// harness instructions to avoid mention loops between agents.
		if task.TriggerCommentID.Valid {
			if comment, err := h.Queries.GetComment(r.Context(), task.TriggerCommentID); err == nil {
				resp.TriggerCommentContent = comment.Content
				resp.TriggerThreadID = uuidToString(comment.ID)
				if comment.ParentID.Valid {
					resp.TriggerThreadID = uuidToString(comment.ParentID)
				}
				resp.TriggerAuthorType = comment.AuthorType
				// The triggering comment's author is the task initiator — the
				// real requester behind this run. Surface it (type + id + name,
				// plus account for members) so a workspace-visible agent can
				// attribute the request to the right person instead of to the
				// runtime owner. Same lookups as the display name above; we just
				// also capture the id and account. See MUL-2645.
				resp.InitiatorType = comment.AuthorType
				if comment.AuthorID.Valid {
					resp.InitiatorID = uuidToString(comment.AuthorID)
				}
				switch comment.AuthorType {
				case "agent":
					if comment.AuthorID.Valid {
						if a, err := h.Queries.GetAgent(r.Context(), comment.AuthorID); err == nil {
							resp.TriggerAuthorName = a.Name
							resp.InitiatorName = a.Name
						}
					}
				case "member":
					// For member-authored comments, AuthorID is a user UUID
					// (see handler.resolveActor) — look up the user's display name.
					if comment.AuthorID.Valid {
						if u, err := h.Queries.GetUser(r.Context(), comment.AuthorID); err == nil {
							resp.TriggerAuthorName = u.Name
							resp.InitiatorName = u.Name
							resp.InitiatorAccount = u.Account
						}
					}
				}
				// Count comments that arrived issue-wide since this agent's last
				// run, so the daemon can tell it the full catch-up volume up front
				// (the prompt then steers it to read the triggering thread first).
				// Anchor = the prior task's started_at (never completed_at: a long
				// run would miss comments posted while it ran). Cold start (no prior
				// task) → no anchor → no hint. Excludes the agent's own comments and
				// the triggering comment itself because that body is already
				// injected into the prompt. Best-effort: any DB error or zero count
				// leaves the hint suppressed.
				if startedAt, err := h.Queries.GetLastTaskStartedAtForIssueAndAgent(r.Context(), db.GetLastTaskStartedAtForIssueAndAgentParams{
					AgentID: task.AgentID,
					IssueID: comment.IssueID,
				}); err == nil && startedAt.Valid {
					if cnt, err := h.Queries.CountNewCommentsSince(r.Context(), db.CountNewCommentsSinceParams{
						AnchorID:    task.TriggerCommentID,
						IssueID:     comment.IssueID,
						WorkspaceID: comment.WorkspaceID,
						Since:       startedAt,
						AuthorID:    task.AgentID,
					}); err == nil && cnt > 0 {
						resp.NewCommentCount = int(cnt)
						resp.NewCommentsSince = startedAt.Time.UTC().Format(time.RFC3339)
					}
				}
			}
		}

		if hasIssueForSource {
			credentialUserID := issueForSource.CreatorID
			if resp.InitiatorType == "member" && resp.InitiatorID != "" {
				if initiatorID, err := util.ParseUUID(resp.InitiatorID); err == nil {
					credentialUserID = initiatorID
				}
			}
			resp.SourceContext = h.buildIssueSourceContext(r.Context(), issueForSource, credentialUserID)
			if resp.Agent != nil {
				resp.Agent.McpConfig = h.injectSourceCredentialMCPEnv(r.Context(), resp.Agent.McpConfig, resp.SourceContext)
			}
			if _, ok := service.ParseIssueSourceSummaryContext(*task); ok {
				resp.SourceSummaryPrompt = "基于任务的 TAPD 来源内容生成结构化需求摘要。"
				resp.ThreadName = "生成需求摘要：" + issueForSource.Title
			}
		}

		// Look up the prior session for this (agent, issue) pair so the daemon
		// can resume the Claude Code conversation context.
		//
		// Skip all prior state when the task was flagged as a manual rerun:
		// the user just judged the prior output bad, so the daemon must start a
		// fresh agent session in a fresh workdir instead of resuming anything
		// from the same conversation that produced that output.
		if !task.ForceFreshSession && !isNoRepoBoundedPolicy(resp.ExecutionPolicy) {
			if prior, err := h.Queries.GetLastTaskSession(r.Context(), db.GetLastTaskSessionParams{
				AgentID: task.AgentID,
				IssueID: task.IssueID,
			}); err == nil && prior.SessionID.Valid {
				// Resume the prior session when it ran on the same runtime —
				// including comment-triggered follow-ups, so the agent keeps the
				// issue's conversation context across turns. The "Focus on THIS
				// comment" guard in prompt.go defends against inheriting the prior
				// turn's "Done." marker, and GetLastTaskSession already excludes
				// poisoned sessions.
				if prior.RuntimeID == task.RuntimeID {
					resp.PriorSessionID = prior.SessionID.String
				}
				if prior.WorkDir.Valid {
					resp.PriorWorkDir = prior.WorkDir.String
				}
			}
		}
	}

	// Chat task: populate workspace/session info from the chat_session table.
	if task.ChatSessionID.Valid {
		if cs, err := h.Queries.GetChatSession(r.Context(), task.ChatSessionID); err == nil {
			resp.WorkspaceID = uuidToString(cs.WorkspaceID)
			resp.ChatSessionID = uuidToString(cs.ID)
			resp.ThreadName = cs.Title
			if ws, err := h.Queries.GetWorkspace(r.Context(), cs.WorkspaceID); err == nil && ws.Repos != nil {
				var repos []RepoData
				if json.Unmarshal(ws.Repos, &repos) == nil && len(repos) > 0 {
					resp.Repos = repos
				}
			}
			if !task.ForceFreshSession {
				// Resume chat sessions only when the stored pointer was produced
				// by the same runtime as the claiming task. When the chat_session
				// pointer is missing (legacy NULL runtime_id), stale (last task
				// failed before reporting completion), or runtime-mismatched, fall
				// back to the most recent task row that recorded a session_id —
				// otherwise a single failed turn would silently drop the entire
				// conversation memory on the next message. The fallback also
				// requires runtime to match.
				if cs.SessionID.Valid && cs.RuntimeID.Valid && cs.RuntimeID == task.RuntimeID {
					resp.PriorSessionID = cs.SessionID.String
				}
				if cs.WorkDir.Valid {
					resp.PriorWorkDir = cs.WorkDir.String
				}
				if prior, err := h.Queries.GetLastChatTaskSession(r.Context(), cs.ID); err == nil && prior.SessionID.Valid {
					if resp.PriorSessionID == "" && prior.RuntimeID == task.RuntimeID {
						resp.PriorSessionID = prior.SessionID.String
					}
					if prior.WorkDir.Valid && resp.PriorWorkDir == "" {
						resp.PriorWorkDir = prior.WorkDir.String
					}
				}
			}
			// Build the chat prompt from EVERY user message that has arrived
			// since the agent's last reply — not just the most recent one. A
			// short-window debounce (MUL-2968) can land several user messages
			// before a single run fires; the agent resumes its prior session
			// and only learns of new input through resp.ChatMessage, so
			// delivering just the latest message would silently drop the
			// earlier ones (e.g. "看上海天气" then "还有青岛" → only Qingdao
			// answered). The unanswered set is the trailing run of user
			// messages after the last assistant message (every completed or
			// failed run writes an assistant row, so that anchor advances each
			// turn). Attachments are collected from each included message so
			// the agent can `multica attachment download <id>` — the markdown
			// URL alone is signed and 30-min expiring on the private CDN.
			if msgs, err := h.Queries.ListChatMessages(r.Context(), cs.ID); err == nil && len(msgs) > 0 {
				unanswered := trailingUserMessages(msgs)
				parts := make([]string, 0, len(unanswered))
				for _, m := range unanswered {
					if strings.TrimSpace(m.Content) != "" {
						parts = append(parts, m.Content)
					}
					if atts, attErr := h.Queries.ListAttachmentsByChatMessage(r.Context(), db.ListAttachmentsByChatMessageParams{
						ChatMessageID: m.ID,
						WorkspaceID:   parseUUID(resp.WorkspaceID),
					}); attErr == nil && len(atts) > 0 {
						for _, a := range atts {
							resp.ChatMessageAttachments = append(resp.ChatMessageAttachments, ChatAttachmentMeta{
								ID:          uuidToString(a.ID),
								Filename:    a.Filename,
								ContentType: a.ContentType,
							})
						}
					}
				}
				resp.ChatMessage = strings.Join(parts, "\n\n")
				if strings.TrimSpace(resp.ThreadName) == "" {
					resp.ThreadName = resp.ChatMessage
				}
			}
		}
	}

	// Autopilot run_only task: resolve workspace from autopilot_run →
	// autopilot, and include the autopilot instructions because there is no
	// issue for the agent to fetch.
	if task.AutopilotRunID.Valid {
		if run, err := h.Queries.GetAutopilotRun(r.Context(), task.AutopilotRunID); err == nil {
			resp.AutopilotID = uuidToString(run.AutopilotID)
			resp.AutopilotSource = run.Source
			if run.TriggerPayload != nil {
				resp.AutopilotTriggerPayload = json.RawMessage(run.TriggerPayload)
			}
			if ap, err := h.Queries.GetAutopilot(r.Context(), run.AutopilotID); err == nil {
				resp.AutopilotTitle = ap.Title
				resp.ThreadName = ap.Title
				if ap.Description.Valid {
					resp.AutopilotDescription = ap.Description.String
				}
				if resp.WorkspaceID == "" {
					resp.WorkspaceID = uuidToString(ap.WorkspaceID)
				}
				if len(resp.Repos) == 0 {
					if ws, err := h.Queries.GetWorkspace(r.Context(), ap.WorkspaceID); err == nil && ws.Repos != nil {
						var repos []RepoData
						if json.Unmarshal(ws.Repos, &repos) == nil && len(repos) > 0 {
							resp.Repos = repos
						}
					}
				}
			}
		}
	}

	// Quick-create task: no issue / chat / autopilot link — workspace and
	// prompt come from the task's context JSONB. Resolve workspace from
	// there so the isolation check below has something to compare.
	hasQuickCreate := false
	if task.Context != nil && !task.IssueID.Valid && !task.ChatSessionID.Valid && !task.AutopilotRunID.Valid {
		var qc service.QuickCreateContext
		if json.Unmarshal(task.Context, &qc) == nil && qc.Type == service.QuickCreateContextType {
			hasQuickCreate = true
			resp.QuickCreatePrompt = qc.Prompt
			resp.QuickCreateAttachmentIDs = append([]string(nil), qc.AttachmentIDs...)
			resp.QuickCreateStatus = qc.Status
			resp.QuickCreatePriority = qc.Priority
			resp.QuickCreateAssigneeType = qc.AssigneeType
			resp.QuickCreateAssigneeID = qc.AssigneeID
			resp.QuickCreateStartDate = qc.StartDate
			resp.QuickCreateDueDate = qc.DueDate
			resp.ThreadName = qc.Prompt
			resp.WorkspaceID = qc.WorkspaceID

			// When the user picked a project in the modal, surface its title
			// and resources to the daemon so the agent has the same context
			// it would for an issue-bound task: the prompt template can name
			// the project, and `multica repo checkout` sees the project's
			// github_repo resources instead of the workspace fallback.
			var projectRepos []RepoData
			if qc.ProjectID != "" {
				projectUUID, err := util.ParseUUID(qc.ProjectID)
				if err == nil {
					resp.ProjectID = qc.ProjectID
					if proj, err := h.Queries.GetProject(r.Context(), projectUUID); err == nil {
						resp.ProjectTitle = proj.Title
					}
					if rows := h.listProjectResourcesForProject(r.Context(), projectUUID); len(rows) > 0 {
						out := make([]ProjectResourceData, 0, len(rows))
						for _, row := range rows {
							label := ""
							if row.Label.Valid {
								label = row.Label.String
							}
							ref := json.RawMessage(row.ResourceRef)
							if len(ref) == 0 {
								ref = json.RawMessage("{}")
							}
							out = append(out, ProjectResourceData{
								ID:           uuidToString(row.ID),
								ResourceType: row.ResourceType,
								ResourceRef:  ref,
								Label:        label,
							})
							if row.ResourceType == "github_repo" {
								var payload struct {
									URL string `json:"url"`
								}
								if json.Unmarshal(row.ResourceRef, &payload) == nil && payload.URL != "" {
									projectRepos = append(projectRepos, RepoData{URL: payload.URL})
								}
							} else if row.ResourceType == "gongfeng_repo" {
								var payload struct {
									URL         string `json:"url"`
									ProjectPath string `json:"project_path"`
								}
								if json.Unmarshal(row.ResourceRef, &payload) == nil {
									if cloneURL := canonicalGongfengCloneURL(payload.URL, payload.ProjectPath); cloneURL != "" {
										projectRepos = append(projectRepos, RepoData{URL: cloneURL})
									}
								}
							}
						}
						resp.ProjectResources = out
					}
				}
			}

			if len(projectRepos) > 0 {
				resp.Repos = projectRepos
			} else if ws, err := h.Queries.GetWorkspace(r.Context(), parseUUID(qc.WorkspaceID)); err == nil && ws.Repos != nil {
				var repos []RepoData
				if json.Unmarshal(ws.Repos, &repos) == nil && len(repos) > 0 {
					resp.Repos = repos
				}
			}

			// Parent-issue resolution for quick-create tasks opened from
			// "Add sub issue". The handler already verified workspace
			// membership at submit time; here we re-fetch to pull the
			// human-readable identifier (e.g. MUL-123) the agent will
			// reference in the prompt. If the parent was deleted between
			// submit and claim we surface the UUID anyway — the agent
			// still passes `--parent <uuid>` and the server-side create
			// will fail loud, which is a better outcome than silently
			// dropping the sub-issue intent.
			if qc.ParentIssueID != "" {
				resp.ParentIssueID = qc.ParentIssueID
				if parentUUID, err := util.ParseUUID(qc.ParentIssueID); err == nil {
					if wsUUID, wsErr := util.ParseUUID(qc.WorkspaceID); wsErr == nil {
						parent, perr := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
							ID:          parentUUID,
							WorkspaceID: wsUUID,
						})
						if perr == nil && parent.ID.Valid {
							if ws, werr := h.Queries.GetWorkspace(r.Context(), wsUUID); werr == nil {
								resp.ParentIssueIdentifier = ws.IssuePrefix + "-" + strconv.Itoa(int(parent.Number))
							}
						}
					}
				}
			}

			// Squad-leader briefing injection for quick-create tasks. When
			// the user picked a squad in the modal, the task runs on the
			// squad's leader agent (resolved by the handler). Surface the
			// same Operating Protocol + Roster + user Instructions that
			// issue-bound squad tasks see, so the leader can decide to
			// delegate before opening the issue.
			if resp.Agent != nil && qc.SquadID != "" {
				wsUUID, wsErr := util.ParseUUID(qc.WorkspaceID)
				squadUUID, sqErr := util.ParseUUID(qc.SquadID)
				if wsErr == nil && sqErr == nil {
					if squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
						ID:          squadUUID,
						WorkspaceID: wsUUID,
					}); err == nil && uuidToString(squad.LeaderID) == resp.Agent.ID {
						briefing := buildSquadLeaderBriefing(r.Context(), h.Queries, squad)
						if strings.TrimSpace(resp.Agent.Instructions) == "" {
							resp.Agent.Instructions = briefing
						} else {
							resp.Agent.Instructions = resp.Agent.Instructions + "\n\n" + briefing
						}
						// Surface the squad identity to the daemon so the
						// quick-create prompt defaults the new issue's
						// assignee to the squad, not the leader agent.
						resp.SquadID = uuidToString(squad.ID)
						resp.SquadName = squad.Name
						slog.Debug("injected squad leader briefing for quick-create",
							"squad_id", uuidToString(squad.ID),
							"squad_name", squad.Name,
							"leader_agent_id", resp.Agent.ID,
						)
					}
				}
			}
		}
	}

	// Workspace isolation check: the daemon uses this response's workspace_id
	// as the only authority for MULTICA_WORKSPACE_ID in the agent env. An
	// empty value would make the CLI silently fall back to the user-global
	// config and talk to whatever workspace the user happened to last
	// configure; a value that doesn't match the runtime's workspace means
	// upstream routed a foreign-workspace task here. Both cases must hard-
	// fail AND cancel the just-dispatched task so the queue / agent status
	// don't sit stuck until the stale-task sweeper fires minutes later.
	if resp.WorkspaceID == "" || resp.WorkspaceID != runtimeWorkspaceID {
		outcome = "error_workspace"
		slog.Error("task claim: workspace isolation check failed, cancelling task",
			"task_id", uuidToString(task.ID),
			"runtime_id", runtimeID,
			"runtime_workspace", runtimeWorkspaceID,
			"resolved_workspace", resp.WorkspaceID,
			"has_issue", task.IssueID.Valid,
			"has_chat", task.ChatSessionID.Valid,
			"has_autopilot_run", task.AutopilotRunID.Valid,
			"has_quick_create", hasQuickCreate,
		)
		if _, cerr := h.TaskService.CancelTask(r.Context(), task.ID); cerr != nil {
			slog.Error("task claim: cancel after workspace check failed",
				"task_id", uuidToString(task.ID), "error", cerr)
		}
		writeError(w, http.StatusInternalServerError, "task workspace isolation check failed")
		return
	}

	// Workspace-level Context (workspace.context DB column) — the per-workspace
	// system prompt that workspace owners set in Settings → General. Inject it
	// into the brief regardless of task kind (issue / chat / autopilot /
	// quick-create) so every agent running in the workspace sees the same
	// shared context. Empty string when the owner hasn't set one; the daemon
	// skips rendering the heading in that case.
	if ws, err := h.Queries.GetWorkspace(r.Context(), parseUUID(resp.WorkspaceID)); err == nil {
		if ws.Context.Valid {
			resp.WorkspaceContext = ws.Context.String
		}
	} else {
		slog.Warn("task claim: failed to load workspace for context injection",
			"task_id", uuidToString(task.ID),
			"workspace_id", resp.WorkspaceID,
			"error", err,
		)
	}

	// Mint a task-scoped `mat_` token bound to (agent, task, workspace,
	// owner). The daemon will inject this as MULTICA_TOKEN into the agent
	// process instead of its own credential, so any API call the agent
	// makes — even one that strips X-Agent-ID / X-Task-ID headers — is
	// recognized server-side as actor=agent, closing the lateral-movement
	// path on owner-only endpoints (e.g. `/api/agents/{id}/env`). Runtime
	// owner is required because task tokens are still bound to an owning user;
	// without one, fail the claim explicitly instead of letting the daemon
	// fall back to a member/owner credential. MUL-3292.
	// Token expires after the queue/runtime upper bound (24h) so it survives
	// long-running tasks but cannot outlive a forgotten one.
	if !runtime.OwnerID.Valid {
		outcome = "error_token"
		slog.Error("task claim: runtime owner missing; cancelling task to avoid unscoped agent credentials",
			"task_id", uuidToString(task.ID),
			"runtime_id", runtimeID,
			"workspace_id", runtimeWorkspaceID,
		)
		if _, cerr := h.TaskService.CancelTask(r.Context(), task.ID); cerr != nil {
			slog.Error("task claim: cancel after missing runtime owner failed",
				"task_id", uuidToString(task.ID), "error", cerr)
		}
		writeError(w, http.StatusInternalServerError, "runtime owner required to mint task token")
		return
	}
	tokenStr, terr := auth.GenerateAgentTaskToken()
	if terr != nil {
		outcome = "error_token"
		slog.Error("task claim: failed to generate agent task token",
			"task_id", uuidToString(task.ID), "error", terr)
		writeError(w, http.StatusInternalServerError, "failed to mint task token")
		return
	}
	if _, terr := h.Queries.CreateTaskToken(r.Context(), db.CreateTaskTokenParams{
		TokenHash:   auth.HashToken(tokenStr),
		TaskID:      task.ID,
		AgentID:     task.AgentID,
		WorkspaceID: parseUUID(resp.WorkspaceID),
		UserID:      runtime.OwnerID,
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	}); terr != nil {
		outcome = "error_token"
		slog.Error("task claim: failed to persist agent task token",
			"task_id", uuidToString(task.ID), "error", terr)
		writeError(w, http.StatusInternalServerError, "failed to persist task token")
		return
	}
	resp.AuthToken = tokenStr

	slog.Info("task claimed by runtime", "task_id", uuidToString(task.ID), "runtime_id", runtimeID, "agent_id", uuidToString(task.AgentID), "prior_session", resp.PriorSessionID)
	writeJSON(w, http.StatusOK, map[string]any{"task": resp})
}

func canonicalGongfengCloneURL(rawURL, projectPath string) string {
	projectPath = strings.Trim(strings.TrimSpace(projectPath), "/")
	if projectPath == "" {
		parsed, err := parseGongfengURL(rawURL)
		if err == nil {
			projectPath = parsed.ProjectPath
		}
	}
	if projectPath == "" {
		return ""
	}
	return "https://git.code.tencent.com/" + strings.TrimSuffix(projectPath, ".git") + ".git"
}

// trailingUserMessages returns the run of user messages after the last
// assistant message in a chronologically-ordered chat history — the set the
// agent has NOT yet replied to. The agent resumes its prior session and only
// learns of new input through the claim response's chat_message, so a single
// run that covers a debounced burst (MUL-2968) must deliver every one of
// these, not just the latest. Every completed or failed run writes an
// assistant row, so the anchor advances one turn at a time; the result is the
// whole slice on the first turn and exactly the new message(s) thereafter.
func trailingUserMessages(msgs []db.ChatMessage) []db.ChatMessage {
	start := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			start = i + 1
			break
		}
	}
	return msgs[start:]
}

// ListPendingTasksByRuntime returns queued/dispatched tasks for a runtime.
func (h *Handler) ListPendingTasksByRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")

	// Verify the caller owns this runtime's workspace.
	runtime, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	workspaceID := uuidToString(runtime.WorkspaceID)

	tasks, err := h.Queries.ListPendingTasksByRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pending tasks")
		return
	}

	resp := make([]AgentTaskResponse, len(tasks))
	for i, t := range tasks {
		resp[i] = taskToResponse(t, workspaceID)
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Task Lifecycle (called by daemon)
// ---------------------------------------------------------------------------

// StartTask marks a dispatched task as running.
func (h *Handler) StartTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	// Verify the caller owns this task's workspace.
	_, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}

	task, err := h.TaskService.StartTask(r.Context(), parseUUID(taskID))
	if err != nil {
		if errors.Is(err, service.ErrTaskStartConflict) {
			slog.Info("start task skipped because task is no longer startable", "task_id", taskID, "error", err)
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		slog.Warn("start task failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("task started", "task_id", taskID, "agent_id", uuidToString(task.AgentID))
	writeJSON(w, http.StatusOK, taskToResponse(*task, workspaceID))
}

// TaskWaitLocalDirectoryRequest is the legacy compatibility body the daemon
// POSTs when it parks a task on a busy local_directory path.
type TaskWaitLocalDirectoryRequest struct {
	// Reason is a short hint surfaced by the UI alongside the status —
	// typically "<path>" or "<path> (holder: <task short id>)". Small
	// enough to fit on the issue card. Empty is accepted; the column is
	// nullable on the server.
	Reason string `json:"reason"`
}

// MarkTaskWaitingLocalDirectory transitions a dispatched task to
// waiting_local_directory for legacy local_directory compatibility paths.
// Standard issue tasks use issue-scoped managed worktrees instead.
func (h *Handler) MarkTaskWaitingLocalDirectory(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	_, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}

	var req TaskWaitLocalDirectoryRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	task, err := h.TaskService.MarkTaskWaitingLocalDirectory(r.Context(), parseUUID(taskID), req.Reason)
	if err != nil {
		slog.Warn("mark task waiting_local_directory failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, taskToResponse(*task, workspaceID))
}

// ReportTaskProgress broadcasts a progress update.
type TaskProgressRequest struct {
	Summary string `json:"summary"`
	Step    int    `json:"step"`
	Total   int    `json:"total"`
}

func (h *Handler) ReportTaskProgress(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	var req TaskProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Verify ownership and resolve workspace ID.
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}

	workspaceID := ""
	if task.IssueID.Valid {
		if issue, err := h.Queries.GetIssue(r.Context(), task.IssueID); err == nil {
			workspaceID = uuidToString(issue.WorkspaceID)
		}
	}

	h.TaskService.ReportProgress(r.Context(), taskID, workspaceID, req.Summary, req.Step, req.Total)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CompleteTask marks a running task as completed.
type TaskCompleteRequest struct {
	PRURL     string `json:"pr_url"`
	Output    string `json:"output"`
	SessionID string `json:"session_id"` // Claude session ID for future resumption
	WorkDir   string `json:"work_dir"`   // working directory used during execution
}

