"use client";

// 引导第 1 步「关于你」：同一屏上问「你的角色」（单选）和「主要用途」（多选）。
// 两组都可跳过，答案用于产品个性化与引导统计，与 Agent 创建相互独立。
import {
  Brain,
  Briefcase,
  Code2,
  Compass,
  FileEdit,
  GraduationCap,
  ListChecks,
  Megaphone,
  MoreHorizontal,
  Palette,
  PenLine,
  Rocket,
  Search,
  Settings2,
  User,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import type { QuestionnaireAnswers, Role, UseCase } from "@multica/core/onboarding";
import {
  StepFooter,
  StepHeading,
} from "../components/step-shell";
import {
  IconOptionCard,
  IconOtherOptionCard,
  type QuestionOption,
} from "../components/icon-option-card";
import { useT } from "../../i18n";

/**
 * 第 1 步——「关于你」：角色（单选）和用途（多选）放在同一屏。它们曾经是两步，
 * 但共用一个眉题、又各自撑不起一整屏，合并后引导进度条从五步缩成三步。答案
 * 对产品个性化与引导统计仍然有用，和 Agent 的创建无关。
 *
 * 两组都允许不答：
 *   - 只要任一组给出了有效答案，「继续」就可用（选「其他」必须等它的自由文本
 *     非空才算）。点继续时，没答的那组会被盖上 `*_skipped` 标记——用户看过这道
 *     题然后继续走，这正是下游跳过标记的含义（统计 + 不再追问）。
 *   - Skip 一次拒绝两组。
 *
 * 每次选择变化都会立刻通过 `onChange` 发出 PATCH（由外层 `applyAnswers` 负责
 * 不等待的持久化），所以再次进入时会预填这两组。
 *
 * @param answers 当前问卷答案
 * @param onChange 局部答案变更回调（立即持久化）
 * @param onAdvance 继续到下一步的回调
 * @param onSkip 跳过本步的回调
 */
export function StepAboutYou({
  answers,
  onChange,
  onAdvance,
  onSkip,
}: {
  answers: QuestionnaireAnswers;
  onChange: (patch: Partial<QuestionnaireAnswers>) => void;
  onAdvance: () => void;
  onSkip: () => void;
}) {
  const { t } = useT("onboarding");

  const roleOptions: QuestionOption[] = [
    { slug: "engineer", icon: <Code2 className="h-4 w-4" />, label: t(($) => $.questions.role.engineer) },
    { slug: "product", icon: <Briefcase className="h-4 w-4" />, label: t(($) => $.questions.role.product) },
    { slug: "designer", icon: <Palette className="h-4 w-4" />, label: t(($) => $.questions.role.designer) },
    { slug: "founder", icon: <Rocket className="h-4 w-4" />, label: t(($) => $.questions.role.founder) },
    { slug: "marketing", icon: <Megaphone className="h-4 w-4" />, label: t(($) => $.questions.role.marketing) },
    { slug: "writer", icon: <PenLine className="h-4 w-4" />, label: t(($) => $.questions.role.writer) },
    { slug: "research", icon: <Search className="h-4 w-4" />, label: t(($) => $.questions.role.research) },
    { slug: "ops", icon: <Settings2 className="h-4 w-4" />, label: t(($) => $.questions.role.ops) },
    { slug: "student", icon: <GraduationCap className="h-4 w-4" />, label: t(($) => $.questions.role.student) },
    { slug: "other", icon: <MoreHorizontal className="h-4 w-4" />, label: t(($) => $.questions.role.other), isOther: true },
  ];

  const useCaseOptions: QuestionOption[] = [
    { slug: "ship_code", icon: <Code2 className="h-4 w-4" />, label: t(($) => $.questions.use_case.ship_code) },
    { slug: "manage_team", icon: <ListChecks className="h-4 w-4" />, label: t(($) => $.questions.use_case.manage_team) },
    { slug: "personal_tasks", icon: <User className="h-4 w-4" />, label: t(($) => $.questions.use_case.personal_tasks) },
    { slug: "plan_research", icon: <Brain className="h-4 w-4" />, label: t(($) => $.questions.use_case.plan_research) },
    { slug: "write_publish", icon: <FileEdit className="h-4 w-4" />, label: t(($) => $.questions.use_case.write_publish) },
    { slug: "automate_ops", icon: <Settings2 className="h-4 w-4" />, label: t(($) => $.questions.use_case.automate_ops) },
    { slug: "evaluate", icon: <Compass className="h-4 w-4" />, label: t(($) => $.questions.use_case.evaluate) },
    { slug: "other", icon: <MoreHorizontal className="h-4 w-4" />, label: t(($) => $.questions.use_case.other), isOther: true },
  ];

  // 角色保持单选——下游个性化要的是一个「主身份」，而不是混合体。
  const roleSelected: readonly string[] = answers.role ? [answers.role] : [];
  const roleOtherFilled = (answers.role_other ?? "").trim().length > 0;
  const roleAnswered =
    answers.role !== null && (answers.role !== "other" || roleOtherFilled);

  const useCaseSlugs = answers.use_case ?? [];
  const useCaseOtherFilled =
    (answers.use_case_other ?? "").trim().length > 0;
  const useCaseHasNonOther = useCaseSlugs.some((s) => s !== "other");
  // 沿用旧版逐步规则：孤零零一个「其他」要等它的自由文本有内容才算有效答案。
  const useCaseAnswered =
    useCaseSlugs.length > 0 && (useCaseHasNonOther || useCaseOtherFilled);

  const canContinue = roleAnswered || useCaseAnswered;

  const pickRole = (slug: string) => {
    if (slug === "other") {
      onChange({ role: "other", role_skipped: false });
      return;
    }
    onChange({ role: slug as Role, role_other: null, role_skipped: false });
  };

  const toggleUseCase = (slug: string) => {
    const current = answers.use_case ?? [];
    if (slug === "other") {
      if (current.includes("other")) {
        onChange({
          use_case: current.filter((s) => s !== "other"),
          use_case_other: null,
        });
      } else {
        onChange({ use_case: [...current, "other"], use_case_skipped: false });
      }
      return;
    }
    const typed = slug as UseCase;
    const next = current.includes(typed)
      ? current.filter((s) => s !== typed)
      : [...current, typed];
    onChange({ use_case: next, use_case_skipped: false });
  };

  const confirmAdvance = () => {
    if (!canContinue) return;
    // 用户看过但没作答的那组视为「放弃作答」——盖上跳过标记，让统计能看到这个
    // 决定，而不是把该槽位当成「从没到过」。顺便清掉悬空的、没有文字的
    // 「其他」角色选择。
    const patch: Partial<QuestionnaireAnswers> = {};
    if (!roleAnswered) {
      patch.role = null;
      patch.role_other = null;
      patch.role_skipped = true;
    }
    if (!useCaseAnswered) {
      patch.use_case = [];
      patch.use_case_other = null;
      patch.use_case_skipped = true;
    }
    if (Object.keys(patch).length > 0) onChange(patch);
    onAdvance();
  };

  const handleSkip = () => {
    onChange({
      role: null,
      role_other: null,
      role_skipped: true,
      use_case: [],
      use_case_other: null,
      use_case_skipped: true,
    });
    onSkip();
  };

  const footerHint = canContinue
    ? t(($) => $.step_question.hint_continue)
    : t(($) => $.step_question.hint_pick);

  return (
    <>
      <div className="flex flex-col gap-8 pt-2 sm:pt-6">
        <StepHeading title={t(($) => $.questions.about_you.question)} />

        <QuestionGroup
          question={t(($) => $.questions.role.question)}
          options={roleOptions}
          selectedSlugs={roleSelected}
          otherValue={answers.role_other ?? ""}
          onOtherChange={(v) => onChange({ role_other: v })}
          otherPlaceholder={t(($) => $.questions.role.other_placeholder)}
          onAnswer={pickRole}
          onConfirm={confirmAdvance}
        />

        <QuestionGroup
          question={t(($) => $.questions.use_case.question)}
          options={useCaseOptions}
          selectedSlugs={useCaseSlugs}
          otherValue={answers.use_case_other ?? ""}
          onOtherChange={(v) => onChange({ use_case_other: v })}
          otherPlaceholder={t(($) => $.questions.use_case.other_placeholder)}
          onAnswer={toggleUseCase}
          onConfirm={confirmAdvance}
          multiSelect
        />

      </div>

      <StepFooter hint={footerHint}>
        <Button className="w-full" disabled={!canContinue} onClick={confirmAdvance}>
          {t(($) => $.common.continue)}
        </Button>
        <Button variant="ghost" className="w-full" onClick={handleSkip}>
          {t(($) => $.common.skip)}
        </Button>
      </StepFooter>
    </>
  );
}

StepAboutYou.displayName = "StepAboutYou";

/**
 * 一道问卷题的一个分组：小题目标签 + 一排选项卡片。选择语义都在父组件的
 * handler 里，这里只做布局壳，让两个分组渲染得一模一样。
 */
function QuestionGroup({
  question,
  options,
  selectedSlugs,
  otherValue,
  onOtherChange,
  otherPlaceholder,
  onAnswer,
  onConfirm,
  multiSelect = false,
}: {
  question: string;
  options: readonly QuestionOption[];
  selectedSlugs: readonly string[];
  otherValue: string;
  onOtherChange: (value: string) => void;
  otherPlaceholder: string;
  onAnswer: (slug: string) => void;
  /** 在「其他」输入框里按 Enter——是否继续由父组件决定。 */
  onConfirm: () => void;
  multiSelect?: boolean;
}) {
  const otherOption = options.find((o) => o.isOther) ?? null;
  const otherSelected = otherOption
    ? selectedSlugs.includes(otherOption.slug)
    : false;

  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-label font-medium text-foreground">{question}</h2>
      <fieldset
        role={multiSelect ? "group" : "radiogroup"}
        aria-label={question}
        className="m-0 flex flex-row flex-wrap gap-2 p-0"
      >
        {options.map((option) =>
          option.isOther ? (
            <IconOtherOptionCard
              key={option.slug}
              icon={option.icon}
              label={option.label}
              selected={otherSelected}
              onSelect={() => onAnswer(option.slug)}
              otherValue={otherValue}
              onOtherChange={onOtherChange}
              onConfirm={onConfirm}
              placeholder={otherPlaceholder}
              mode={multiSelect ? "checkbox" : "radio"}
            />
          ) : (
            <IconOptionCard
              key={option.slug}
              icon={option.icon}
              label={option.label}
              selected={selectedSlugs.includes(option.slug)}
              onSelect={() => onAnswer(option.slug)}
              mode={multiSelect ? "checkbox" : "radio"}
            />
          ),
        )}
      </fieldset>
    </section>
  );
}
