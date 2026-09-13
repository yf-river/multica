// 本文件职责：「创建技能」弹窗——先选创建方式（手动编写 / 本地文件夹或 .skill 归档 /
// URL 导入 / 本地 runtime 导入），再按所选方式走各自表单，创建成功回调并关闭。
"use client";

import { useRef, useState, type ChangeEvent } from "react";
import {
  AlertCircle,
  ArrowLeft,
  ChevronRight,
  Download,
  FileArchive,
  FolderOpen,
  HardDrive,
  Loader2,
  Pencil,
  Plus,
  X as XIcon,
} from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { Skill } from "@multica/core/types";
import {
  prepareSkillArchiveFromPickerFiles,
  wrapExistingSkillArchive,
  type PreparedSkillArchive,
} from "@multica/core/skills";
import { useWorkspaceId } from "@multica/core/hooks";
import { isImeComposing } from "@multica/core/utils";
import {
  skillDetailOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useScrollFade } from "@multica/ui/hooks/use-scroll-fade";
import { cn } from "@multica/ui/lib/utils";
import { openExternal } from "../../platform";
import { RuntimeLocalSkillImportPanel } from "./runtime-local-skill-import-panel";
import { useT } from "../../i18n";
import { isNameConflictError } from "../lib/utils";

// 创建弹窗当前所处的方式：chooser（选择卡）或某一具体表单。
type Method = "chooser" | "manual" | "local" | "url" | "runtime";

/** 创建成功后：把返回的 skill 直接种进详情缓存，并让列表/agent 缓存失效刷新。 */
function seedAfterCreate(
  qc: ReturnType<typeof useQueryClient>,
  wsId: string,
  skill: Skill,
) {
  qc.setQueryData(skillDetailOptions(wsId, skill.id).queryKey, skill);
  qc.invalidateQueries({ queryKey: workspaceKeys.skills(wsId) });
  qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
}

// ---------------------------------------------------------------------------
// 方式选择器——初始的四种创建方式卡片
// ---------------------------------------------------------------------------

/** 方式选择器：列出四种创建方式（手动 / 本地 / URL / runtime）各一张可点卡片。 */
function MethodChooser({ onChoose }: { onChoose: (m: Method) => void }) {
  const { t } = useT("skills");
  const methods: {
    key: Exclude<Method, "chooser">;
    icon: typeof Plus;
  }[] = [
    { key: "manual", icon: Plus },
    { key: "local", icon: FolderOpen },
    { key: "url", icon: Download },
    { key: "runtime", icon: HardDrive },
  ];
  return (
    <div className="grid gap-2 p-5">
      {methods.map(({ key, icon: Icon }) => (
        <button
          key={key}
          type="button"
          onClick={() => onChoose(key)}
          className="group flex items-start gap-3 rounded-lg border bg-card p-4 text-left transition-colors hover:border-primary/40 hover:bg-accent/40"
        >
          <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground group-hover:text-foreground">
            <Icon className="h-4 w-4" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="text-body font-medium">
              {t(($) => $.create.method[key].title)}
            </div>
            <div className="mt-0.5 text-caption text-muted-foreground">
              {t(($) => $.create.method[key].desc)}
            </div>
          </div>
          <ChevronRight className="h-4 w-4 shrink-0 text-faint-foreground transition-colors group-hover:text-muted-foreground" />
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 手动编写表单
// ---------------------------------------------------------------------------

/** 手动创建表单：填名称（回车可提交）+ 描述，名称冲突时给额外提示。 */
function ManualForm({
  onCreated,
  onCancel,
}: {
  onCreated: (skill: Skill) => void;
  onCancel: () => void;
}) {
  const { t } = useT("skills");
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const scrollRef = useRef<HTMLDivElement>(null);
  const fadeStyle = useScrollFade(scrollRef);

  const submit = async () => {
    const trimmed = name.trim();
    if (!trimmed) return;
    setLoading(true);
    setError("");
    try {
      const skill = await api.createSkill({
        name: trimmed,
        description: description.trim(),
      });
      seedAfterCreate(qc, wsId, skill);
      toast.success(t(($) => $.create.manual.toast_created));
      onCreated(skill);
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.create.manual.fallback_error));
      setLoading(false);
    }
  };

  return (
    <>
      <div
        ref={scrollRef}
        style={fadeStyle}
        className="flex-1 min-h-0 space-y-4 overflow-y-auto px-5 py-4"
      >
        <div className="space-y-1.5">
          <Label
            htmlFor="create-skill-name"
            className="text-caption text-muted-foreground"
          >
            {t(($) => $.create.manual.name_label)}
          </Label>
          <Input
            id="create-skill-name"
            autoFocus
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setError("");
            }}
            placeholder={t(($) => $.create.manual.name_placeholder)}
            onKeyDown={(e) => {
              if (isImeComposing(e)) return;
              if (e.key === "Enter") submit();
            }}
          />
          <p className="text-caption text-muted-foreground">
            {t(($) => $.create.manual.name_hint)}
          </p>
        </div>

        <div className="space-y-1.5">
          <Label
            htmlFor="create-skill-desc"
            className="text-caption text-muted-foreground"
          >
            <Pencil className="h-3 w-3" />
            {t(($) => $.create.manual.description_label)}
          </Label>
          <Textarea
            id="create-skill-desc"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={t(($) => $.create.manual.description_placeholder)}
            rows={3}
            className="resize-none"
          />
        </div>

        {error && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-caption text-destructive"
          >
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>
              {error}
              {isNameConflictError(error) && (
                <>{t(($) => $.create.manual.name_conflict_hint)}</>
              )}
            </span>
          </div>
        )}
      </div>

      <div className="flex shrink-0 items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={onCancel}
          disabled={loading}
        >
          {t(($) => $.create.manual.cancel)}
        </Button>
        <Button
          type="button"
          size="sm"
          onClick={submit}
          disabled={!name.trim() || loading}
        >
          {loading ? (
            <>
              <Loader2 className="h-3 w-3 animate-spin" />
              {t(($) => $.create.manual.submitting)}
            </>
          ) : (
            t(($) => $.create.manual.submit)
          )}
        </Button>
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// URL 导入表单
// ---------------------------------------------------------------------------

// 从 URL 里能嗅探出的托管来源。
type DetectedSource = "clawhub" | "skills.sh" | "github" | null;

/** 用主机名粗判 URL 属于哪个托管来源；都不匹配返回 null。 */
function detectUrlSource(url: string): DetectedSource {
  const u = url.trim().toLowerCase();
  if (u.includes("clawhub.ai")) return "clawhub";
  if (u.includes("skills.sh")) return "skills.sh";
  if (u.includes("github.com")) return "github";
  return null;
}

/** 某个受支持来源的说明卡片：展示示例地址，可点击去该站浏览。 */
function SourceCard({
  label,
  exampleHost,
  browseUrl,
  active,
}: {
  label: string;
  exampleHost: string;
  browseUrl: string;
  active: boolean;
}) {
  return (
    <div
      className={`rounded-md border px-3 py-2.5 transition-colors ${
        active ? "border-primary bg-primary/5" : ""
      }`}
    >
      <div className="text-caption font-medium">{label}</div>
      <button
        type="button"
        onClick={() => openExternal(browseUrl)}
        className="mt-0.5 block max-w-full truncate text-left font-mono text-caption text-brand underline decoration-brand/40 underline-offset-2 hover:decoration-brand"
      >
        {exampleHost}
      </button>
    </div>
  );
}

/** URL 导入表单：粘贴来源链接，按嗅探到的来源给出差异化的「导入中…」文案。 */
function UrlForm({
  onCreated,
  onCancel,
}: {
  onCreated: (skill: Skill) => void;
  onCancel: () => void;
}) {
  const { t } = useT("skills");
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const [url, setUrl] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const source = detectUrlSource(url);
  const scrollRef = useRef<HTMLDivElement>(null);
  const fadeStyle = useScrollFade(scrollRef);

  const submit = async () => {
    const trimmed = url.trim();
    if (!trimmed) return;
    setLoading(true);
    setError("");
    try {
      const skill = await api.importSkill({ url: trimmed });
      seedAfterCreate(qc, wsId, skill);
      toast.success(t(($) => $.create.url.toast_imported));
      onCreated(skill);
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.create.url.fallback_error));
      setLoading(false);
    }
  };

  const submittingLabel = (() => {
    if (!loading) return t(($) => $.create.url.import);
    if (source === "clawhub") return t(($) => $.create.url.importing_clawhub);
    if (source === "skills.sh") return t(($) => $.create.url.importing_skills_sh);
    if (source === "github") return t(($) => $.create.url.importing_github);
    return t(($) => $.create.url.importing);
  })();

  return (
    <>
      <div
        ref={scrollRef}
        style={fadeStyle}
        className="flex-1 min-h-0 space-y-4 overflow-y-auto px-5 py-4"
      >
        <div className="space-y-1.5">
          <Label htmlFor="import-url" className="text-caption text-muted-foreground">
            {t(($) => $.create.url.url_label)}
          </Label>
          <Input
            id="import-url"
            autoFocus
            value={url}
            onChange={(e) => {
              setUrl(e.target.value);
              setError("");
            }}
            placeholder="https://clawhub.ai/owner/skill"
            className="font-mono text-body"
            onKeyDown={(e) => {
              if (e.key === "Enter") submit();
            }}
          />
        </div>

        <div>
          <p className="mb-2 text-caption text-muted-foreground">
            {t(($) => $.create.url.supported_sources)}
          </p>
          <div className="grid grid-cols-3 gap-2">
            <SourceCard
              label="ClawHub"
              exampleHost="clawhub.ai/owner/skill"
              browseUrl="https://clawhub.ai"
              active={source === "clawhub"}
            />
            <SourceCard
              label="Skills.sh"
              exampleHost="skills.sh/owner/repo/skill"
              browseUrl="https://skills.sh"
              active={source === "skills.sh"}
            />
            <SourceCard
              label="GitHub"
              exampleHost="github.com/owner/repo"
              browseUrl="https://github.com"
              active={source === "github"}
            />
          </div>
        </div>

        {error && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-caption text-destructive"
          >
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>
              {error}
              {isNameConflictError(error) && (
                <>{t(($) => $.create.url.name_conflict_hint)}</>
              )}
            </span>
          </div>
        )}
      </div>

      <div className="flex shrink-0 items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={onCancel}
          disabled={loading}
        >
          {t(($) => $.create.url.cancel)}
        </Button>
        <Button
          type="button"
          size="sm"
          onClick={submit}
          disabled={!url.trim() || loading}
        >
          {loading ? (
            <>
              <Loader2 className="h-3 w-3 animate-spin" />
              {submittingLabel}
            </>
          ) : (
            <>
              <Download className="h-3 w-3" />
              {submittingLabel}
            </>
          )}
        </Button>
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// 本地文件夹 / .skill 归档导入
// ---------------------------------------------------------------------------

/**
 * 本地导入表单：从「文件夹」或「.skill/.zip 归档」二选一，预检（缺失 SKILL.md、
 * 过大、为空、文件过多）通过后展示预览并允许导入。
 */
function LocalForm({
  prepared,
  preparing,
  onCreated,
  onCancel,
  onChooseFolder,
  onChooseArchive,
}: {
  prepared: PreparedSkillArchive | null;
  preparing: boolean;
  onCreated: (skill: Skill) => void;
  onCancel: () => void;
  onChooseFolder: () => void;
  onChooseArchive: () => void;
}) {
  const { t } = useT("skills");
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const scrollRef = useRef<HTMLDivElement>(null);
  const fadeStyle = useScrollFade(scrollRef);

  // 未选到「合法归档」前导入按钮不可用，因此非法选择到不了这里——下方 `prepareError`
  // 已经解释了它为什么非法。
  const submit = async () => {
    if (!prepared?.ok) return;
    setLoading(true);
    setError("");
    try {
      const skill = await api.importSkillArchive(prepared.file, "fail");
      seedAfterCreate(qc, wsId, skill);
      toast.success(t(($) => $.create.local.toast_imported));
      onCreated(skill);
    } catch (err) {
      setError(
        err instanceof Error ? err.message : t(($) => $.create.local.fallback_error),
      );
      setLoading(false);
    }
  };

  const prepareError =
    prepared && !prepared.ok
      ? {
          missing_skill_md: t(($) => $.create.local.missing_skill_md),
          too_large: t(($) => $.create.local.too_large),
          empty: t(($) => $.create.local.empty),
          too_many_files: t(($) => $.create.local.too_many_files),
        }[prepared.error]
      : "";
  const displayError = error || prepareError;

  return (
    <>
      <div
        ref={scrollRef}
        style={fadeStyle}
        className="flex-1 min-h-0 space-y-4 overflow-y-auto px-5 py-4"
      >
        {preparing && (
          <div className="flex items-center gap-2 text-caption text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            {t(($) => $.create.local.importing)}
          </div>
        )}

        {prepared?.ok && (
          <div className="space-y-3">
            <div className="rounded-md border px-3 py-2.5">
              <div className="text-caption text-muted-foreground">
                {prepared.preview.source === "archive"
                  ? t(($) => $.create.local.archive_label)
                  : t(($) => $.create.local.folder_label)}
              </div>
              <div className="mt-0.5 truncate text-body font-medium">
                {prepared.preview.displayName}
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-body font-medium">{prepared.preview.skillName}</div>
              {prepared.preview.description ? (
                <p className="text-caption text-muted-foreground">
                  {prepared.preview.description}
                </p>
              ) : null}
              {prepared.preview.fileCount != null ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.create.local.files, {
                    count: prepared.preview.fileCount,
                  })}
                </p>
              ) : null}
            </div>
          </div>
        )}

        {/* 来源选择按钮放在正文里：四个按钮若都进 footer 会撑破弹窗宽度、夹掉取消钮
            （MUL-6794）。 */}
        <div className="grid grid-cols-2 gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-full"
            onClick={onChooseFolder}
            disabled={loading || preparing}
          >
            <FolderOpen className="h-3 w-3" />
            {t(($) => $.create.local.choose_folder)}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-full"
            onClick={onChooseArchive}
            disabled={loading || preparing}
          >
            <FileArchive className="h-3 w-3" />
            {t(($) => $.create.local.choose_archive)}
          </Button>
        </div>

        {displayError && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-caption text-destructive"
          >
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>
              {displayError}
              {isNameConflictError(displayError) && (
                <>{t(($) => $.create.local.name_conflict_hint)}</>
              )}
            </span>
          </div>
        )}
      </div>

      <div className="flex shrink-0 items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={onCancel}
          disabled={loading}
        >
          {t(($) => $.create.local.cancel)}
        </Button>
        <Button
          type="button"
          size="sm"
          onClick={submit}
          disabled={!prepared?.ok || loading || preparing}
        >
          {loading ? (
            <>
              <Loader2 className="h-3 w-3 animate-spin" />
              {t(($) => $.create.local.importing)}
            </>
          ) : (
            <>
              <FolderOpen className="h-3 w-3" />
              {t(($) => $.create.local.import)}
            </>
          )}
        </Button>
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// 根弹窗——在四种创建方式间切换视图
// ---------------------------------------------------------------------------

/**
 * 「创建技能」弹窗。
 *
 * 以 method 状态在「方式选择卡 → 具体表单」间切换：手动表单/本地导入/URL 导入/本地
 * runtime 导入。本地导入保持隐藏的 <input type="file">（目录与归档各一）常驻挂载，
 * 并专门处理「重新选同一文件夹」与异步预检的竞态。创建成功统一回调 onCreated 并关闭。
 *
 * @param onClose   关闭弹窗的回调
 * @param onCreated 某技能创建成功后的回调
 */
export function CreateSkillDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated?: (skill: Skill) => void;
}) {
  const { t } = useT("skills");
  const [method, setMethod] = useState<Method>("chooser");
  const [localPrepared, setLocalPrepared] = useState<PreparedSkillArchive | null>(
    null,
  );
  const [localPreparing, setLocalPreparing] = useState(false);
  const [localEpoch, setLocalEpoch] = useState(0);
  const localGeneration = useRef(0);
  const folderInputRef = useRef<HTMLInputElement>(null);
  const archiveInputRef = useRef<HTMLInputElement>(null);

  const handleCreated = (skill: Skill) => {
    onCreated?.(skill);
    onClose();
  };

  const beginLocalSelection = (): number => {
    const next = localGeneration.current + 1;
    localGeneration.current = next;
    setLocalEpoch(next);
    return next;
  };

  const resetLocal = () => {
    beginLocalSelection();
    setLocalPrepared(null);
    setLocalPreparing(false);
  };

  const bindDirectoryInput = (el: HTMLInputElement | null) => {
    folderInputRef.current = el;
    if (!el) return;
    el.setAttribute("webkitdirectory", "");
    el.setAttribute("directory", "");
  };

  const openFolderPicker = () => {
    folderInputRef.current?.click();
  };

  const openArchivePicker = () => {
    archiveInputRef.current?.click();
  };

  const handleChoose = (next: Method) => {
    if (next === "local") {
      // 先切视图再弹选择器：这样即使用户取消了系统文件选择器，也会落在「本地导入」
      // 面板上（那里有 choose-folder / choose-archive 两个按钮可重试），而不是无声地
      // 停在选择卡、没有任何入口选 .skill 文件。
      setMethod("local");
      openFolderPicker();
      return;
    }
    resetLocal();
    setMethod(next);
  };

  const onFolderPicked = async (e: ChangeEvent<HTMLInputElement>) => {
    // FileList 是「活的」：清空 input 会同时清空同一个对象。先快照 File 句柄，
    // 之后重置 `value`（以便能再次选同一文件夹）才不会把已选文件弄丢。
    const files = Array.from(e.target.files ?? []);
    e.target.value = "";
    if (files.length === 0) return;
    const gen = beginLocalSelection();
    setMethod("local");
    setLocalPreparing(true);
    setLocalPrepared(null);
    try {
      const prepared = await prepareSkillArchiveFromPickerFiles(files);
      if (gen !== localGeneration.current) return;
      setLocalPrepared(prepared);
    } catch {
      if (gen !== localGeneration.current) return;
      setLocalPrepared({ ok: false, error: "empty" });
    } finally {
      if (gen === localGeneration.current) setLocalPreparing(false);
    }
  };

  const onArchivePicked = (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    beginLocalSelection();
    setMethod("local");
    setLocalPreparing(false);
    setLocalPrepared(wrapExistingSkillArchive(file));
  };

  const wide = method === "runtime";

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent
        showCloseButton={false}
        className={cn(
          "flex flex-col gap-0 overflow-hidden p-0",
          "!transition-all !duration-300 !ease-out",
          wide
            ? "!h-[min(600px,85vh)] !max-w-2xl !w-full"
            : "!h-auto !max-h-[85vh] !max-w-md !w-full",
        )}
      >
        {/* 头部——返回（非首屏时）/ 标题与说明 / 关闭 */}
        <div className="flex shrink-0 items-start justify-between gap-3 border-b px-5 pt-4 pb-3">
          <div className="flex items-start gap-2 min-w-0">
            {method !== "chooser" && (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <button
                      type="button"
                      onClick={() => {
                        resetLocal();
                        setMethod("chooser");
                      }}
                      className="-ml-1 mt-px rounded-sm p-1 text-faint-foreground transition-colors hover:bg-accent/60 hover:text-muted-foreground"
                      aria-label={t(($) => $.create.back_aria)}
                    >
                      <ArrowLeft className="h-3.5 w-3.5" />
                    </button>
                  }
                />
                <TooltipContent side="bottom">{t(($) => $.create.back)}</TooltipContent>
              </Tooltip>
            )}
            <div className="min-w-0">
              <DialogTitle className="truncate text-title-sm font-medium">
                {t(($) => $.create.method[method].title)}
              </DialogTitle>
              <p className="mt-0.5 text-caption text-muted-foreground">
                {t(($) => $.create.method[method].desc)}
              </p>
            </div>
          </div>
          <Tooltip>
            <TooltipTrigger
              render={
                <button
                  type="button"
                  onClick={onClose}
                  className="rounded-sm p-1 text-faint-foreground transition-colors hover:bg-accent/60 hover:text-muted-foreground"
                  aria-label={t(($) => $.create.close_aria)}
                >
                  <XIcon className="h-3.5 w-3.5" />
                </button>
              }
            />
            <TooltipContent side="bottom">{t(($) => $.create.close)}</TooltipContent>
          </Tooltip>
        </div>

        {/* 隐藏的文件选择器保持挂载，让「方式卡点击」能在同一个用户手势里触发选择器
            （否则浏览器会拦截 input.click()）。 */}
        <input
          ref={bindDirectoryInput}
          type="file"
          multiple
          className="sr-only"
          tabIndex={-1}
          aria-hidden
          onChange={onFolderPicked}
        />
        <input
          ref={archiveInputRef}
          type="file"
          accept=".skill,.zip,application/zip"
          className="sr-only"
          tabIndex={-1}
          aria-hidden
          onChange={onArchivePicked}
        />

        {/* 方式主体——每个表单自带「滚动区 + footer」 */}
        {method === "chooser" && <MethodChooser onChoose={handleChoose} />}
        {method === "manual" && (
          <ManualForm
            onCreated={handleCreated}
            onCancel={() => setMethod("chooser")}
          />
        )}
        {method === "local" && (
          <LocalForm
            key={localEpoch}
            prepared={localPrepared}
            preparing={localPreparing}
            onCreated={handleCreated}
            onCancel={() => {
              resetLocal();
              setMethod("chooser");
            }}
            onChooseFolder={openFolderPicker}
            onChooseArchive={openArchivePicker}
          />
        )}
        {method === "url" && (
          <UrlForm
            onCreated={handleCreated}
            onCancel={() => setMethod("chooser")}
          />
        )}
        {method === "runtime" && (
          <RuntimeLocalSkillImportPanel
            onImported={handleCreated}
            onBulkDone={onClose}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
