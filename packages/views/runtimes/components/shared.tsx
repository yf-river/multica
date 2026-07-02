import { Wifi, WifiHigh, WifiOff } from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import type { RuntimeHealth } from "@multica/core/runtimes";
import { useT } from "../../i18n";

// Maps each derived 4-state runtime health to a semantic colour class.
// Labels flow through useT — see useHealthLabel below.
const HEALTH_VISUAL: Record<RuntimeHealth, { dot: string; tone: string }> = {
  online: { dot: "bg-success", tone: "bg-success/10 text-success" },
  recently_lost: { dot: "bg-warning", tone: "bg-warning/10 text-warning" },
  offline: { dot: "bg-muted-foreground/40", tone: "bg-muted text-muted-foreground" },
  about_to_gc: { dot: "bg-destructive", tone: "bg-destructive/10 text-destructive" },
};

export function HealthDot({
  health,
  className = "",
}: {
  health: RuntimeHealth | "loading";
  className?: string;
}) {
  if (health === "loading") {
    return (
      <span
        className={`inline-block h-2 w-2 rounded-full bg-muted ${className}`}
      />
    );
  }
  return (
    <span
      className={`inline-block h-2 w-2 rounded-full ${HEALTH_VISUAL[health].dot} ${className}`}
    />
  );
}

// Wifi-style runtime health indicator. The icon shape carries the rough
// state ("can it talk to us?") and the colour carries severity. Used
// wherever a richer signal than the bare dot is appropriate (agent
// hover-card runtime row, runtime list health column).
//
//   online        → Wifi (full bars, success)
//   recently_lost → WifiHigh (fewer bars, warning) — transient hiccup
//   offline       → WifiOff (slashed, muted) — long unreachable
//   about_to_gc   → WifiOff (slashed, destructive) — sweeper coming
const HEALTH_ICON: Record<
  RuntimeHealth,
  { Icon: typeof Wifi; tone: string }
> = {
  online: { Icon: Wifi, tone: "text-success" },
  recently_lost: { Icon: WifiHigh, tone: "text-warning" },
  offline: { Icon: WifiOff, tone: "text-muted-foreground" },
  about_to_gc: { Icon: WifiOff, tone: "text-destructive" },
};

export function HealthIcon({
  health,
  className = "h-3 w-3",
}: {
  health: RuntimeHealth | "loading";
  className?: string;
}) {
  if (health === "loading") {
    return <Wifi className={`${className} text-muted-foreground/40`} />;
  }
  const { Icon, tone } = HEALTH_ICON[health];
  return <Icon className={`${className} ${tone}`} />;
}

// Hook form: usable inside React components (preferred for new call sites
// that aren't running in non-component contexts).
export function useHealthLabel(): (health: RuntimeHealth | "loading") => string {
  const { t } = useT("runtimes");
  return (health) => {
    if (health === "loading") return "—";
    return t(($) => $.health[health].label);
  };
}

export function HealthBadge({
  health,
}: {
  health: RuntimeHealth | "loading";
}) {
  const labelOf = useHealthLabel();
  if (health === "loading") {
    return (
      <Badge variant="secondary" className="bg-muted text-muted-foreground">
        —
      </Badge>
    );
  }
  const v = HEALTH_VISUAL[health];
  return (
    <Badge variant="secondary" className={v.tone}>
      <span className={`h-1.5 w-1.5 rounded-full ${v.dot}`} />
      {labelOf(health)}
    </Badge>
  );
}

// KPI tile used in the Runtime detail "story numbers" row. The big number
// is the visual anchor of the whole left column — sized large enough that
// it dominates over the chart hierarchy below it. Label sits as a small
// caps eyebrow; hint is a thin caption beneath the number for deltas /
// ratios / savings context.
export function KpiCard({
  label,
  value,
  hint,
  accent,
}: {
  label: string;
  value: string;
  hint?: React.ReactNode;
  accent?: "brand" | "success" | "default";
}) {
  const valueClass =
    accent === "brand"
      ? "text-brand"
      : accent === "success"
        ? "text-success"
        : "";
  return (
    <div className="flex flex-col gap-2 p-5">
      <div className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </div>
      <div className={`text-3xl font-semibold leading-none tabular-nums ${valueClass}`}>
        {value}
      </div>
      {hint != null && (
        <div className="text-xs text-muted-foreground">{hint}</div>
      )}
    </div>
  );
}
