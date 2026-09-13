"use client";

// 引导第 2 步「创建工作区」：选择「继续用早前会话建好的工作区」或当场命名新建，
// 页脚统一一个 CTA。新建字段里内联了 slug 自动填充、任务编号前缀预览等。
import { type ReactNode, useRef, useEffect, useState } from "react";
import { Dices, Plus } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@multica/ui/components/ui/field";
import { cn } from "@multica/ui/lib/utils";
import { useCreateWorkspace } from "@multica/core/workspace/mutations";
import type { Workspace } from "@multica/core/types";
import { isImeComposing } from "@multica/core/utils";
import { useConfigStore } from "@multica/core/config";
import { workspaceUrlHost } from "@multica/core/workspace/workspace-url";
import { useLogout } from "../../auth";
import {
  StepFooter,
  StepHeading,
} from "../components/step-shell";
import { RadioMark } from "../components/option-card";
import { WorkspaceAvatar } from "../../workspace/workspace-avatar";
import { useT } from "../../i18n";
import {
  WORKSPACE_SLUG_REGEX,
  isWorkspaceSlugConflict,
  nameToWorkspaceSlug,
  randomCelestialWorkspaceIdentity,
} from "../../workspace/slug";
import { isReservedSlug } from "@multica/core/paths";

/** 从工作区 slug 派生任务编号前缀的小工具（被 StepWorkspace 使用）。 */
function issuePrefix(slug: string): string {
  // 与服务端默认前缀派生逻辑一致（handler.defaultIssuePrefixFromSlug）——取 slug
  // 里的字母数字、前 4 位、转大写。这里先小写再取，是因为服务端派生前也会先小写
  // slug：用户在这输入「ACME」会得到服务端同样会产出的「ACME」，而不是空串。
  // slug 中无可派生的字符时返回 ""——这种 slug 本来就无法提交，只有预览需要
  // 面对空串。
  return slug
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]/g, "")
    .slice(0, 4)
    .toUpperCase();
}

// 只保留字母数字、全大写、最长 10 位——与设置页的同款护栏一致，也是服务端现在
// 校验的形状（`^[A-Z0-9]{1,10}$`）。
function normalizePrefix(raw: string): string {
  return raw.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 10);
}

/**
 * 第 2 步——创建你的第一个工作区，或继续用更早会话建好的那个。
 *
 * 和其它步骤一样是单列通栏：一个三段式应用外壳（头部 / 可滚动中部 / 页脚），
 * 表单居中。**页脚只有一个统一的 CTA** 同时覆盖两条路径——用户选了已有工作区时
 * 显示「打开 X」，命名新工作区时显示「创建 X」。名字 / slug 字段内联在这里
 * （不走共享的 `CreateWorkspaceForm`），因为这种由页脚驱动的交互需要外部化的
 * 提交；共享表单自带的按钮会和页脚 CTA 打架。
 *
 * 创建字段块顺带充当教学预览：URL 渲染成 `<host>/[slug]` 的药丸（host 取自当前
 * 部署的 app URL，自托管实例会显示自己的域名），还有一行实时预览「Issues will
 * look like ACME-123」，让用户在还没创建任何东西前就知道任务编号长什么样。预览
 * 背后是「任务编号前缀」——一个从 slug 预填、且可编辑的字段（MUL-6050）：它
 * 以前是只读的，导致所有非 ASCII 名字的工作区只能困在服务端旧的 `WS` 兜底上，
 * 流程里没有任何办法改。
 *
 * 「继续用已有」路径展示两张选择卡（已有 + 新建），用户在两者间切换；
 * 「没有已有工作区」的路径则直接展示创建字段。
 *
 * @param existing 早前会话建好的工作区（可为 null / 缺省）
 * @param onCreated 工作区确定后的回调（打开已有或新建都会走到）
 * @param onBusyChange 把创建请求的进行中状态上报给外壳（用于锁「返回」和进度栏）
 */
export function StepWorkspace({
  existing,
  onCreated,
  onBusyChange,
}: {
  existing?: Workspace | null;
  onCreated: (workspace: Workspace) => void | Promise<void>;
  /** 把创建请求的进行中状态上报给流程（它持有外壳）：创建工作区期间必须锁住
   *  「返回」和进度栏，而只有这一步知道何时处于创建中。 */
  onBusyChange?: (busy: boolean) => void;
}) {
  const { t } = useT("onboarding");
  const workspaceCreationDisabled = useConfigStore((s) => s.workspaceCreationDisabled);
  const urlHost = workspaceUrlHost(useConfigStore((s) => s.daemonAppUrl));
  // 「这个实例允不允许用户走创建路径」的唯一真源——它驱动续用模式的选择卡、
  // 眉题/标题/引导语文案和页脚 CTA，这样即使 /api/config 姗姗来迟，禁用状态也
  // 不会漏出一个可点的创建入口（#3433 评审反馈）。
  const workspaceCreationAllowed = !workspaceCreationDisabled;
  const logout = useLogout();

  const reusing = existing ?? null;
  // 仅「续用」路径使用：用户选哪张卡。`null` = 都还没选，此时页脚 CTA 保持禁用。
  // 点卡片是切换——再点同一张会取消选中。「没有已有工作区」的路径完全无视这个
  // 状态。当创建被禁用、且已有工作区存在时，默认选中「已有」，让用户能立刻按 CTA
  // ——那是唯一有效的动作。
  const [mode, setMode] = useState<"existing" | "create" | null>(() =>
    !workspaceCreationAllowed && existing ? "existing" : null,
  );
  const pickExisting = () =>
    setMode((m) => (m === "existing" ? null : "existing"));
  const pickCreate = () =>
    setMode((m) => (m === "create" ? null : "create"));

  // 创建路径的表单状态，镜像 CreateWorkspaceForm 的内部行为：slug 在用户手动
  // 改之前跟随 name 自动填充；服务端 slug 冲突以内联错误展示。状态放这层，是为了
  // 让页脚 CTA 能读到 `canCreate` 并触发 `handleCreate`。
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugServerError, setSlugServerError] = useState<string | null>(null);
  const slugTouched = useRef(false);
  // 前缀跟随 slug，就像 slug 跟随 name 一样；一旦用户改了它，就不再跟随
  // （MUL-6050）。这里做成可编辑，是因为设置页是唯一能改它的地方，而从没注意到
  // 这个默认值的用户绝不会专门去找设置。
  const [prefix, setPrefix] = useState("");
  const prefixTouched = useRef(false);

  const slugValidationError =
    slug.length > 0 && !WORKSPACE_SLUG_REGEX.test(slug)
      ? t(($) => $.step_workspace.slug_format_error)
      : null;
  const slugReservedError =
    slug.length > 0 && isReservedSlug(slug)
      ? t(($) => $.step_workspace.slug_reserved_error)
      : null;
  const slugError = slugValidationError ?? slugReservedError ?? slugServerError;
  const canCreate =
    name.trim().length > 0 && slug.trim().length > 0 && !slugError;

  // 工作区真正会用的前缀。清空前缀输入会回退到 slug 派生的默认值，而不是拦住
  // CTA——placeholder 里就展示着那个默认值，所以空字段从不是意外。只有在 slug
  // 也为空时这里才为空（名字罗马化后没有任何字符就派生不出 slug），而那恰是
  // `canCreate` 为 false 的时候，所以提交时永远带着真实前缀。
  const derivedPrefix = issuePrefix(slug);
  const effectivePrefix = prefix || derivedPrefix;

  // 所有 slug 写入都经过这里，保证「没被用户动过」的前缀不会跟它派生的 slug 脱节。
  const applySlug = (value: string) => {
    setSlug(value);
    setSlugServerError(null);
    if (!prefixTouched.current) setPrefix(issuePrefix(value));
  };

  const handleNameChange = (value: string) => {
    setName(value);
    if (!slugTouched.current) {
      applySlug(nameToWorkspaceSlug(value));
    }
  };

  const handleSlugChange = (value: string) => {
    slugTouched.current = true;
    applySlug(value);
  };

  const handlePrefixChange = (value: string) => {
    prefixTouched.current = true;
    setPrefix(normalizePrefix(value));
  };

  const handleRandomName = () => {
    const identity = randomCelestialWorkspaceIdentity();
    slugTouched.current = true;
    setName(identity.name);
    applySlug(identity.slug);
  };

  const createWorkspace = useCreateWorkspace();

  const handleCreate = () => {
    if (!canCreate || createWorkspace.isPending) return;
    createWorkspace.mutate(
      {
        name: name.trim(),
        slug: slug.trim(),
        // 提交用户看到的那个值。字段缺省时服务端也会从 slug 派生出同样的值，
        // 所以预览和建出来的工作区无论如何都一致——但显式提交正是让「改过的
        // 前缀」生效的方式。
        issue_prefix: effectivePrefix,
      },
      {
        onSuccess: onCreated,
        onError: (error) => {
          if (isWorkspaceSlugConflict(error)) {
            setSlugServerError(t(($) => $.step_workspace.slug_taken_error));
            toast.error(t(($) => $.step_workspace.slug_conflict_toast));
            return;
          }
          toast.error(
            error instanceof Error && error.message
              ? error.message
              : t(($) => $.step_workspace.create_failed_toast),
          );
        },
      },
    );
  };

  // 按用户所在路径算出页脚 CTA。`null` 只在续用路径里可达；`existing` 只有确实
  // 有 `reusing` 工作区时有效；其余所有情况（包括没有已有工作区的路径）都归到
  // `create`——除非本实例开了 DISABLE_WORKSPACE_CREATION=true，此时创建路径
  // 不可达，没有可续用工作区的用户会落到下面的禁用提示上。
  const isCreating = createWorkspace.isPending;
  useEffect(() => {
    onBusyChange?.(isCreating);
    // 卸载时清掉：创建成功后流程立刻前进一步，不清的话外壳会在下一步仍然锁着。
    return () => onBusyChange?.(false);
  }, [isCreating, onBusyChange]);
  const creatingActive =
    workspaceCreationAllowed && (!reusing || mode === "create");
  const existingActive = Boolean(reusing) && mode === "existing";

  let hint: string;
  let continueLabel: string;
  let continueDisabled: boolean;
  let onContinue: () => void;

  if (existingActive && reusing) {
    hint = t(($) => $.step_workspace.hint_opening, { name: reusing.name });
    continueLabel = t(($) => $.step_workspace.cta_open, { name: reusing.name });
    continueDisabled = isCreating;
    onContinue = () => onCreated(reusing);
  } else if (creatingActive) {
    if (isCreating) {
      hint = t(($) => $.step_workspace.hint_creating_pending, {
        name: name.trim() || t(($) => $.step_workspace.hint_creating_fallback),
      });
      continueLabel = t(($) => $.step_workspace.cta_creating);
      continueDisabled = true;
      onContinue = () => {};
    } else if (canCreate) {
      hint = t(($) => $.step_workspace.hint_creating, { name: name.trim() });
      continueLabel = t(($) => $.step_workspace.cta_create_named, { name: name.trim() });
      continueDisabled = false;
      onContinue = handleCreate;
    } else {
      hint = t(($) => $.step_workspace.hint_name_first);
      continueLabel = t(($) => $.step_workspace.cta_create_workspace);
      continueDisabled = true;
      onContinue = () => {};
    }
  } else {
    hint = t(($) => $.step_workspace.hint_pick);
    continueLabel = t(($) => $.common.continue);
    continueDisabled = true;
    onContinue = () => {};
  }

  // 用 Field 基础组件而不是手搓的 label/hint/error 标记：三个各自带字号间距的
  // `flex flex-col gap-1.5` 堆叠，正是 Field/FieldLabel/FieldError 标准化的东西，
  // 而且手搓版本已经走样——label 是 caption 大小、还带灰，而产品里其它表单的
  // label 都是正文字重。
  const createFields = (
    <FieldGroup>
      <Field>
        <FieldLabel htmlFor="ws-name">
          {t(($) => $.step_workspace.name_label)}
        </FieldLabel>
        <div className="flex items-center gap-2">
          <Input
            id="ws-name"
            autoFocus
            type="text"
            value={name}
            onChange={(e) => handleNameChange(e.target.value)}
            placeholder={t(($) => $.step_workspace.name_placeholder)}
            className="min-w-0"
            onKeyDown={(e) => {
              if (isImeComposing(e)) return;
              if (e.key === "Enter") handleCreate();
            }}
          />
          <Button
            type="button"
            variant="outline"
            onClick={handleRandomName}
            disabled={isCreating}
            className="shrink-0"
          >
            <Dices className="h-4 w-4" />
            {t(($) => $.step_workspace.random_name)}
          </Button>
        </div>
      </Field>
      <Field data-invalid={slugError ? true : undefined}>
        <FieldLabel htmlFor="ws-slug">
          {t(($) => $.step_workspace.url_label)}
        </FieldLabel>
        <div className="flex items-center rounded-md border bg-muted transition-colors focus-within:border-foreground aria-invalid:border-destructive">
          <span className="select-none pl-3 font-mono text-body text-muted-foreground">
            {`${urlHost}/`}
          </span>
          <Input
            id="ws-slug"
            type="text"
            value={slug}
            onChange={(e) => handleSlugChange(e.target.value)}
            placeholder={t(($) => $.step_workspace.slug_placeholder)}
            className="border-0 bg-transparent font-mono shadow-none focus-visible:ring-0"
            onKeyDown={(e) => {
              if (isImeComposing(e)) return;
              if (e.key === "Enter") handleCreate();
            }}
          />
        </div>
        {slugError ? <FieldError>{slugError}</FieldError> : null}
      </Field>
      {/* 可编辑、从 slug 预填。输入框做得窄——值上限 10 字符，通栏输入框反而
          像是个错误。

          slug 为空时不臆造任何东西：罗马化不出字符的名字——日文假名、韩文、
          emoji——派生不出 slug（见 nameToWorkspaceSlug），旧版会用 "WS" 占位
          补这个空，等于告诉用户「你会拿到这次改动想消灭的默认前缀」。空字段 +
          一行提示才是诚实的形态；反正用户下一步就要填 URL，填了前缀立刻出现。 */}
      <Field>
        <FieldLabel htmlFor="ws-issue-prefix">
          {t(($) => $.step_workspace.issue_prefix_label)}
        </FieldLabel>
        <Input
          id="ws-issue-prefix"
          type="text"
          value={prefix}
          onChange={(e) => handlePrefixChange(e.target.value)}
          placeholder={derivedPrefix}
          autoComplete="off"
          autoCapitalize="characters"
          spellCheck={false}
          maxLength={10}
          className="w-32 font-mono uppercase"
          onKeyDown={(e) => {
            if (isImeComposing(e)) return;
            if (e.key === "Enter") handleCreate();
          }}
        />
        <FieldDescription>
          {effectivePrefix ? (
            <>
              {t(($) => $.step_workspace.issue_prefix_prefix)}
              <span className="font-mono text-foreground">
                {effectivePrefix}-123
              </span>
              {t(($) => $.step_workspace.issue_prefix_suffix)}
            </>
          ) : (
            t(($) => $.step_workspace.issue_prefix_pending)
          )}
        </FieldDescription>
      </Field>
    </FieldGroup>
  );

  return (
    <>
      <div className="flex flex-col gap-8 pt-2 sm:pt-6">
        {/* 眉题和别的眉题一起删了，但它在禁用状态下的措辞没丢：「工作区创建已被
            禁用」是下面禁用提示出现前、本屏唯一说明这件事的地方，所以把这个变体
            折进了标题自己的文案里。 */}
        <StepHeading
          title={
            reusing
              ? workspaceCreationAllowed
                ? t(($) => $.step_workspace.headline_resume, { name: reusing.name })
                : t(($) => $.step_workspace.creation_disabled_headline_resume, { name: reusing.name })
              : workspaceCreationAllowed
                ? t(($) => $.step_workspace.headline_first)
                : t(($) => $.step_workspace.creation_disabled_headline)
          }
          description={
            reusing
              ? workspaceCreationAllowed
                ? t(($) => $.step_workspace.lede_resume)
                : t(($) => $.step_workspace.creation_disabled_lede_resume)
              : workspaceCreationAllowed
                ? t(($) => $.step_workspace.lede_first)
                : t(($) => $.step_workspace.creation_disabled_lede)
          }
        />

        <div>
          {reusing ? (
            <div className="flex flex-col gap-3">
              <ExistingWorkspaceCard
                workspace={reusing}
                selected={mode === "existing"}
                onSelect={pickExisting}
              />
              {/* 自托管闸门（DISABLE_WORKSPACE_CREATION）开启时整张「新建」卡都
                  不渲染（#3433）——否则后端会对 POST 回 403，用户只会对着一张
                  没用的表单干瞪眼。 */}
              {!workspaceCreationDisabled && (
                <CreateNewWorkspaceCard
                  selected={mode === "create"}
                  onSelect={pickCreate}
                >
                  {createFields}
                </CreateNewWorkspaceCard>
              )}
            </div>
          ) : workspaceCreationDisabled ? (
            <CreationDisabledNotice onLogout={logout} />
          ) : (
            createFields
          )}
        </div>

      </div>

      {!(workspaceCreationDisabled && !reusing) && (
        <StepFooter hint={hint}>
          <Button
            className="w-full"
            disabled={continueDisabled}
            onClick={onContinue}
          >
            {continueLabel}
          </Button>
        </StepFooter>
      )}
    </>
  );
}

/**
 * 操作者把 DISABLE_WORKSPACE_CREATION 设为 true（#3433）、且用户还没有任何
 * 已有工作区时，在引导步骤里显示的提示。上方标题/引导语已经承担了说明文案，
 * 这个组件只提供「退出登录」这条逃生通道，让没拿到邀请就落在这里的用户不至于
 * 被困住。
 */
function CreationDisabledNotice({ onLogout }: { onLogout: () => void }) {
  const { t } = useT("onboarding");
  return (
    <div className="flex flex-col gap-3">
      <Button variant="outline" size="lg" onClick={onLogout}>
        {t(($) => $.step_workspace.creation_disabled_logout)}
      </Button>
    </div>
  );
}

/** 已有工作区选择卡（续用路径）：单选行，展示头像、名字和完整 URL。 */
function ExistingWorkspaceCard({
  workspace,
  selected,
  onSelect,
}: {
  workspace: Workspace;
  selected: boolean;
  onSelect: () => void;
}) {
  const urlHost = workspaceUrlHost(useConfigStore((s) => s.daemonAppUrl));
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={cn(
        "flex w-full items-center gap-4 rounded-lg border bg-card px-5 py-4 text-left transition-all",
        selected
          ? "border-foreground shadow-[inset_0_0_0_1px_var(--color-foreground)]"
          : "hover:border-foreground/20 hover:bg-accent/30",
      )}
    >
      <WorkspaceAvatar name={workspace.name} avatarUrl={workspace.avatar_url} size="lg" />
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="truncate text-body font-medium text-foreground">
          {workspace.name}
        </div>
        <div className="truncate font-mono text-caption text-muted-foreground">
          {`${urlHost}/${workspace.slug}`}
        </div>
      </div>
      <RadioMark selected={selected} />
    </button>
  );
}

/**
 * 可折叠的「创建新工作区」单选卡——在续用路径里和「已有工作区」卡并排展示。
 * 点头部切换选中；选中时展开露出 name / slug 字段（由调用方以 children 传入）。
 * 提交由父组件的页脚 CTA 驱动，而不是卡内的按钮。
 */
function CreateNewWorkspaceCard({
  selected,
  onSelect,
  children,
}: {
  selected: boolean;
  onSelect: () => void;
  children: ReactNode;
}) {
  const { t } = useT("onboarding");
  return (
    <div
      className={cn(
        "overflow-hidden rounded-lg border bg-card transition-all",
        selected
          ? "border-foreground shadow-[inset_0_0_0_1px_var(--color-foreground)]"
          : "hover:border-foreground/20",
      )}
    >
      <button
        type="button"
        role="radio"
        aria-checked={selected}
        aria-expanded={selected}
        onClick={onSelect}
        className="flex w-full items-center gap-4 px-5 py-4 text-left"
      >
        <div
          aria-hidden
          className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground"
        >
          <Plus className="h-4 w-4" />
        </div>
        <div className="flex min-w-0 flex-1 flex-col">
          <div className="truncate text-body font-medium text-foreground">
            {t(($) => $.step_workspace.create_new_title)}
          </div>
          <div className="truncate text-caption text-muted-foreground">
            {t(($) => $.step_workspace.create_new_subtitle)}
          </div>
        </div>
        <RadioMark selected={selected} />
      </button>
      {selected && <div className="border-t px-5 py-5">{children}</div>}
    </div>
  );
}
