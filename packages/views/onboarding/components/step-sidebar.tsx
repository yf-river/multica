"use client";

// 引导流程的常驻进度侧栏（窄屏时由顶部进度条 StepProgressBar 替代）：展示当前
// 位于哪一步、支持返回上一步或点选回到已完成的步骤。
import type { CSSProperties, ReactNode } from "react";
import { ArrowLeft, Check } from "lucide-react";
import {
  ONBOARDING_STEP_ORDER,
  type OnboardingStep,
} from "@multica/core/onboarding";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { DotSphere } from "@multica/ui/components/ui/dot-sphere";
import {
  Stepper,
  StepperDescription,
  StepperIndicator,
  StepperItem,
  StepperNav,
  StepperSeparator,
  StepperTitle,
} from "@multica/ui/components/ui/stepper";
import { useT } from "../../i18n";

/**
 * 进度栏的窄屏版：在窄到放不下侧栏的宽度下，仍然回答「我在哪、怎么返回」。
 * 只在 `md` 以下渲染——此时 StepSidebar 处于隐藏状态。
 *
 * 用「几段进度条 + 当前步名称」而不是重复整份步骤列表：375px 宽度下的目标是
 * 尽量少占纵向空间，而列表本来要传达的也就是「当前是哪一步、走到第几步」。
 * 文案复用同一个 `step_nav` 词条，翻译只此一处。
 *
 * @param currentStep 当前步骤
 * @param onBack 返回上一步的回调
 * @param backDisabled 是否禁用返回
 * @param footer 同侧栏页脚位置的插槽——「退出登录」出口。这里必须重复放一份
 *   而不能只靠侧栏：侧栏在 md 以下会被隐藏，若不重复，除 Welcome 之外的每一步
 *   都会失去退出通道
 */
export function StepProgressBar({
  currentStep,
  onBack,
  backDisabled,
  footer,
}: {
  currentStep: OnboardingStep;
  onBack?: () => void;
  backDisabled?: boolean;
  /** 与侧栏页脚同槽位的「退出登录」出口。这里必须重复放一份而不能只靠侧栏：
   *  这些宽度下侧栏是隐藏的，不重复的话除 Welcome 外每步都会失去退出通道。 */
  footer?: ReactNode;
}) {
  const { t } = useT("onboarding");
  const currentIndex = Math.max(0, ONBOARDING_STEP_ORDER.indexOf(currentStep));
  const key = ONBOARDING_STEP_ORDER[currentIndex] as Exclude<
    OnboardingStep,
    "welcome"
  >;

  return (
    <div className="mb-6 flex items-center gap-3 md:hidden">
      {onBack ? (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onBack}
          disabled={backDisabled}
          aria-label={t(($) => $.common.back)}
          className="-ml-2 shrink-0"
          style={{ WebkitAppRegion: "no-drag" } as CSSProperties}
        >
          <ArrowLeft />
        </Button>
      ) : null}
      <span
        aria-hidden
        className="flex flex-1 items-center gap-1.5"
      >
        {ONBOARDING_STEP_ORDER.map((stepId, index) => (
          <span
            key={stepId}
            className={cn(
              "h-1 flex-1 rounded-full transition-colors",
              index <= currentIndex ? "bg-foreground" : "bg-border",
            )}
          />
        ))}
      </span>
      <span className="min-w-0 truncate text-caption font-medium text-muted-foreground">
        {t(($) => $.step_nav[key].label)}
      </span>
      {footer ? <span className="shrink-0">{footer}</span> : null}
    </div>
  );
}

/**
 * 常驻进度侧栏：告诉用户引导走到哪一步、以及如何往回退。
 *
 * 它取代了原来一条只带「第 2 / 3 步」计数、别无一物的横向圆点行。计数器只告诉
 * 用户还剩多少，却从不说接下来是什么，于是每一步都像是「突然出现」的。
 *
 * 结构上照着 ReUI onboarding-3 区块做：一块内嵌面板，顶部放品牌标识，步骤列表
 * 在剩余高度中居中，底部再放一行页脚——而不是把所有东西挤在上边缘的扁平侧栏。
 *
 * 有三点值得知道：
 *
 *   - **面板上用 `dark` 而不是反色工具类。** `.dark` 在我们 tokens 表里只是一个
 *     普通类选择器，作用域限定在这里后只会重定义这一子树的颜色变量，
 *     `text-muted-foreground` 因此在深色表面上取到正确的值。初版在这里手拼
 *     `text-background/60` 的灰阶，这正是「退出登录」按钮最终近不可见的原因。
 *   - **它不是 tablist。** 移植来的 stepper 默认带 `role="tablist"`，每个触发器
 *     用 `aria-controls` 关联一个面板 id——对自带面板的 stepper 是正确语义，
 *     但这里面板是按路由切换的步骤，那些 id 会悬空。所以这里只用纯展示插槽，
 *     改用 `aria-current="step"` 标注当前位置。
 *   - **只有已完成步骤可点击。** 向前跳转必须逐步骤跑过该校验和提交逻辑，从进度
 *     栏直接跳到后面会跳过它们；往回退则始终安全。
 *
 * @param currentStep 当前步骤
 * @param onBack 返回上一步的回调
 * @param backDisabled 是否禁用返回
 * @param onStepChange 回到某个已完成步骤的回调。由流程注入（它掌握步骤顺序）；
 *   缺省表示侧栏仅作展示、不可跳转
 * @param footer 由流程注入的「退出登录」出口。做成插槽而非直接渲染，让本组件
 *   保持纯展示：若就地渲染退出登录的变更逻辑，就得往五个步骤测试文件里塞
 *   QueryClient
 */
export function StepSidebar({
  currentStep,
  onBack,
  backDisabled,
  onStepChange,
  footer,
}: {
  currentStep: OnboardingStep;
  onBack?: () => void;
  /** 工作区步骤在创建请求进行中会禁用「返回」。 */
  backDisabled?: boolean;
  /** 回到某个已完成步骤。由流程注入（它掌握步骤顺序）；缺省表示进度栏只展示。 */
  onStepChange?: (step: OnboardingStep) => void;
  /** 由流程注入的「退出登录」出口。做成插槽而不是直接渲染，让这里保持纯展示：
   *  直接渲染退出登录的变更逻辑，会让五个步骤测试文件被迫引入 QueryClient。 */
  footer?: ReactNode;
}) {
  const { t } = useT("onboarding");
  const currentIndex = Math.max(0, ONBOARDING_STEP_ORDER.indexOf(currentStep));

  return (
    // md 以下隐藏：侧栏面板从未窄于 15rem，而内容区还留着 3rem 的 gutter，
    // 375px 宽的浏览器最后只剩约 87px 给表单。StepProgressBar 在这个宽度下承担
    // 同样的信息——连同本头部里的「返回」按钮。
    <aside className="hidden w-[15rem] shrink-0 p-2 md:block md:w-[19rem] md:p-3 lg:w-[22rem] lg:p-4">
      <div className="dark relative isolate flex h-full w-full flex-col overflow-hidden rounded-2xl bg-background px-5 pb-5 text-foreground ring-1 ring-border">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 bg-background"
        >
          <DotSphere
            dotGap={19}
            motion="wave"
            sphereCount={5}
            sphereRadius="20%"
            dotRadiusMax={1.9}
            speed={0.4}
          />
        </div>

        <div className="relative flex min-h-0 flex-1 flex-col pt-5">
          <header className="flex min-h-9 shrink-0 items-center justify-between gap-3">
            <span className="flex min-w-0 items-center gap-2">
              <MulticaIcon className="size-5 shrink-0 text-foreground" noSpin />
              <span className="truncate text-label font-medium text-foreground">
                {t(($) => $.step_nav.wordmark)}
              </span>
            </span>
            {onBack ? (
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                onClick={onBack}
                disabled={backDisabled}
                aria-label={t(($) => $.common.back)}
                style={{ WebkitAppRegion: "no-drag" } as CSSProperties}
              >
                <ArrowLeft />
              </Button>
            ) : null}
          </header>

          <div className="flex min-h-0 flex-1 items-center justify-center py-10">
            <Stepper
              value={currentIndex + 1}
              orientation="vertical"
              role="group"
              aria-label={t(($) => $.step_nav.label)}
              className="flex w-full flex-col items-start justify-center gap-0"
            >
              <StepperNav className="w-full">
                {ONBOARDING_STEP_ORDER.map((stepId, index) => {
                  const isDone = index < currentIndex;
                  const isCurrent = index === currentIndex;
                  const isLast = index === ONBOARDING_STEP_ORDER.length - 1;
                  const canReturn = isDone && !!onStepChange && !backDisabled;
                  const key = stepId as Exclude<OnboardingStep, "welcome">;

                  const body = (
                    <>
                      <StepperIndicator
                        className={cn(
                          "mt-0.5 size-4 shrink-0 border-0 bg-transparent ring-1 transition-colors",
                          isDone
                            ? "bg-foreground text-background ring-foreground"
                            : isCurrent
                              ? "text-transparent ring-muted-foreground"
                              : "text-transparent ring-border",
                        )}
                      >
                        {isDone ? (
                          <Check aria-hidden className="size-3" />
                        ) : isCurrent ? (
                          <span
                            aria-hidden
                            className="block size-1.5 rounded-full bg-foreground"
                          />
                        ) : (
                          <span className="sr-only">{index + 1}</span>
                        )}
                      </StepperIndicator>
                      <div className="min-w-0 flex-1 text-left">
                        <StepperTitle
                          className={cn(
                            "transition-colors",
                            isCurrent || isDone
                              ? "text-foreground"
                              : "text-muted-foreground",
                          )}
                        >
                          {t(($) => $.step_nav[key].label)}
                        </StepperTitle>
                        <StepperDescription className="mt-0.5 max-w-none text-muted-foreground">
                          {t(($) => $.step_nav[key].description)}
                        </StepperDescription>
                      </div>
                    </>
                  );

                  return (
                    <StepperItem
                      key={stepId}
                      step={index + 1}
                      completed={isDone}
                      className="relative w-full items-start not-last:flex-1"
                      {...(isCurrent
                        ? { "aria-current": "step" as const }
                        : {})}
                    >
                      {canReturn ? (
                        <button
                          type="button"
                          onClick={() => onStepChange(stepId)}
                          className="flex w-full items-start gap-3 rounded-md pb-6 text-left transition-opacity hover:opacity-80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          {body}
                        </button>
                      ) : (
                        <div
                          className={cn(
                            "flex w-full items-start gap-3 text-left",
                            !isLast && "pb-6",
                          )}
                        >
                          {body}
                        </div>
                      )}

                      {/* 每行后面是一条贯穿始终的连续细线，而不是行与行之间的
                          短线，这样整条轨道读起来是一条线。用 -order-1 把它放到
                          内容下层。 */}
                      {!isLast ? (
                        <StepperSeparator
                          className={cn(
                            "absolute left-2 top-6 -order-1 m-0 w-px -translate-x-1/2",
                            // StepperSeparator 对竖向导航硬编码了 3rem 高度，
                            // 超出本行会直接把线画穿下一个指示器。这里复用它
                            // 自己的 variant，让 tailwind-merge 丢弃它的高度
                            // 类，就不用再写 !important。
                            "group-data-[orientation=vertical]/stepper-nav:h-[calc(100%-1.75rem)]",
                            isDone ? "bg-muted-foreground" : "bg-border",
                          )}
                        />
                      ) : null}
                    </StepperItem>
                  );
                })}
              </StepperNav>
            </Stepper>
          </div>

          {footer ? (
            <footer className="flex min-h-8 shrink-0 items-end justify-between gap-4">
              {footer}
            </footer>
          ) : null}
        </div>
      </div>
    </aside>
  );
}
