// 本文件职责：「创建项目」弹窗——填写项目名称、描述、图标、状态/优先级/负责人/起止日期，
// 并可附带关联 GitHub 仓库，最后调用创建接口并跳转到新项目详情页。
"use client";

import { useState, useRef } from "react";
import { CalendarClock, CalendarDays, ChevronRight, Maximize2, Minimize2, MoreHorizontal, Search, X as XIcon, UserMinus } from "lucide-react";

/**
 * GitHub 图标（纯 SVG）。
 *
 * lucide-react v1 移除了品牌类图标，所以这里把 Octicon 风格的 GitHub 标记直接内联：
 * 24×24 viewBox、用 currentColor 填充，从而继承父级的文字颜色。目前只有这一个调用方，
 * 所以先留在本文件；如果以后出现第二个使用处，再提升到 packages/ui。
 */
function GithubIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="currentColor"
      aria-hidden="true"
      className={className}
    >
      <path d="M12 .5C5.73.5.66 5.57.66 11.84c0 5.01 3.25 9.26 7.76 10.76.57.1.78-.25.78-.55 0-.27-.01-1.17-.02-2.13-3.16.69-3.83-1.34-3.83-1.34-.52-1.31-1.27-1.66-1.27-1.66-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.02 1.75 2.68 1.24 3.34.95.1-.74.4-1.24.72-1.53-2.52-.29-5.18-1.26-5.18-5.62 0-1.24.45-2.26 1.18-3.06-.12-.29-.51-1.45.11-3.02 0 0 .96-.31 3.15 1.17a10.93 10.93 0 0 1 5.74 0c2.19-1.48 3.15-1.17 3.15-1.17.62 1.57.23 2.73.11 3.02.74.8 1.18 1.82 1.18 3.06 0 4.37-2.67 5.32-5.21 5.61.41.35.78 1.04.78 2.1 0 1.52-.01 2.74-.01 3.11 0 .3.21.66.79.55 4.51-1.5 7.76-5.75 7.76-10.76C23.34 5.57 18.27.5 12 .5Z" />
    </svg>
  );
}
import { useQuery } from "@tanstack/react-query";
import { useCreateProject } from "@multica/core/projects/mutations";
import { useProjectDraftStore } from "@multica/core/projects";
import {
  PROJECT_STATUS_CONFIG,
  PROJECT_STATUS_ORDER,
  PROJECT_PRIORITY_ORDER,
} from "@multica/core/projects/config";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { memberListOptions, agentListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import type { ProjectStatus, ProjectPriority } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { toast } from "sonner";
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Popover, PopoverTrigger, PopoverContent } from "@multica/ui/components/ui/popover";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import { Button } from "@multica/ui/components/ui/button";
import { EmojiPicker } from "@multica/ui/components/common/emoji-picker";
import { ContentEditor, type ContentEditorRef, TitleEditor } from "../editor";
import { PriorityIcon } from "../issues/components/priority-icon";
import { ActorAvatar } from "../common/actor-avatar";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";
import { matchesPinyin } from "../editor/extensions/pinyin-match";
import {
  useProjectStatusLabels,
  useProjectPriorityLabels,
} from "../projects/components/labels";
import { ProjectStartDatePicker } from "../projects/components/project-start-date-picker";
import { ProjectDueDatePicker } from "../projects/components/project-due-date-picker";
import { PillButton } from "../common/pill-button";
import { githubShortLabel } from "../common/github-url";
/**
 * 仓库地址展示：一行内只显示截短的仓库标识（如 owner/repo），完整 URL 通过悬浮
 * tooltip 展示。URL 可能很长，所以 tooltip 允许换行并整体可复制。
 */
function RepoUrlText({
  url,
  className,
}: {
  url: string;
  className?: string;
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span className={cn("truncate flex-1 text-left", className)}>
            {githubShortLabel(url)}
          </span>
        }
      />
      <TooltipContent side="top" align="start" className="max-w-sm break-all">
        {url}
      </TooltipContent>
    </Tooltip>
  );
}

/**
 * 「创建项目」弹窗主组件。
 *
 * 负责整个表单：标题、描述、图标 emoji、负责人（成员或 agent）、状态/优先级、
 * 起止日期，以及可选关联的 GitHub 仓库（来自工作区配置，也支持手动粘贴 URL）。
 * 字段改动实时同步到草稿 store，创建成功后清空草稿并跳转项目详情页。
 *
 * @param onClose 关闭弹窗的回调
 */
export function CreateProjectModal({ onClose }: { onClose: () => void }) {
  const { t } = useT("modals");
  const router = useNavigation();
  const workspace = useCurrentWorkspace();
  const workspaceName = workspace?.name;
  const wsPaths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { getActorName } = useActorName();
  const projectStatusLabels = useProjectStatusLabels();
  const projectPriorityLabels = useProjectPriorityLabels();

  const draft = useProjectDraftStore((s) => s.draft);
  const setDraft = useProjectDraftStore((s) => s.setDraft);
  const clearDraft = useProjectDraftStore((s) => s.clearDraft);

  const [title, setTitle] = useState(draft.title);
  const descEditorRef = useRef<ContentEditorRef>(null);
  const [status, setStatus] = useState<ProjectStatus>(draft.status);
  const [priority, setPriority] = useState<ProjectPriority>(draft.priority);
  const [leadType, setLeadType] = useState<"member" | "agent" | undefined>(draft.leadType);
  const [leadId, setLeadId] = useState<string | undefined>(draft.leadId);
  const [icon, setIcon] = useState<string | undefined>(draft.icon);
  const [startDate, setStartDate] = useState<string>(draft.startDate ?? "");
  const [dueDate, setDueDate] = useState<string>(draft.dueDate ?? "");
  // 日期字段默认折叠进 ⋯ 溢出菜单（渐进式暴露，与 create-issue 一致）；
  // 以下开关负责把对应的内联胶囊和日历同时唤起。
  const [startDatePickerOpen, setStartDatePickerOpen] = useState(false);
  const [dueDatePickerOpen, setDueDatePickerOpen] = useState(false);
  const [iconPickerOpen, setIconPickerOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [isExpanded, setIsExpanded] = useState(false);
  // 选中的仓库只存 URL 字符串，而不是完整的 ProjectResource 记录——它们要到
  // handleSubmit 里真正发起 createProjectResource 调用时才会被持久化。
  const [selectedRepos, setSelectedRepos] = useState<string[]>([]);
  const [repoPopoverOpen, setRepoPopoverOpen] = useState(false);
  const [repoSearch, setRepoSearch] = useState("");
  const [customRepoUrl, setCustomRepoUrl] = useState("");
  const workspaceRepos = workspace?.repos ?? [];
  const repoQuery = repoSearch.trim().toLowerCase();
  const filteredWorkspaceRepos = workspaceRepos.filter((repo) =>
    repo.url.toLowerCase().includes(repoQuery),
  );

  // 把字段改动同步到草稿 store，弹窗中途关闭再打开能原样恢复。
  const updateTitle = (v: string) => { setTitle(v); setDraft({ title: v }); };
  const updateStatus = (v: ProjectStatus) => { setStatus(v); setDraft({ status: v }); };
  const updatePriority = (v: ProjectPriority) => { setPriority(v); setDraft({ priority: v }); };
  const updateLead = (type?: "member" | "agent", id?: string) => {
    setLeadType(type); setLeadId(id);
    setDraft({ leadType: type, leadId: id });
  };
  const updateIcon = (v: string | undefined) => { setIcon(v); setDraft({ icon: v }); };
  const updateStartDate = (v: string) => { setStartDate(v); setDraft({ startDate: v || undefined }); };
  const updateDueDate = (v: string) => { setDueDate(v); setDraft({ dueDate: v || undefined }); };

  const [leadOpen, setLeadOpen] = useState(false);
  const [leadFilter, setLeadFilter] = useState("");

  const leadQuery = leadFilter.toLowerCase();
  const filteredMembers = members.filter((m) => m.name.toLowerCase().includes(leadQuery) || matchesPinyin(m.name, leadQuery));
  const filteredAgents = agents.filter(
    (a) => !a.archived_at && (a.name.toLowerCase().includes(leadQuery) || matchesPinyin(a.name, leadQuery)),
  );

  const leadLabel =
    leadType && leadId ? getActorName(leadType, leadId) : t(($) => $.create_project.lead);

  const createProject = useCreateProject();

  const handleSubmit = async () => {
    if (!title.trim() || submitting) return;
    const resources = selectedRepos.length > 0
      ? selectedRepos.map((url) => ({
          resource_type: "github_repo" as const,
          resource_ref: { url },
        }))
      : undefined;
    setSubmitting(true);
    try {
      const project = await createProject.mutateAsync({
        title: title.trim(),
        description: descEditorRef.current?.getMarkdown()?.trim() || undefined,
        icon,
        status,
        priority,
        lead_type: leadType,
        lead_id: leadId,
        start_date: startDate || undefined,
        due_date: dueDate || undefined,
        // 服务端会在与创建项目同一事务里把这些资源一并挂上。
        resources,
      });
      clearDraft();
      onClose();
      toast.success(t(($) => $.create_project.toast_created));
      router.push(wsPaths.projectDetail(project.id));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.create_project.toast_failed),
      );
    } finally {
      setSubmitting(false);
    }
  };

  const toggleRepo = (url: string) => {
    setSelectedRepos((prev) =>
      prev.includes(url) ? prev.filter((u) => u !== url) : [...prev, url],
    );
  };

  const addCustomRepo = () => {
    const url = customRepoUrl.trim();
    if (!url) return;
    setSelectedRepos((prev) => (prev.includes(url) ? prev : [...prev, url]));
    setCustomRepoUrl("");
  };

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent
        showCloseButton={false}
        className={cn(
          "p-0 gap-0 flex flex-col overflow-hidden",
          "!top-1/2 !left-1/2 !-translate-x-1/2",
          "!transition-all !duration-300 !ease-out",
          isExpanded
            ? "!max-w-4xl !w-full !h-5/6 !-translate-y-1/2"
            : "!max-w-2xl !w-full !h-96 !-translate-y-1/2",
        )}
      >
        <DialogTitle className="sr-only">{t(($) => $.create_project.title)}</DialogTitle>

        <div className="flex items-center justify-between px-5 pt-3 pb-2 shrink-0">
          <div className="flex items-center gap-1.5 text-caption">
            <span className="text-muted-foreground">{workspaceName}</span>
            <ChevronRight className="size-3 text-faint-foreground" />
            <span className="font-medium">{t(($) => $.create_project.title_breadcrumb)}</span>
          </div>
          <div className="flex items-center gap-1">
            <Tooltip>
              <TooltipTrigger
                render={
                  <button
                    type="button"
                    onClick={() => setIsExpanded(!isExpanded)}
                    className="rounded-sm p-1.5 opacity-70 hover:opacity-100 hover:bg-accent/60 transition-all cursor-pointer"
                  >
                    {isExpanded ? <Minimize2 className="size-4" /> : <Maximize2 className="size-4" />}
                  </button>
                }
              />
              <TooltipContent side="bottom">
                {isExpanded
                  ? t(($) => $.common.collapse_tooltip)
                  : t(($) => $.common.expand_tooltip)}
              </TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger
                render={
                  <button
                    type="button"
                    onClick={onClose}
                    className="rounded-sm p-1.5 opacity-70 hover:opacity-100 hover:bg-accent/60 transition-all cursor-pointer"
                  >
                    <XIcon className="size-4" />
                  </button>
                }
              />
              <TooltipContent side="bottom">{t(($) => $.common.close)}</TooltipContent>
            </Tooltip>
          </div>
        </div>

        <div className="px-5 pb-2 shrink-0">
          <Popover open={iconPickerOpen} onOpenChange={setIconPickerOpen}>
            <PopoverTrigger
              render={
                <button
                  type="button"
                  className="text-display-sm cursor-pointer rounded-lg p-1 -ml-1 hover:bg-accent/60 transition-colors"
                  title={t(($) => $.create_project.icon_tooltip)}
                >
                  {icon || "📁"}
                </button>
              }
            />
            <PopoverContent align="start" className="w-auto p-0">
              <EmojiPicker
                onSelect={(emoji) => {
                  updateIcon(emoji);
                  setIconPickerOpen(false);
                }}
              />
            </PopoverContent>
          </Popover>
          <TitleEditor
            autoFocus
            defaultValue={draft.title}
            placeholder={t(($) => $.create_project.title_placeholder)}
            className="text-title font-semibold"
            onChange={(v) => updateTitle(v)}
            onSubmit={handleSubmit}
          />
        </div>

        <div className="flex-1 min-h-0 overflow-y-auto px-5">
          <ContentEditor
            ref={descEditorRef}
            defaultValue={draft.description}
            placeholder={t(($) => $.create_project.description_placeholder)}
            onUpdate={(md) => setDraft({ description: md })}
            debounceMs={500}
          />
          <p className="mt-1 text-caption text-muted-foreground">
            {t(($) => $.create_project.description_hint)}
          </p>
        </div>

        {/* 属性工具栏——布局与 create-issue 的 footer 对齐：一行可换行的胶囊，
            低频字段（起止日期）默认折叠进 ⋯ 溢出菜单，主操作放在下方独立一栏。
            仓库选择暂时和属性胶囊放在同一行；等以后支持更多资源类型
            （Linear / Notion / Figma / Slack）时，再抽成 footer 上方独立的
            Resources 栏。 */}
        <div className="flex items-center gap-1.5 px-4 py-2 shrink-0 flex-wrap">
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <PillButton>
                  <span className={cn("size-2 rounded-full", PROJECT_STATUS_CONFIG[status].dotColor)} />
                  <span>{projectStatusLabels[status]}</span>
                </PillButton>
              }
            />
            <DropdownMenuContent align="start" className="w-44">
              {PROJECT_STATUS_ORDER.map((s) => (
                <DropdownMenuItem key={s} onClick={() => updateStatus(s)}>
                  <span className={cn("size-2 rounded-full", PROJECT_STATUS_CONFIG[s].dotColor)} />
                  <span>{projectStatusLabels[s]}</span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>

          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <PillButton>
                  <PriorityIcon priority={priority} />
                  <span>{projectPriorityLabels[priority]}</span>
                </PillButton>
              }
            />
            <DropdownMenuContent align="start" className="w-44">
              {PROJECT_PRIORITY_ORDER.map((pr) => (
                <DropdownMenuItem key={pr} onClick={() => updatePriority(pr)}>
                  <PriorityIcon priority={pr} />
                  <span>{projectPriorityLabels[pr]}</span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>

          <Popover
            open={leadOpen}
            onOpenChange={(v) => {
              setLeadOpen(v);
              if (!v) setLeadFilter("");
            }}
          >
            <PopoverTrigger
              render={
                <PillButton>
                  {leadType && leadId ? (
                    <>
                      <ActorAvatar actorType={leadType} actorId={leadId} size="sm" showStatusDot />
                      <span className="truncate">{leadLabel}</span>
                    </>
                  ) : (
                    <span className="text-muted-foreground">{t(($) => $.create_project.lead)}</span>
                  )}
                </PillButton>
              }
            />
            <PopoverContent align="start" className="w-52 p-0">
              <div className="px-2 py-1.5 border-b">
                <input
                  type="text"
                  value={leadFilter}
                  onChange={(e) => setLeadFilter(e.target.value)}
                  placeholder={t(($) => $.create_project.lead_placeholder)}
                  className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
                />
              </div>
              <div className="p-1 max-h-60 overflow-y-auto">
                <button
                  type="button"
                  onClick={() => {
                    updateLead(undefined, undefined);
                    setLeadOpen(false);
                  }}
                  className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-body hover:bg-accent transition-colors"
                >
                  <UserMinus className="h-3.5 w-3.5 text-muted-foreground" />
                  <span className="text-muted-foreground">{t(($) => $.create_project.no_lead)}</span>
                </button>
                {filteredMembers.length > 0 && (
                  <>
                    <div className="px-2 pt-2 pb-1 text-caption font-medium text-muted-foreground uppercase tracking-wider">
                      {t(($) => $.create_project.members_group)}
                    </div>
                    {filteredMembers.map((m) => (
                      <button
                        type="button"
                        key={m.user_id}
                        onClick={() => {
                          updateLead("member", m.user_id);
                          setLeadOpen(false);
                        }}
                        className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-body hover:bg-accent transition-colors"
                      >
                        <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
                        <span>{m.name}</span>
                      </button>
                    ))}
                  </>
                )}
                {filteredAgents.length > 0 && (
                  <>
                    <div className="px-2 pt-2 pb-1 text-caption font-medium text-muted-foreground uppercase tracking-wider">
                      {t(($) => $.create_project.agents_group)}
                    </div>
                    {filteredAgents.map((a) => (
                      <button
                        type="button"
                        key={a.id}
                        onClick={() => {
                          updateLead("agent", a.id);
                          setLeadOpen(false);
                        }}
                        className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-body hover:bg-accent transition-colors"
                      >
                        <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                        <span>{a.name}</span>
                      </button>
                    ))}
                  </>
                )}
                {filteredMembers.length === 0 &&
                  filteredAgents.length === 0 &&
                  leadFilter && (
                    <div className="px-2 py-3 text-center text-body text-muted-foreground">
                      {t(($) => $.create_project.no_results)}
                    </div>
                  )}
              </div>
            </PopoverContent>
          </Popover>

          {/* 开始日期——默认折叠进 ⋯，除非已填值、或用户刚从溢出菜单打开
              （此时日期选择器的日历需要以内联胶囊为锚点）。 */}
          {(startDate || startDatePickerOpen) && (
            <ProjectStartDatePicker
              startDate={startDate || null}
              onUpdate={(u) => updateStartDate(u.start_date ?? "")}
              triggerRender={<PillButton />}
              open={startDatePickerOpen}
              onOpenChange={setStartDatePickerOpen}
            />
          )}

          {(dueDate || dueDatePickerOpen) && (
            <ProjectDueDatePicker
              dueDate={dueDate || null}
              onUpdate={(u) => updateDueDate(u.due_date ?? "")}
              triggerRender={<PillButton />}
              open={dueDatePickerOpen}
              onOpenChange={setDueDatePickerOpen}
            />
          )}

          <Popover
            open={repoPopoverOpen}
            onOpenChange={(v) => {
              setRepoPopoverOpen(v);
              if (!v) setRepoSearch("");
            }}
          >
            <PopoverTrigger
              render={
                <PillButton>
                  <GithubIcon className="size-3" />
                  <span>
                    {selectedRepos.length === 0
                      ? t(($) => $.create_project.repos_pill)
                      : t(($) => $.create_project.repos_pill_count, { count: selectedRepos.length })}
                  </span>
                </PillButton>
              }
            />
            <PopoverContent side="top" align="start" className="w-72 p-2 space-y-2">
                  <div className="text-caption font-medium text-muted-foreground">
                    {t(($) => $.create_project.repos_heading)}
                  </div>
                  {workspaceRepos.length > 0 ? (
                    <>
                      <div className="relative">
                        <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                        <input
                          type="text"
                          value={repoSearch}
                          onChange={(e) => setRepoSearch(e.target.value)}
                          aria-label={t(($) => $.create_project.repos_search_placeholder)}
                          placeholder={t(($) => $.create_project.repos_search_placeholder)}
                          className="h-8 w-full rounded-md border bg-transparent pl-7 pr-2 text-caption outline-none placeholder:text-muted-foreground focus-visible:ring-1 focus-visible:ring-ring"
                        />
                      </div>
                      <div className="max-h-48 space-y-1 overflow-y-auto">
                        {filteredWorkspaceRepos.length === 0 && repoQuery && (
                          <p className="py-2 text-center text-caption text-muted-foreground">
                            {t(($) => $.create_project.repos_search_empty)}
                          </p>
                        )}
                        {filteredWorkspaceRepos.map((repo) => {
                          const checked = selectedRepos.includes(repo.url);
                          return (
                            <button
                              type="button"
                              key={repo.url}
                              onClick={() => toggleRepo(repo.url)}
                              className={cn(
                                "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-caption hover:bg-accent transition-colors",
                                checked && "bg-accent",
                              )}
                            >
                              <input
                                type="checkbox"
                                checked={checked}
                                readOnly
                                className="size-3.5"
                              />
                              <GithubIcon className="size-3.5" />
                              <RepoUrlText url={repo.url} />
                            </button>
                          );
                        })}
                      </div>
                    </>
                  ) : (
                    <p className="text-caption text-muted-foreground">
                      {t(($) => $.create_project.repos_empty)}
                    </p>
                  )}
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      addCustomRepo();
                    }}
                    className="flex items-center gap-1.5 pt-1 border-t"
                  >
                    <input
                      type="text"
                      value={customRepoUrl}
                      onChange={(e) => setCustomRepoUrl(e.target.value)}
                      placeholder={t(($) => $.create_project.repos_url_placeholder)}
                      className="flex-1 bg-transparent text-caption px-2 py-1 outline-none placeholder:text-muted-foreground"
                    />
                    <Button
                      type="submit"
                      size="sm"
                      variant="ghost"
                      className="h-6 px-2 text-caption"
                      disabled={!customRepoUrl.trim()}
                    >
                      {t(($) => $.create_project.repos_add)}
                    </Button>
                  </form>
                  {selectedRepos.length > 0 && (
                    <div className="space-y-1 pt-1 border-t">
                      <div className="text-micro font-medium text-muted-foreground uppercase tracking-wider">
                        {t(($) => $.create_project.repos_selected)}
                      </div>
                      {selectedRepos.map((url) => (
                        <div
                          key={url}
                          className="flex items-center gap-2 text-caption"
                        >
                          <GithubIcon className="size-3 text-muted-foreground" />
                          <RepoUrlText url={url} />
                          <button
                            type="button"
                            onClick={() => toggleRepo(url)}
                            className="text-muted-foreground hover:text-foreground"
                          >
                            <XIcon className="size-3" />
                          </button>
                        </div>
                      ))}
                    </div>
                  )}
            </PopoverContent>
          </Popover>

          {/* 溢出菜单——永远是最后一个子元素，这样在换行流里始终排到最后。
              只有某个日期仍可折叠时才渲染；两个日期都已设上时没有可加的项。 */}
          {(!startDate || !dueDate) && (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <PillButton aria-label={t(($) => $.create_project.more_options_aria)}>
                    <MoreHorizontal className="size-3.5" />
                  </PillButton>
                }
              />
              <DropdownMenuContent align="start" className="w-auto">
                {!dueDate && (
                  <DropdownMenuItem onClick={() => setDueDatePickerOpen(true)}>
                    <CalendarDays className="h-3.5 w-3.5" />
                    {t(($) => $.create_project.set_due_date)}
                  </DropdownMenuItem>
                )}
                {!startDate && (
                  <DropdownMenuItem onClick={() => setStartDatePickerOpen(true)}>
                    <CalendarClock className="h-3.5 w-3.5" />
                    {t(($) => $.create_project.set_start_date)}
                  </DropdownMenuItem>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>

        {/* Footer 操作栏——主按钮放在独立一栏，与 create-issue 保持一致。 */}
        <div className="flex items-center justify-end border-t px-4 py-3 shrink-0">
          <Button
            size="sm"
            onClick={handleSubmit}
            disabled={!title.trim() || submitting}
            className="shrink-0"
          >
            {submitting ? t(($) => $.create_project.submitting) : t(($) => $.create_project.submit)}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
