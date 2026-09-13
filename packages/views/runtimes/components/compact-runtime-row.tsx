// ============================================================================
// 单行可选的 runtime 卡片（CompactRuntimeRow）
//
// 角色定位：在「把 Mika 指派给某台 runtime」的引导场景（Step 3 的 Web CLI
// 展开面板）里，把一台 runtime 渲染成一行可点击的候选：厂商 logo + 名称 +
// 厂商名，右侧一个小圆点表示在线/离线。点击整行即选中。
//
// 约定：本组件刻意保持无状态——选中态由调用方驱动，这样 StepPlatformFork
// 与将来任何内嵌方都能共用它，不必各自复制一套勾选逻辑。
// ============================================================================
import { cn } from "@multica/ui/lib/utils";
import type { AgentRuntime } from "@multica/core/types";
import { runtimeDisplayName } from "@multica/core/runtimes";
import { ProviderLogo } from "./provider-logo";
import { useT } from "../../i18n";

/**
 * 单行 runtime 候选：logo + 名称 + 在线小圆点。选中态由调用方传入。
 *
 * @param runtime - 要展示/选择的 runtime。
 * @param selected - 是否处于选中态（驱动边框高亮与 aria-pressed）。
 * @param onSelect - 用户点选整行时的回调。
 * @param disabled - 提交进行中置灰，禁止在提交期间改选。
 */
export function CompactRuntimeRow({
  runtime,
  selected,
  onSelect,
  disabled = false,
}: {
  runtime: AgentRuntime;
  selected: boolean;
  onSelect: () => void;
  /** 提交请求在途时置 true：选择不能在提交中途被换掉。 */
  disabled?: boolean;
}) {
  const { t: tAgents } = useT("agents");
  const online = runtime.status === "online";
  return (
    // 用真正的 <button> 而不是 role="button" 的 div：disabled、焦点态、
    // Enter/Space 键行为全部白拿。替换掉的手写 keydown 分支完全不认 disabled，
    // 导致成员能在提交中途换 runtime，最后把 Mika 挂到他其实没选中的那台机器上。
    <button
      type="button"
      onClick={onSelect}
      disabled={disabled}
      aria-pressed={selected}
      className={cn(
        "flex w-full flex-row items-center gap-3 rounded-lg border bg-card p-4 text-left transition-colors",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        "disabled:pointer-events-none disabled:opacity-60",
        selected
          ? "border-primary ring-1 ring-primary"
          : "hover:border-foreground/20",
      )}
    >
      <ProviderLogo provider={runtime.provider} className="h-5 w-5" />
      <div className="min-w-0 flex-1">
        <div className="truncate text-body font-medium">
          {runtimeDisplayName(runtime)}
        </div>
        <div className="text-caption text-muted-foreground">{runtime.provider}</div>
      </div>
      <span
        className={cn(
          "h-2 w-2 shrink-0 rounded-full",
          online ? "bg-success" : "bg-muted-foreground/40",
        )}
        aria-label={online ? tAgents(($) => $.availability.online) : tAgents(($) => $.availability.offline)}
      />
    </button>
  );
}
