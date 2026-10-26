// 本文件提供设置页的通用布局组件：标签页容器、分区、卡片、行，以及保存状态指示。

import type { ReactNode } from "react";
import { AlertCircle, Check, Loader2 } from "lucide-react";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { cn } from "@multica/ui/lib/utils";

/** 设置项自动保存的四种状态：空闲、保存中、已保存、出错。 */
export type SettingsSaveStatus = "idle" | "saving" | "saved" | "error";

/**
 * 单个设置标签页的容器：顶部放标题和描述，下面是具体内容。
 * @param props.title - 标签页标题。
 * @param props.description - 可选，标题下的说明文字。
 * @param props.children - 标签页主体内容。
 */
export function SettingsTab({
  title,
  description,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="space-y-8">
      <header>
        <h2 className="text-title-lg font-semibold tracking-tight">{title}</h2>
        {description ? (
          <p className="mt-1 max-w-2xl text-body leading-6 text-muted-foreground">
            {description}
          </p>
        ) : null}
      </header>
      {children}
    </div>
  );
}

/**
 * 设置卡片内的一个分区，包含可选的标题、说明、右侧操作按钮和内容。
 * @param props.title - 可选，分区标题。
 * @param props.description - 可选，分区说明。
 * @param props.action - 可选，右侧操作区（如按钮）。
 * @param props.children - 分区内容。
 * @param props.className - 可选，附加到 section 上的 CSS 类名。
 */
export function SettingsSection({
  title,
  description,
  action,
  children,
  className,
}: {
  title?: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("space-y-3", className)}>
      {title || description || action ? (
        <div className="flex min-w-0 items-end justify-between gap-4 px-0.5">
          <div className="min-w-0">
            {title ? <h3 className="text-body font-semibold">{title}</h3> : null}
            {description ? (
              <p className="mt-1 text-caption leading-5 text-muted-foreground">
                {description}
              </p>
            ) : null}
          </div>
          {action ? <div className="shrink-0">{action}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}

/**
 * 设置页使用的卡片容器，内部会用分割线把各行隔开。
 * @param props.children - 卡片内容，通常是一组 SettingsRow。
 * @param props.className - 可选，附加到 Card 上的 CSS 类名。
 */
export function SettingsCard({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <Card className={cn("gap-0 py-0 shadow-none", className)}>
      <CardContent className="divide-y divide-surface-border px-0">
        {children}
      </CardContent>
    </Card>
  );
}

/**
 * 卡片内的分组小标题。用于需要在一张卡片里镜像侧边栏分区的地方（工作区
 * 菜单），避免为每个小分组再套一张卡片。
 */
export function SettingsGroupLabel({ children }: { children: ReactNode }) {
  return (
    <h4 className="px-4 pt-4 pb-1 text-caption font-medium text-muted-foreground">
      {children}
    </h4>
  );
}

/**
 * 控件列宽度档位。同一张卡片内，所有文本输入控件共用 `text` 档以保证边缘对齐；
 * 只有当字段本身就短（如代码、枚举下拉）时才降到更小的档位——这种差异必须是
 * 有意为之。请选择档位，而不是给每一行临时加宽度。
 */
const SETTINGS_CONTROL_WIDTHS = {
  /** 文本输入框和文本域——标准控件列宽度。 */
  text: "sm:w-96",
  /** 选项文案较长的下拉/选择器（时区、模型）。 */
  "select-wide": "sm:w-72",
  /** 紧凑的枚举下拉（主题、语言）。 */
  select: "sm:w-48",
  /** 短小的固定格式代码（issue 前缀）。 */
  code: "sm:w-40",
  /** 不限制——头像上传这类非输入内容。 */
  none: "sm:max-w-none",
} as const;

/** 控件列宽度档位的键名，供 SettingsRow 的 size 属性使用。 */
export type SettingsControlSize = keyof typeof SETTINGS_CONTROL_WIDTHS;

/**
 * 设置卡片里的一行：左侧是标签和说明，右侧是控件。
 * @param props.label - 行标签。
 * @param props.description - 可选，标签下方的说明文字。
 * @param props.children - 右侧控件内容。
 * @param props.className - 可选，附加到行容器上的 CSS 类名。
 * @param props.size - 可选，控件列宽度档位；按钮、开关这类自适应控件可省略。
 * @param props.align - 控件与标签的垂直对齐方式，默认居中。
 */
export function SettingsRow({
  label,
  description,
  children,
  className,
  size,
  align = "center",
}: {
  label: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  className?: string;
  /** 控件列宽度档位；按钮、开关这类自适应内容可省略。 */
  size?: SettingsControlSize;
  align?: "center" | "start";
}) {
  return (
    <div
      className={cn(
        "flex min-h-16 flex-col gap-3 px-4 py-3.5 sm:flex-row sm:justify-between sm:gap-8",
        align === "center" ? "sm:items-center" : "sm:items-start",
        className,
      )}
    >
      <div className="min-w-0 flex-1">
        <div className="text-body font-medium">{label}</div>
        {description ? (
          <div className="mt-0.5 text-caption leading-5 text-muted-foreground">
            {description}
          </div>
        ) : null}
      </div>
      <div
        className={cn(
          "w-full shrink-0 sm:w-auto sm:max-w-[56%]",
          size ? SETTINGS_CONTROL_WIDTHS[size] : undefined,
        )}
      >
        {children}
      </div>
    </div>
  );
}

/**
 * 显示设置项的保存状态（转圈/成功/失败），空闲时什么都不渲染。
 * @param props.status - 当前保存状态。
 * @param props.savingLabel - 保存中提示文字。
 * @param props.savedLabel - 已保存提示文字。
 * @param props.errorLabel - 出错提示文字。
 */
export function SettingsSaveState({
  status,
  savingLabel,
  savedLabel,
  errorLabel,
}: {
  status: SettingsSaveStatus;
  savingLabel: string;
  savedLabel: string;
  errorLabel: string;
}) {
  if (status === "idle") return null;

  const content =
    status === "saving" ? (
      <>
        <Loader2 className="size-3 animate-spin" />
        {savingLabel}
      </>
    ) : status === "saved" ? (
      <>
        <Check className="size-3 text-success" />
        {savedLabel}
      </>
    ) : (
      <>
        <AlertCircle className="size-3 text-destructive" />
        {errorLabel}
      </>
    );

  return (
    <span
      role="status"
      className={cn(
        "inline-flex items-center gap-1.5 text-caption text-muted-foreground",
        status === "error" && "text-destructive",
      )}
    >
      {content}
    </span>
  );
}
