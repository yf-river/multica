// 本文件职责：技能（Skill）详情页。顶部是面包屑 + 身份条 + 页头操作（只读提示、
// 从来源刷新 / 添加到 agent / 删除）；正文分两个标签页：
// - Overview：编辑名称/描述/标签，列出正在使用它的 agent；
// - Files：文件树 + 正文区，可预览/编辑 SKILL.md 与附加文件，支持新增/重命名/删除。
// 全页围绕「本地草稿 vs 服务端版本」运转：保存栏由脏标记门控，远端更新只在与草稿
// 冲突时才弹横幅；保存 / 放弃 / 刷新都把结果收敛到 adoptServerVersion 一个入口。
"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  AlertTriangle,
  Clock3,
  Download,
  FileText,
  HardDrive,
  Loader2,
  Lock,
  Pencil,
  Plus,
  RotateCw,
  Save,
  Trash2,
  UserPlus,
  Users,
} from "lucide-react";
import { SkillIcon } from "../lib/skill-icon";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
  Skill,
  SkillFile,
  UpdateSkillRequest,
} from "@multica/core/types";
import { toast } from "sonner";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useTimeAgo } from "../../i18n";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentListOptions,
  memberListOptions,
  selectSkillAssignments,
  skillDetailOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import {
  runtimeDisplayLabel,
  runtimeListOptions,
} from "@multica/core/runtimes";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { AppLink, useNavigation } from "../../navigation";
import { BreadcrumbHeader } from "../../layout/breadcrumb-header";
import { useCanEditSkill } from "../hooks/use-can-edit-skill";
import { useSkillPermissions } from "@multica/core/permissions";
import { CapabilityBanner } from "@multica/ui/components/common/capability-banner";
import {
  isRefreshableOrigin,
  originSourceUrl,
  readOrigin,
  totalFileCount,
  type OriginInfo,
} from "../lib/origin";
import { FileTree } from "./file-tree";
import { FileViewer, isMarkdownPath, type FileMode } from "./file-viewer";
import {
  AddToAgentDialog,
  type SkillActionsContext,
} from "./skill-list-actions";
import { RefreshSkillDialog } from "./refresh-skill-dialog";
import { useT } from "../../i18n";
import { ResourceLabelPicker } from "../../labels/resource-label-picker";

const SKILL_MD = "SKILL.md";

type DraftFile = { id?: string; path: string; content: string };

/** 可编辑草稿：四个可编辑字段合成的一张快照。 */
type SkillDraft = {
  name: string;
  description: string;
  content: string;
  files: DraftFile[];
};

/**
 * 把服务端技能转成可编辑草稿。
 *
 * `name`/`description` 在这里就 trim 掉，是因为保存时也会 trim：在「服务端数据
 * 变成草稿」这唯一交接点归一化一次，之后所有比较就能直接用等号。当初两边漂移的
 * 根源正是在比较时才 trim——脏检查只 trim 了一侧操作数：以换行结尾的描述
 * （frontmatter 里 `description: |` 会生成）永远不等于它自己，页面一打开就是
 * 永久脏状态。
 *
 * `content` 与各文件正文刻意不做归一化：SKILL.md 正文首尾的空白是内容本身，
 * 不是排版噪声。
 */
function toDraft(s: Skill): SkillDraft {
  return {
    name: s.name.trim(),
    description: s.description.trim(),
    content: s.content,
    files: (s.files ?? []).map((f: SkillFile) => ({
      id: f.id,
      path: f.path,
      content: f.content,
    })),
  };
}

/**
 * 一组文件的「与顺序无关」签名。两个返回文件的接口顺序不一致——GET 按路径排序、
 * PUT 按请求顺序回显——而顺序不同并不等于内容不同。
 */
function fileSignature(files: DraftFile[]): string {
  return JSON.stringify(
    files
      .map((f) => ({ path: f.path, content: f.content }))
      .sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0)),
  );
}

/**
 * 用户是否真的改了什么——以「上次交给用户的草稿」为基准来量。
 *
 * 刻意不问「草稿与最新服务端技能是否不同」：那个问题混了两个互不相干的原因——
 * 用户动了手，或服务端在我们背后变了——只按前者作答，正是当初把每个远端更新都
 * 误读成本地编辑的原因。
 */
function hasLocalEdits(draft: SkillDraft, baseline: SkillDraft | null): boolean {
  if (!baseline) return false;
  return (
    draft.name.trim() !== baseline.name ||
    draft.description.trim() !== baseline.description ||
    draft.content !== baseline.content ||
    fileSignature(draft.files) !== fileSignature(baseline.files)
  );
}

/**
 * 两个标签页，不是三个。skill 没有值得单开的「设置」维度：更新请求只携带
 * name/description/content/config/files，编辑权是派生出来的（创建者或工作区
 * admin），没有对应的可写字段——真设一个 Settings 标签页，也只能放一个删除按钮
 * 加一句只读说明。删除因此放在页头，与 agent 详情页把 Archive 放在页头一致。
 */
type DetailView = "overview" | "files";

function isDetailView(value: string | null): value is DetailView {
  return value === "overview" || value === "files";
}

// ---------------------------------------------------------------------------
// 新文件路径校验 + 行内新增
// ---------------------------------------------------------------------------

/**
 * 返回一个校验器：判断「新文件路径」是否合法，不合法时给出本地化的中文错误消息，
 * 合法时返回空串。规则（空/绝对路径/.. /保留名/重名/与目录撞名/落在已有文件之下）
 * 与文件树、重命名共用，避免在页面各处维护多份「什么路径合法」的规则。
 */
export function useValidateNewFilePath() {
  const { t } = useT("skills");
  return (path: string, existing: string[]): string => {
    const p = path.trim();
    if (!p) return t(($) => $.detail.add_file.errors.empty);
    if (p.startsWith("/")) return t(($) => $.detail.add_file.errors.absolute);
    if (p.split("/").includes("..")) return t(($) => $.detail.add_file.errors.double_dot);
    if (p === SKILL_MD) return t(($) => $.detail.add_file.errors.reserved);
    if (existing.includes(p)) return t(($) => $.detail.add_file.errors.exists);
    // 目录是从路径「推断」出来的、并不单独存储：新文件若取了一个与已有推断目录
    // 同名的路径，建树时会被并进那个目录节点——它明明还躺在草稿里，却在侧栏
    // 消失了。所以两个方向都拒绝：文件不能占目录名，也不能取「某已有文件之下」
    // 的子路径（那等于把已有文件当文件夹）。
    if (existing.some((other) => other.startsWith(`${p}/`))) {
      return t(($) => $.detail.add_file.errors.is_directory);
    }
    if (existing.some((other) => p.startsWith(`${other}/`))) {
      return t(($) => $.detail.add_file.errors.under_file);
    }
    return "";
  };
}

function AddFileInline({
  existingPaths,
  onAdd,
  onCancel,
}: {
  existingPaths: string[];
  onAdd: (path: string) => void;
  onCancel: () => void;
}) {
  const { t } = useT("skills");
  const validate = useValidateNewFilePath();
  const [path, setPath] = useState("");
  const [error, setError] = useState("");

  const submit = () => {
    const err = validate(path, existingPaths);
    if (err) {
      setError(err);
      return;
    }
    onAdd(path.trim());
  };

  return (
    <div className="mt-1.5">
      <Input
        autoFocus
        value={path}
        onChange={(e) => {
          setPath(e.target.value);
          setError("");
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") submit();
          if (e.key === "Escape") onCancel();
        }}
        placeholder={t(($) => $.detail.add_file.placeholder)}
        className="h-7 font-mono text-caption"
      />
      {error && (
        <p role="alert" className="mt-1 text-caption text-destructive">
          {error}
        </p>
      )}
      <div className="mt-1.5 flex items-center gap-1.5">
        <Button type="button" size="xs" onClick={submit}>
          {t(($) => $.detail.add_file.add)}
        </Button>
        <Button type="button" size="xs" variant="ghost" onClick={onCancel}>
          {t(($) => $.detail.add_file.cancel)}
        </Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 页头（身份条）
// ---------------------------------------------------------------------------

function useOriginLabel(origin: OriginInfo | null, runtime: AgentRuntime | null) {
  const { t } = useT("skills");
  if (!origin) return null;
  if (origin.type === "runtime_local") {
    return runtime
      ? t(($) => $.detail.subline.origin_runtime_named, {
          name: runtimeDisplayLabel(runtime),
        })
      : origin.provider
        ? t(($) => $.detail.subline.origin_runtime_provider, { provider: origin.provider })
        : t(($) => $.detail.subline.origin_runtime_unknown);
  }
  if (origin.type === "clawhub") return t(($) => $.detail.subline.origin_clawhub);
  if (origin.type === "skills_sh") return t(($) => $.detail.subline.origin_skills_sh);
  if (origin.type === "github") return t(($) => $.detail.subline.origin_github);
  return t(($) => $.detail.subline.origin_workspace);
}

/**
 * 面包屑下方的一行身份条：图标、名称，外加几个说明「这技能由什么构成」的计数。
 * 保持一行：读者想滚动查看更多细节时，那些字段都在 Overview 标签页里。
 *
 * 描述（description）不再在这里复述。过去它展示在可编辑副本两屏之上的这里——对
 * 一页动作只有编辑/新增/删除的页面，每次访问都要为「下一个屏幕就能改」的字段
 * 付一次只读复述。用户从列表页进入，列表已经带描述，足以决定是否打开详情。
 *
 * agent 详情页页头仍是更高的那种形态；把它对齐是本分支不触碰的那页的一次独立改动。
 */
function SkillIdentity({
  skill,
  origin,
  originRuntime,
  agentCount,
  creator,
}: {
  skill: Skill;
  origin: OriginInfo | null;
  originRuntime: AgentRuntime | null;
  agentCount: number;
  creator: MemberWithUser | null;
}) {
  const { t } = useT("skills");
  const timeAgo = useTimeAgo();
  const originLabel = useOriginLabel(origin, originRuntime);
  const isRuntimeOrigin = origin?.type === "runtime_local";
  const sourceUrl = originSourceUrl(origin);

  return (
    <div className="shrink-0 border-b px-4 py-3 sm:px-6">
      <div className="mx-auto flex max-w-[1440px] flex-wrap items-center gap-x-4 gap-y-1.5">
        <div className="flex min-w-0 items-center gap-2.5">
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border bg-muted text-muted-foreground">
            <SkillIcon className="h-4 w-4" aria-hidden="true" />
          </div>
          <h1 className="min-w-0 truncate font-mono text-title font-semibold tracking-tight">

            {skill.name}
          </h1>
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 text-caption text-muted-foreground sm:ml-auto">
          {originLabel && (
            <span className="inline-flex min-w-0 items-center gap-1.5">
              {/* 与列表页 Source 列同样的三分法图标：本地 runtime / 手动创建 /
                  外部导入。 */}
              {isRuntimeOrigin ? (
                <HardDrive className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
              ) : origin?.type === "manual" ? (
                <Pencil className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
              ) : (
                <Download className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
              )}
              {sourceUrl ? (
                <Tooltip>
                  <TooltipTrigger
                    render={
                      <a
                        href={sourceUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="truncate hover:underline"
                      >
                        {originLabel}
                      </a>
                    }
                  />
                  <TooltipContent side="top">{sourceUrl}</TooltipContent>
                </Tooltip>
              ) : (
                <span className="truncate">{originLabel}</span>
              )}
            </span>
          )}
          <span className="inline-flex items-center gap-1.5">
            <FileText className="h-3.5 w-3.5" aria-hidden="true" />
            {t(($) => $.detail.header.files, { count: totalFileCount(skill) })}
          </span>
          <span className="inline-flex items-center gap-1.5">
            <Users className="h-3.5 w-3.5" aria-hidden="true" />
            {t(($) => $.detail.header.used_by, { count: agentCount })}
          </span>
          <span className="inline-flex items-center gap-1.5">
            <Clock3 className="h-3.5 w-3.5" aria-hidden="true" />
            {creator
              ? t(($) => $.detail.header.updated_by, {
                  when: timeAgo(skill.updated_at),
                  name: creator.name,
                })
              : t(($) => $.detail.header.updated, { when: timeAgo(skill.updated_at) })}
          </span>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Overview 标签页
// ---------------------------------------------------------------------------

function PropertyRow({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid gap-1.5 py-2.5 sm:grid-cols-[128px_minmax(0,1fr)] sm:gap-4">
      <label
        htmlFor={htmlFor}
        className="pt-1.5 text-caption text-muted-foreground sm:text-body"
      >
        {label}
      </label>
      <div className="min-w-0">{children}</div>
    </div>
  );
}

function UsedByList({ agents }: { agents: Agent[] }) {
  const { t } = useT("skills");
  if (agents.length === 0) {
    return (
      <div className="rounded-lg border border-dashed px-3 py-6 text-center text-caption text-muted-foreground">
        {t(($) => $.detail.overview.used_by_empty)}
      </div>
    );
  }
  return (
    <ul className="divide-y overflow-hidden rounded-lg border bg-card">
      {agents.map((a) => (
        <li key={a.id} className="flex items-center gap-2.5 px-3 py-2.5">
          <ActorAvatar
            name={a.name}
            initials={a.name.slice(0, 2).toUpperCase()}
            avatarUrl={resolvePublicFileUrl(a.avatar_url)}
            isAgent
            size="md"
          />
          <div className="min-w-0 flex-1">
            <div className="truncate text-body font-medium">{a.name}</div>
            {a.description && (
              <div className="truncate text-caption text-muted-foreground">
                {a.description}
              </div>
            )}
          </div>
        </li>
      ))}
    </ul>
  );
}

function OverviewTab({
  skill,
  name,
  description,
  canEdit,
  creatorName,
  skillAgents,
  onNameChange,
  onDescriptionChange,
  onAddToAgents,
}: {
  skill: Skill;
  name: string;
  description: string;
  canEdit: boolean;
  creatorName: string | null;
  skillAgents: Agent[];
  onNameChange: (value: string) => void;
  onDescriptionChange: (value: string) => void;
  onAddToAgents: () => void;
}) {
  const { t } = useT("skills");

  return (
    <div className="mx-auto w-full max-w-3xl p-4 sm:p-6 md:p-8">
      <section>
        <h2 className="text-title-sm font-medium">{t(($) => $.detail.overview.properties)}</h2>
        <p className="mt-1 text-caption text-muted-foreground">
          {t(($) => $.detail.overview.properties_hint)}
        </p>
        <div className="mt-4 divide-y">
          <PropertyRow label={t(($) => $.detail.overview.name)} htmlFor="skill-name">
            <Input
              id="skill-name"
              value={name}
              readOnly={!canEdit}
              onChange={(e) => onNameChange(e.target.value)}
              placeholder={t(($) => $.detail.name_placeholder)}
              className="font-mono text-body read-only:cursor-default"
            />
          </PropertyRow>

          <PropertyRow
            label={t(($) => $.detail.overview.description)}
            htmlFor="skill-description"
          >
            {/* 真实描述往往有 500–900 字（承载着 agent 用来匹配技能的触发词），
                所以输入区按数据量定尺寸，而不是早前那两行的高度。 */}
            <Textarea
              id="skill-description"
              value={description}
              readOnly={!canEdit}
              onChange={(e) => onDescriptionChange(e.target.value)}
              placeholder={t(($) => $.detail.description_placeholder)}
              rows={6}
              className="text-body leading-relaxed read-only:cursor-default"
            />
            <p className="mt-1.5 text-caption text-muted-foreground">
              {t(($) => $.detail.overview.description_hint, {
                count: description.length,
              })}
            </p>
          </PropertyRow>

          <PropertyRow label={t(($) => $.detail.overview.labels)}>
            <ResourceLabelPicker
              resourceType="skill"
              resourceId={skill.id}
              canEdit={canEdit}
            />
          </PropertyRow>
        </div>
      </section>

      <section className="mt-10">
        <div className="flex items-center justify-between gap-3">
          <h2 className="min-w-0 text-title-sm font-medium">
            {t(($) => $.detail.overview.used_by, { count: skillAgents.length })}
          </h2>
          <Button
            variant="outline"
            size="xs"
            className="shrink-0 gap-1"
            onClick={onAddToAgents}
          >
            <UserPlus className="h-3 w-3" />
            {t(($) => $.actions.add_to_agent)}
          </Button>
        </div>
        <div className="mt-3">
          <UsedByList agents={skillAgents} />
        </div>
      </section>

      <p className="mt-10 rounded-lg bg-muted px-3 py-2.5 text-caption leading-relaxed text-muted-foreground">
        {canEdit
          ? t(($) => $.detail.overview.permissions_owner)
          : creatorName
            ? t(($) => $.detail.overview.permissions_locked_creator, { name: creatorName })
            : t(($) => $.detail.overview.permissions_locked)}
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Files 标签页
// ---------------------------------------------------------------------------

function FilesTab({
  filePaths,
  selectedPath,
  selectedContent,
  mode,
  canEdit,
  addingFile,
  focusEditor,
  onSelectPath,
  onModeChange,
  onStartAddFile,
  onAddFile,
  onCancelAddFile,
  onDeleteFile,
  onRenameFile,
  onEditFile,
  onContentChange,
  onFocusHandled,
}: {
  filePaths: string[];
  selectedPath: string;
  selectedContent: string;
  mode: FileMode;
  canEdit: boolean;
  addingFile: boolean;
  focusEditor: boolean;
  onSelectPath: (path: string) => void;
  onModeChange: (mode: FileMode) => void;
  onStartAddFile: () => void;
  onAddFile: (path: string) => void;
  onCancelAddFile: () => void;
  onDeleteFile: (path?: string) => void;
  onRenameFile: (from: string, to: string) => void;
  onEditFile: (path: string) => void;
  onContentChange: (content: string) => void;
  onFocusHandled: () => void;
}) {
  const { t } = useT("skills");
  const validatePath = useValidateNewFilePath();
  const supportingPaths = filePaths.filter((p) => p !== SKILL_MD);
  const isMd = isMarkdownPath(selectedPath);
  // 只读查看器不传 actions——树绝不提供一个之后又要拒绝的操作。两条文件树（主文件 /
  // 支持文件）共用同一个对象：SKILL.md 保留「编辑」、去掉改名/删除，树据此从
  // reservedPath 推导每行的可用动作。
  const treeActions = canEdit
    ? {
        onEdit: onEditFile,
        validatePath,
        onRename: onRenameFile,
        onDelete: onDeleteFile,
        reservedPath: SKILL_MD,
      }
    : undefined;

  return (
    <div className="flex min-h-full flex-col md:h-full md:flex-row">
      {/* 文件列表本身就是第二级导航，所以沿用 agent 详情页「能力/设置」导航
          同款侧栏样式，而不是再造第三种侧栏风格。 */}
      <aside
        role="tablist"
        aria-orientation="vertical"
        aria-label={t(($) => $.detail.files.list_aria)}
        className="shrink-0 border-b border-surface-border p-3 md:w-52 md:overflow-y-auto md:border-b-0 md:border-r md:p-4"
      >
        <p className="px-2.5 pb-1 text-micro font-semibold uppercase tracking-wider text-muted-foreground">
          {t(($) => $.detail.files.main)}
        </p>
        <FileTree
          actions={treeActions}
          filePaths={[SKILL_MD]}
          selectedPath={selectedPath}
          onSelect={onSelectPath}
        />

        <p className="px-2.5 pb-1 pt-3 text-micro font-semibold uppercase tracking-wider text-muted-foreground">
          {t(($) => $.detail.files.supporting, { count: supportingPaths.length })}
        </p>
        {supportingPaths.length > 0 ? (
          <FileTree
            actions={treeActions}
            filePaths={supportingPaths}
            selectedPath={selectedPath}
            onSelect={onSelectPath}
          />
        ) : (
          !addingFile && (
            <p className="px-2.5 py-1 text-caption text-muted-foreground">
              {t(($) => $.detail.files.supporting_empty)}
            </p>
          )
        )}

        {canEdit &&
          (addingFile ? (
            <AddFileInline
              existingPaths={filePaths}
              onAdd={onAddFile}
              onCancel={onCancelAddFile}
            />
          ) : (
            <Button
              type="button"
              variant="ghost"
              size="xs"
              onClick={onStartAddFile}
              className="mt-2 h-8 w-full justify-start px-2.5 text-muted-foreground"
            >
              <Plus className="h-3.5 w-3.5" />
              {t(($) => $.detail.files.add_file)}
            </Button>
          ))}
      </aside>

      <section className="flex min-h-[32rem] min-w-0 flex-1 flex-col md:min-h-0">
        <div className="flex h-10 shrink-0 items-center gap-3 border-b px-3 sm:px-4">
          <span className="truncate font-mono text-caption text-muted-foreground">
            {selectedPath}
          </span>
          <div className="ml-auto flex shrink-0 items-center gap-1">
            {isMd && (
              // 第二段文案按「对这个查看者意味着什么」命名：打开的窗格接受输入就叫
              // 「编辑」，同样的窗格只读就叫「纯文本」。底层都是同一个 raw 模式，
              // 变的只是对用户的承诺——绝不提供一个页面之后会拒绝的「编辑」入口，
              // 是这条侧栏一直小心守着的事。
              <div
                role="group"
                aria-label={t(($) => $.detail.files.mode_aria)}
                className="flex items-center gap-0.5 rounded-md bg-muted p-0.5"
              >
                {(["preview", "raw"] as const).map((value) => (
                  <button
                    key={value}
                    type="button"
                    aria-pressed={mode === value}
                    onClick={() => onModeChange(value)}
                    className={cn(
                      "h-6 rounded px-2 text-caption font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                      mode === value
                        ? "bg-surface text-foreground shadow-sm"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    {value === "preview"
                      ? t(($) => $.detail.files.mode_preview)
                      : canEdit
                        ? t(($) => $.detail.files.mode_edit)
                        : t(($) => $.detail.files.mode_raw)}
                  </button>
                ))}
              </div>
            )}
            {selectedPath !== SKILL_MD && canEdit && (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => onDeleteFile()}
                      className="text-muted-foreground hover:text-destructive"
                      aria-label={t(($) => $.detail.delete_file)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  }
                />
                <TooltipContent>{t(($) => $.detail.delete_file)}</TooltipContent>
              </Tooltip>
            )}
          </div>
        </div>

        <div className="min-h-0 flex-1">
          <FileViewer
            key={selectedPath}
            path={selectedPath}
            content={selectedContent}
            mode={mode}
            readOnly={!canEdit}
            autoFocus={focusEditor}
            onChange={onContentChange}
            onFocusHandled={onFocusHandled}
          />
        </div>
      </section>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 页面主组件
// ---------------------------------------------------------------------------

/**
 * 技能详情页（作为路由页内目标被渲染）。接收 skillId，加载技能详情及成员/agent/
 * runtime 等聚合数据；维护 Overview 与 Files 两个标签的本地草稿，统一管理
 * 保存/放弃/删除/从来源刷新/增删改文件等交互，并在「本地有改动 + 远端出新版」
 * 时提示冲突。无编辑权限时整体降级为只读并展示能力提示条。
 */
export function SkillDetailPage({ skillId }: { skillId: string }) {
  const { t } = useT("skills");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const currentUserId = useAuthStore((s) => s.user?.id ?? null);

  const {
    data: skill,
    isLoading,
    error,
  } = useQuery(skillDetailOptions(wsId, skillId));
  const { data: agents = [], error: agentsError } = useQuery(
    agentListOptions(wsId),
  );
  const { data: members = [], error: membersError } = useQuery(
    memberListOptions(wsId),
  );
  const { data: runtimes = [], error: runtimesError } = useQuery(
    runtimeListOptions(wsId),
  );

  const assignments = useMemo(() => selectSkillAssignments(agents), [agents]);

  const canEdit = useCanEditSkill(skill, wsId);
  const skillPermissions = useSkillPermissions(skill ?? null, wsId);

  // 供共用「添加到 agent」弹窗使用的上下文（技能列表页也用同一弹窗）。
  // 普通成员只能看到自己的 agent；工作区 owner/admin 能看到全部。
  const myRole = useMemo(
    () => members.find((m) => m.user_id === currentUserId)?.role ?? null,
    [members, currentUserId],
  );
  const actionsCtx: SkillActionsContext = {
    wsId,
    agents,
    currentUserId,
    isAdmin: myRole === "owner" || myRole === "admin",
  };

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [content, setContent] = useState("");
  const [files, setFiles] = useState<DraftFile[]>([]);
  const [selectedPath, setSelectedPath] = useState(SKILL_MD);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmRefresh, setConfirmRefresh] = useState(false);
  const [showAddToAgents, setShowAddToAgents] = useState(false);
  const [addingFile, setAddingFile] = useState(false);
  const [conflictPending, setConflictPending] = useState(false);

  // 预览/原文是「用户想怎么干活」的属性，所以放在页面层、切换文件时得以保留。
  // 早前它住在 FileViewer 内部，而 FileViewer 按文件 `key` 重挂载——每次切换
  // 文件都被悄悄弹回预览态。
  const [fileMode, setFileMode] = useState<FileMode>("preview");
  // 哪个文件的编辑器正在等光标。用路径而非布尔值：若 effect 跑之前用户已移动选中，
  // 为某一行发起的聚焦请求不会落进另一行的编辑器。
  const [focusPath, setFocusPath] = useState<string | null>(null);

  const urlView = navigation.searchParams.get("view");
  const [activeView, setActiveView] = useState<DetailView>(() =>
    isDetailView(urlView) ? urlView : "overview",
  );
  const lastUrlViewRef = useRef(urlView);

  useEffect(() => {
    if (urlView === lastUrlViewRef.current) return;
    lastUrlViewRef.current = urlView;
    setActiveView(isDetailView(urlView) ? urlView : "overview");
  }, [urlView]);

  const selectView = useCallback(
    (next: DetailView) => {
      setActiveView(next);
      const params = new URLSearchParams(navigation.searchParams);
      if (next === "overview") params.delete("view");
      else params.set("view", next);
      const query = params.toString();
      navigation.replace(`${navigation.pathname}${query ? `?${query}` : ""}`);
    },
    [navigation],
  );

  const draftRef = useRef({ name, description, content, files });
  draftRef.current = { name, description, content, files };

  const seededKeyRef = useRef<string | null>(null);

  /**
   * 本页面最近一次交给用户的草稿（经 toDraft 归一化）。脏状态只以它为基准衡量，
   * 绝不以「最新服务端技能」为基准。
   *
   * 不变量：只有 `seedFromSkill` 写它，且每次写入与推进到 state 的完全是同一份
   * 快照；`dirtySummary` 是键在这些 state 值上的 `useMemo`，因此每次基准写入都
   * 配对一次会触发它重算的状态更新。在别处给这个 ref 赋值会破坏成对关系，
   * 静默重开 MUL-5645。
   */
  const baselineRef = useRef<SkillDraft | null>(null);

  const seedFromSkill = useCallback((s: Skill) => {
    const seeded = toDraft(s);
    setName(seeded.name);
    setDescription(seeded.description);
    setContent(seeded.content);
    setFiles(seeded.files);
    baselineRef.current = seeded;
  }, []);

  /**
   * 整包采信某个服务端版本：草稿、基准、seed 键与冲突标记一起推进。凡接受服务端
   * 版本的路径都走这里——首次加载、静默刷新、保存、放弃——四个部分因此不可能
   * 各自漂移。
   */
  const adoptServerVersion = useCallback(
    (s: Skill, resetSelection = false) => {
      seededKeyRef.current = `${wsId}:${s.id}@${s.updated_at}`;
      setConflictPending(false);
      seedFromSkill(s);
      if (resetSelection) setSelectedPath(SKILL_MD);
    },
    [wsId, seedFromSkill],
  );

  useEffect(() => {
    if (!skill) return;
    if (seededKeyRef.current === `${wsId}:${skill.id}@${skill.updated_at}`) {
      return;
    }

    const sameSkill =
      seededKeyRef.current !== null &&
      seededKeyRef.current.startsWith(`${wsId}:${skill.id}@`);

    // 同一技能、更新的服务端版本。是否构成冲突只看本地草稿：没有本地改动说明用户
    // 只是在读页面，那就静默拉入新版本，而不是诬赖用户没做过的编辑、把编辑器冻在
    // 过期文本上。
    //
    // effect 在草稿变化时也要重跑，而不只在服务端出新版时跑：用户通过「放弃自己
    // 的改动」解决冲突后，页面必须解除冲突态。保存栏受脏标记门控，若过了那个点还
    // 挂着冲突，横幅就会悬在过期文本之上，而「放弃」按钮已无处可点。
    if (sameSkill && hasLocalEdits(draftRef.current, baselineRef.current)) {
      setConflictPending(true);
      return;
    }

    adoptServerVersion(skill, !sameSkill);
  }, [skill, wsId, adoptServerVersion, name, description, content, files]);

  const creator = useMemo<MemberWithUser | null>(
    () =>
      skill?.created_by
        ? members.find((m) => m.user_id === skill.created_by) ?? null
        : null,
    [members, skill?.created_by],
  );

  const origin = useMemo(() => (skill ? readOrigin(skill) : null), [skill]);
  const originRuntime = useMemo<AgentRuntime | null>(() => {
    if (!origin || origin.type !== "runtime_local" || !origin.runtime_id)
      return null;
    return runtimes.find((r) => r.id === origin.runtime_id) ?? null;
  }, [origin, runtimes]);

  const skillAgents = useMemo(
    () => assignments.get(skillId) ?? [],
    [assignments, skillId],
  );

  const fileMap = useMemo(() => {
    const map = new Map<string, string>();
    map.set(SKILL_MD, content);
    for (const f of files) if (f.path.trim()) map.set(f.path, f.content);
    return map;
  }, [content, files]);
  const filePaths = useMemo(() => Array.from(fileMap.keys()), [fileMap]);
  const selectedContent = fileMap.get(selectedPath) ?? "";

  useEffect(() => {
    if (selectedPath !== SKILL_MD && !fileMap.has(selectedPath)) {
      setSelectedPath(SKILL_MD);
    }
  }, [fileMap, selectedPath]);

  // 与种下的基准比较，而非与最新服务端技能比较——远端更新因此绝不可能被读成
  // 一次本地编辑。文件按 id 配对：改名算「一个文件有改动」而不是「删一个加一个」；
  // SKILL.md 因为住在 `content` 里，单独算一项。
  const dirtySummary = useMemo(() => {
    const baseline = baselineRef.current;
    if (!baseline) {
      return { nameChanged: false, descChanged: false, changedFileCount: 0 };
    }
    const baselineById = new Map(
      baseline.files.flatMap((f) => (f.id ? [[f.id, f] as const] : [])),
    );
    let changedFileCount = content !== baseline.content ? 1 : 0;
    const draftIds = new Set<string>();
    for (const f of files) {
      if (f.id) draftIds.add(f.id);
      const base = f.id ? baselineById.get(f.id) : undefined;
      if (!base || base.path !== f.path || base.content !== f.content) {
        changedFileCount += 1;
      }
    }
    for (const f of baseline.files) {
      if (f.id && !draftIds.has(f.id)) changedFileCount += 1;
    }
    return {
      nameChanged: name.trim() !== baseline.name,
      descChanged: description.trim() !== baseline.description,
      changedFileCount,
    };
  }, [name, description, content, files]);

  const isDirty =
    dirtySummary.nameChanged ||
    dirtySummary.descChanged ||
    dirtySummary.changedFileCount > 0;

  const handleSave = async () => {
    if (!skill || !canEdit) return;
    const trimmedName = name.trim();
    const trimmedDesc = description.trim();
    setSaving(true);
    try {
      const payload: UpdateSkillRequest = {
        name: trimmedName,
        description: trimmedDesc,
        content,
        files: files.filter((f) => f.path.trim()),
      };
      const updated = await api.updateSkill(skill.id, payload);
      qc.setQueryData(skillDetailOptions(wsId, skill.id).queryKey, updated);
      adoptServerVersion(updated);
      qc.invalidateQueries({
        queryKey: workspaceKeys.skills(wsId),
        exact: true,
      });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      toast.success(t(($) => $.detail.toast_saved));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.detail.toast_save_failed));
    } finally {
      setSaving(false);
    }
  };

  const handleDiscard = () => {
    if (!skill) return;
    adoptServerVersion(skill);
  };

  const handleDelete = async () => {
    if (!skill) return;
    setDeleting(true);
    try {
      await api.deleteSkill(skill.id);
      navigation.replace(paths.skills());
      qc.removeQueries({
        queryKey: skillDetailOptions(wsId, skill.id).queryKey,
      });
      qc.invalidateQueries({ queryKey: workspaceKeys.skills(wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      toast.success(t(($) => $.detail.toast_deleted));
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : t(($) => $.detail.toast_delete_failed),
      );
      setDeleting(false);
      setConfirmDelete(false);
    }
  };

  const handleAddFile = (path: string) => {
    setFiles((prev) => [...prev, { path, content: "" }]);
    setSelectedPath(path);
    setAddingFile(false);
  };

  // 默认删当前打开的文件，让编辑器自己的删除按钮依旧可用；文件树传入的是被操作的
  // 那一行，它不一定是当前打开的文件。
  const handleDeleteFile = (path: string = selectedPath) => {
    if (path === SKILL_MD) return;
    setFiles((prev) => prev.filter((f) => f.path !== path));
    if (path === selectedPath) setSelectedPath(SKILL_MD);
  };

  // 行菜单里的「编辑」是「一个手势同时做三件事」：打开该文件、把窗格切成编辑器
  // 而非预览、再让光标落进编辑器。少做一件，用户就得多点一次才能开始打字——
  // 而这个入口存在的全部意义就是省下那一次点击。
  const handleEditFile = useCallback(
    (path: string) => {
      if (!canEdit) return;
      setSelectedPath(path);
      setFileMode("raw");
      setFocusPath(path);
    },
    [canEdit],
  );

  const handleFocusHandled = useCallback(() => setFocusPath(null), []);

  const handleRenameFile = (from: string, to: string) => {
    if (from === SKILL_MD) return;
    setFiles((prev) =>
      prev.map((f) => (f.path === from ? { ...f, path: to } : f)),
    );
    // 让选中跟着文件走：侧栏以路径为 key，若选择仍停在旧路径，会落在一条已不
    // 存在的行上。
    if (from === selectedPath) setSelectedPath(to);
  };

  const handleFileContentChange = (newContent: string) => {
    if (!canEdit) return;
    if (selectedPath === SKILL_MD) {
      setContent(newContent);
    } else {
      setFiles((prev) =>
        prev.map((f) =>
          f.path === selectedPath ? { ...f, content: newContent } : f,
        ),
      );
    }
  };

  const supportingQueryDown = !!agentsError || !!membersError || !!runtimesError;

  if (isLoading) {
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <div className="flex h-12 shrink-0 items-center gap-2 border-b px-4">
          <Skeleton className="h-4 w-16" />
          <Skeleton className="h-3 w-3 rounded" />
          <Skeleton className="h-4 w-40" />
        </div>
        <div className="space-y-3 p-6">
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-5/6" />
          <Skeleton className="h-4 w-3/4" />
        </div>
      </div>
    );
  }

  if (error || !skill) {
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <div className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
          <Button
            variant="ghost"
            size="xs"
            render={<AppLink href={paths.skills()} />}
            nativeButton={false}
          >
            {t(($) => $.detail.all_skills)}
          </Button>
        </div>
        <div className="flex flex-1 flex-col items-center justify-center gap-2 text-center">
          <AlertCircle className="h-8 w-8 text-faint-foreground" />
          <p className="text-body font-medium">{t(($) => $.detail.not_found.title)}</p>
          <p className="max-w-xs text-caption text-muted-foreground">
            {error instanceof Error ? error.message : t(($) => $.detail.not_found.fallback)}
          </p>
          <AppLink
            href={paths.skills()}
            className={`${buttonVariants({ variant: "outline", size: "xs" })} mt-2`}
          >
            {t(($) => $.detail.not_found.back)}
          </AppLink>
        </div>
      </div>
    );
  }

  // 各段直接复用 Overview 字段的文案：保存胶囊与它所指的字段，对同一件事
  // 永远用同一个词，不会各说各话。
  const changedParts = [
    dirtySummary.nameChanged ? t(($) => $.detail.overview.name) : null,
    dirtySummary.descChanged ? t(($) => $.detail.overview.description) : null,
    dirtySummary.changedFileCount > 0
      ? t(($) => $.detail.save_bar.changed_files, {
          count: dirtySummary.changedFileCount,
        })
      : null,
  ].filter((part): part is string => part !== null);

  const TABS: { id: DetailView; label: string }[] = [
    { id: "overview", label: t(($) => $.detail.tabs.overview) },
    {
      id: "files",
      label: t(($) => $.detail.tabs.files, { count: totalFileCount(skill) }),
    },
  ];

  return (
    // relative：给浮动保存胶囊当定位锚点——相对页面（而非视口）水平居中，
    // 与技能列表页的批量工具栏同一规则。
    <div className="relative flex flex-1 min-h-0 flex-col">
      <BreadcrumbHeader
        segments={[{ href: paths.skills(), label: t(($) => $.page.title) }]}
        leaf={
          <span className="truncate font-mono text-caption text-foreground">
            {skill.name}
          </span>
        }
        actions={
          <>
            {!canEdit && (
              <span className="inline-flex items-center gap-1 text-caption text-muted-foreground">
                <Lock className="h-3 w-3" />
                {t(($) => $.detail.read_only)}
              </span>
            )}
            {canEdit && origin && isRefreshableOrigin(origin) && (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      variant="outline"
                      size="xs"
                      className="gap-1"
                      onClick={() => setConfirmRefresh(true)}
                    >
                      <RotateCw className="h-3 w-3" />
                      {t(($) => $.detail.refresh.button)}
                    </Button>
                  }
                />
                <TooltipContent>
                  {t(($) => $.detail.refresh.tooltip)}
                </TooltipContent>
              </Tooltip>
            )}
            <Button
              variant="outline"
              size="xs"
              className="gap-1"
              onClick={() => setShowAddToAgents(true)}
            >
              <UserPlus className="h-3 w-3" />
              {t(($) => $.actions.add_to_agent)}
            </Button>
            {canEdit && (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => setConfirmDelete(true)}
                      className="text-muted-foreground hover:text-destructive"
                      aria-label={t(($) => $.detail.delete_aria)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  }
                />
                <TooltipContent>{t(($) => $.detail.delete_tooltip)}</TooltipContent>
              </Tooltip>
            )}
          </>
        }
      />

      {!canEdit && (
        <div className="px-4 pt-3 sm:px-6">
          <CapabilityBanner
            reason={skillPermissions.canEdit.reason}
            resource="skill"
            ownerName={creator?.name}
          />
        </div>
      )}

      {supportingQueryDown && (
        <div
          role="status"
          className="flex shrink-0 items-start gap-2 border-b bg-warning/10 px-4 py-2 text-caption text-muted-foreground sm:px-6"
        >
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning" />
          <span>{t(($) => $.detail.supporting_data_warning)}</span>
        </div>
      )}

      <SkillIdentity
        skill={skill}
        origin={origin}
        originRuntime={originRuntime}
        agentCount={skillAgents.length}
        creator={creator}
      />

      <div
        className="shrink-0 overflow-x-auto border-b px-4 sm:px-6"
        role="tablist"
        aria-label={t(($) => $.detail.tabs.aria)}
      >
        <div className="mx-auto flex max-w-[1440px] items-center gap-6">
          {TABS.map((tab) => (
            <button
              key={tab.id}
              type="button"
              role="tab"
              aria-selected={activeView === tab.id}
              onClick={() => selectView(tab.id)}
              className={cn(
                "relative shrink-0 py-3 text-body font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                activeView === tab.id
                  ? "text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {tab.label}
            </button>
          ))}
        </div>
      </div>

      {conflictPending && canEdit && (
        <div
          role="status"
          aria-live="polite"
          className="flex shrink-0 items-start gap-2 border-b border-warning/30 bg-warning/10 px-4 py-2 text-caption sm:px-6"
        >
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning" />
          <div className="flex-1">
            <div className="font-medium text-foreground">
              {t(($) => $.detail.conflict_banner.title)}
            </div>
            <div className="mt-0.5 text-muted-foreground">
              {t(($) => $.detail.conflict_banner.body)}
            </div>
          </div>
        </div>
      )}

      <div
        className={cn(
          "min-h-0 flex-1 overflow-y-auto",
          activeView === "files" && "md:overflow-hidden",
        )}
      >
        {activeView === "overview" ? (
          <OverviewTab
            skill={skill}
            name={name}
            description={description}
            canEdit={canEdit}
            creatorName={creator?.name ?? null}
            skillAgents={skillAgents}
            onNameChange={setName}
            onDescriptionChange={setDescription}
            onAddToAgents={() => setShowAddToAgents(true)}
          />
        ) : (
          <FilesTab
            filePaths={filePaths}
            selectedPath={selectedPath}
            selectedContent={selectedContent}
            mode={fileMode}
            canEdit={canEdit}
            addingFile={addingFile}
            focusEditor={focusPath === selectedPath}
            onSelectPath={setSelectedPath}
            onModeChange={setFileMode}
            onStartAddFile={() => setAddingFile(true)}
            onAddFile={handleAddFile}
            onCancelAddFile={() => setAddingFile(false)}
            onDeleteFile={handleDeleteFile}
            onRenameFile={handleRenameFile}
            onEditFile={handleEditFile}
            onContentChange={handleFileContentChange}
            onFocusHandled={handleFocusHandled}
          />
        )}
      </div>

      {/* 放在页面级，让两个标签页里的改动都被覆盖到。仅在有脏改动时浮出、
          随内容浮动，与技能列表页的批量工具栏同一套路；锚定页面根（relative），
          而非视口。 */}
      {canEdit && isDirty && (
        <div
          role="status"
          aria-live="polite"
          className="absolute bottom-6 left-1/2 z-50 flex -translate-x-1/2 animate-in items-center gap-1 rounded-lg border bg-background px-2 py-1.5 fade-in slide-in-from-bottom-2 shadow-lg max-md:above-chat-launcher"
        >
          <div className="mr-1 flex items-center border-r pl-1 pr-2">
            <span className="whitespace-nowrap text-caption text-muted-foreground">
              {t(($) => $.detail.save_bar.changed_summary, {
                parts: changedParts.join(" · "),
              })}
            </span>
          </div>
          <Button
            type="button"
            variant="ghost"
            size="xs"
            onClick={handleDiscard}
            disabled={saving}
          >
            {t(($) => $.detail.save_bar.discard)}
          </Button>
          <Button
            type="button"
            size="xs"
            onClick={handleSave}
            disabled={saving || !name.trim()}
          >
            {saving ? (
              <>
                <Loader2 className="h-3 w-3 animate-spin" />
                {t(($) => $.detail.save_bar.saving)}
              </>
            ) : (
              <>
                <Save className="h-3 w-3" />
                {t(($) => $.detail.save_bar.save)}
              </>
            )}
          </Button>
        </div>
      )}

      {/* 删除确认弹窗 */}
      <Dialog
        open={confirmDelete}
        onOpenChange={(v) => {
          if (!v) setConfirmDelete(false);
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t(($) => $.detail.delete_dialog.title)}</DialogTitle>
            <DialogDescription>
              {skillAgents.length > 0
                ? t(($) => $.detail.delete_dialog.description_with_agents, {
                    name: skill.name,
                    count: skillAgents.length,
                  })
                : t(($) => $.detail.delete_dialog.description_no_agents, {
                    name: skill.name,
                  })}
            </DialogDescription>
          </DialogHeader>
          <div className="rounded-md bg-destructive/10 px-3 py-2 text-caption text-destructive">
            {t(($) => $.detail.delete_dialog.warning)}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => setConfirmDelete(false)}
              disabled={deleting}
            >
              {t(($) => $.detail.delete_dialog.cancel)}
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={handleDelete}
              disabled={deleting}
            >
              {deleting ? (
                <>
                  <Loader2 className="h-3 w-3 animate-spin" />
                  {t(($) => $.detail.delete_dialog.deleting)}
                </>
              ) : (
                <>
                  <Trash2 className="h-3 w-3" />
                  {t(($) => $.detail.delete_dialog.confirm)}
                </>
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AddToAgentDialog
        skills={[skill]}
        ctx={actionsCtx}
        open={showAddToAgents}
        onOpenChange={setShowAddToAgents}
      />

      <RefreshSkillDialog
        skill={skill}
        origin={origin}
        wsId={wsId}
        open={confirmRefresh}
        onOpenChange={setConfirmRefresh}
        // 显式采信新版：用户刚确认了覆盖，脏草稿应当被替换掉，而不是触发冲突横幅。
        onRefreshed={(updated) => adoptServerVersion(updated)}
      />
    </div>
  );
}
