// ============================================================================
// 本机 daemon CLI 的在线升级区块（UpdateSection）
//
// 角色定位：为「本机（local）模式的机器」提供 CLI 工具升级入口：先去 GitHub
// Releases 拉取最新版本号，对比本机当前版本，有新版时展示「版本号 → 新版号 +
// 更新按钮」；点击后调用后端 API 发起升级，并每 2 秒轮询一次升级状态（排队 /
// 运行中 / 完成 / 失败 / 超时），把进度与结果/错误文案渲染在行内。云 runtime
// 与只读观看者不显示任何升级入口。
//
// 数据流：本组件只在 machine-cli-section 里被使用，后者会传入当前版本号、
// 该机可用的命令通道 runtime id（或 null）与在线状态。版本对比使用纯数字
// 逐段比较，绝不把源码构建版本（git describe 串 / "dev"）误判成可升级。
// ============================================================================
import { useState, useEffect, useCallback, useRef } from "react";
import {
  Loader2,
  CheckCircle2,
  XCircle,
  ArrowUpCircle,
  Check,
  Lock,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { api } from "@multica/core/api";
import type { RuntimeUpdateStatus } from "@multica/core/types";
import { useT } from "../../i18n";

// GitHub Releases 的「最新发布」接口。daemon 版本以 GitHub tag 为准。
const GITHUB_RELEASES_URL =
  "https://api.github.com/repos/multica-ai/multica/releases/latest";
// 模块级缓存的有效期：10 分钟内不重复请求，避免每个页面/每次渲染都打一次
// GitHub。模块级（而非组件内）是为了让同一次会话里的多个调用点共享结果。
const CACHE_TTL_MS = 10 * 60 * 1000; // 缓存 10 分钟

let cachedLatestVersion: string | null = null;
let cachedAt = 0;

// 拉取 GitHub 上最新的发布版本号（tag），失败或没有发布时返回 null。
// 命中模块级缓存（见 CACHE_TTL_MS）就直接返回缓存值，不真正发请求。
async function fetchLatestVersion(): Promise<string | null> {
  if (cachedLatestVersion && Date.now() - cachedAt < CACHE_TTL_MS) {
    return cachedLatestVersion;
  }
  try {
    const resp = await fetch(GITHUB_RELEASES_URL, {
      headers: { Accept: "application/vnd.github+json" },
    });
    if (!resp.ok) return null;
    const data = await resp.json();
    cachedLatestVersion = data.tag_name ?? null;
    cachedAt = Date.now();
    return cachedLatestVersion;
  } catch {
    return null;
  }
}

/**
 * 把发布版 CLI 版本号（"v0.4.17" / "0.4.17"）解析成可逐段比较的数字数组；
 * 字符串不是合法的发布版本时返回 null。
 *
 * 为什么需要判空而不是「当作某个数字」：从源码编译出来的 daemon 上报的是
 * `git describe` 串（"v0.4.17-12-gabc1234"）或 ldflags 默认值（"dev"），
 * 这两者都无法与发布 tag 排序比较。此逻辑与服务端
 * server/internal/cli/update.go 的 `IsReleaseVersion` 保持一致——daemon 自身
 * 的自动升级循环也正是用它判断同一件事。
 */
function parseReleaseVersion(v: string): number[] | null {
  const parts = v.trim().replace(/^v/, "").split(".");
  if (parts.length !== 3) return null;
  const parsed: number[] = [];
  for (const part of parts) {
    if (!/^\d+$/.test(part)) return null;
    parsed.push(Number(part));
  }
  return parsed;
}

/**
 * `latest` 是否严格新于 `current`。
 *
 * 任一侧版本无法解析都按「无更新可用」处理。Number("dev") 是 NaN，而任何
 * 与 NaN 的比较都是 false，所以若按旧的逐组件扫描写法，会一路落到下一段并
 * 对一个从未真正读到的版本串报出「有新版」——那等于拿一个站不住脚的结论，
 * 怂恿用户去替换本地编译出来的二进制。
 */
function isNewer(latest: string, current: string): boolean {
  const l = parseReleaseVersion(latest);
  const c = parseReleaseVersion(current);
  if (!l || !c) return false;
  for (const [i, lv] of l.entries()) {
    const cv = c[i] ?? 0;
    if (lv > cv) return true;
    if (lv < cv) return false;
  }
  return false;
}

// 升级状态 → （图标, 颜色）：pending 用灰 loader（提交后等待轮询出结果）、
// running 用信息蓝 loader（旋转表示进行中）、completed 用绿色对勾、
// failed 用红色叉、timeout 用警告黄叉（超时不是失败，只是没等到结果）。
const statusConfig: Record<
  RuntimeUpdateStatus,
  { icon: typeof Loader2; color: string }
> = {
  pending: { icon: Loader2, color: "text-muted-foreground" },
  running: { icon: Loader2, color: "text-info" },
  completed: { icon: CheckCircle2, color: "text-success" },
  failed: { icon: XCircle, color: "text-destructive" },
  timeout: { icon: XCircle, color: "text-warning" },
};

interface UpdateSectionProps {
  /**
   * 命令通道 runtime 的 id。为 null 时表示观看者不能拿任何 runtime 当升级
   * 命令通道（只读观看者 / 没有可用 runtime）——此时只展示版本信息，按钮禁用。
   */
  runtimeId: string | null;
  /** 本机当前 CLI 版本号；未知时展示「版本未知」占位。 */
  currentVersion: string | null;
  /** runtime 是否在线。离线时 daemon 收不到升级命令，不展示更新按钮。 */
  isOnline: boolean;
}

// 升级区块主体：状态机 + 渲染见文件头导读。本组件受控的部分只有文案/状态，
// 真正的升级动作全部经 core/api 完成。
export function UpdateSection({
  runtimeId,
  currentVersion,
  isOnline,
}: UpdateSectionProps) {
  const { t } = useT("runtimes");
  const [latestVersion, setLatestVersion] = useState<string | null>(null);
  const [status, setStatus] = useState<RuntimeUpdateStatus | null>(null);
  const [error, setError] = useState("");
  const [output, setOutput] = useState("");
  const [updating, setUpdating] = useState(false);
  const [targetVersion, setTargetVersion] = useState<string | null>(null);
  // 轮询定时器的句柄，供 cleanup 统一清除。
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // 停掉正在进行的轮询（若组件卸载或重新发起升级，避免旧定时器继续 setState
  // 或与新一轮轮询打架）。
  const cleanup = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, []);

  // 组件卸载时清理：useEffect 返回 cleanup，等价于 componentWillUnmount。
  useEffect(() => cleanup, [cleanup]);

  // 组件挂载后拉取一次最新版本号，用于展示「有新版」提示与启用更新按钮。
  useEffect(() => {
    fetchLatestVersion().then(setLatestVersion);
  }, []);

  // markCompleted：把状态置为「已完成」并展示升级输出。随后 5 秒清掉状态，
  // 让 UI 回到常规展示——此时 runtime 数据已因刷新拿到新版本号，界面自然
  // 从「已更新」过渡到「已是最新」。
  const markCompleted = useCallback(
    (message: string) => {
      setStatus("completed");
      setOutput(message);
      setUpdating(false);
      setTargetVersion(null);
      cleanup();
      setTimeout(() => setStatus(null), 5000);
    },
    [cleanup],
  );

  // 升级兜底：升级一旦真正生效，daemon 会用新版本号重新上报，父组件刷新后
  // currentVersion 就会追上 targetVersion。此时若 currentVersion 已不再旧于
  // 目标版本（isNewer 为 false），说明版本已切换成功，直接把 UI 收尾成
  // 「已更新到 X」——用来兜住轮询恰好错过「completed」状态的边角情况。
  useEffect(() => {
    if (!updating || !targetVersion || !currentVersion) return;
    if (!isNewer(targetVersion, currentVersion)) {
      markCompleted(`Updated to ${targetVersion}`);
    }
  }, [currentVersion, markCompleted, targetVersion, updating]);

  // handleUpdate：发起一次升级。先清掉旧轮询，再把状态推到 pending，然后调
  // 后端 initiateUpdate；成功后每 2 秒轮询一次 getUpdateResult，直到拿到
  // completed / failed / timeout 为止。
  const handleUpdate = async () => {
    if (!latestVersion || !runtimeId) return;
    cleanup();
    setUpdating(true);
    setTargetVersion(latestVersion);
    setStatus("pending");
    setError("");
    setOutput("");

    try {
      const update = await api.initiateUpdate(runtimeId, latestVersion);

      pollRef.current = setInterval(async () => {
        try {
          const result = await api.getUpdateResult(runtimeId, update.id);
          setStatus(result.status as RuntimeUpdateStatus);

          if (result.status === "completed") {
            markCompleted(
              result.output ?? `Updated to ${targetVersion ?? latestVersion}`,
            );
          } else if (
            result.status === "failed" ||
            result.status === "timeout"
          ) {
            setError(result.error ?? t(($) => $.update.unknown_error));
            setUpdating(false);
            setTargetVersion(null);
            cleanup();
          }
        } catch {
          // 单次轮询的网络错误忽略不计，交给下一轮再试；真正的失败由后端
          // 状态（failed / timeout）表达，而不是一次瞬时的请求失败。
        }
      }, 2000);
    } catch {
      // initiateUpdate 本身失败（请求没送出去/后端拒绝）——这属于「无法开始
      // 升级」，与升级中途失败的展示不同：直接落 failed 态并给出错误文案。
      setStatus("failed");
      setError(t(($) => $.update.initiate_failed));
      setUpdating(false);
      setTargetVersion(null);
    }
  };

  const hasUpdate =
    currentVersion &&
    latestVersion &&
    isNewer(latestVersion, currentVersion);

  // 源码构建的版本无法与发布 tag 排序，所以「有新版」和「已是最新」这两种
  // 说法我们都无法下结论——那就直说「本地构建」，而不是默认显示「已是最新」、
  // 在从未解析过版本号的情况下告诉用户他的二进制已是最新。
  const isLocalBuild =
    !!currentVersion && parseReleaseVersion(currentVersion) === null;

  const config = status ? statusConfig[status] : null;
  const Icon = config?.icon;
  const isActive = status === "pending" || status === "running";

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 flex-wrap">
        <span className="text-caption text-muted-foreground">{t(($) => $.update.cli_version_label)}</span>
        <span className="text-caption font-mono">
          {currentVersion ?? t(($) => $.update.version_unknown)}
        </span>

        <>
            {isLocalBuild && !status && (
              <span
                className="inline-flex items-center gap-1 text-caption text-muted-foreground"
                title={t(($) => $.update.local_build_title)}
              >
                {t(($) => $.update.local_build)}
              </span>
            )}

            {!isLocalBuild &&
              !hasUpdate &&
              currentVersion &&
              latestVersion &&
              !status && (
                <span className="inline-flex items-center gap-1 text-caption text-success">
                  <Check className="h-3 w-3" />
                  {t(($) => $.update.latest)}
                </span>
              )}

            {hasUpdate && !status && (
              <>
                <span className="text-caption text-muted-foreground">→</span>
                <span className="text-caption font-mono text-info">
                  {latestVersion}
                </span>
                <span className="text-caption text-muted-foreground">{t(($) => $.update.available)}</span>
              </>
            )}

            {hasUpdate && !runtimeId && (
              <span
                className="inline-flex items-center gap-1 text-caption text-muted-foreground"
                title={t(($) => $.update.read_only_title)}
              >
                <Lock className="h-3 w-3" />
                {t(($) => $.update.read_only)}
              </span>
            )}

            {hasUpdate && runtimeId && isOnline && !status && (
              <Button
                variant="outline"
                size="xs"
                onClick={handleUpdate}
                disabled={updating}
              >
                <ArrowUpCircle className="h-3 w-3" />
                {t(($) => $.update.action)}
              </Button>
            )}
        </>

        {config && Icon && status && (
          <span
            className={`inline-flex items-center gap-1 text-caption ${config.color}`}
          >
            <Icon className={`h-3 w-3 ${isActive ? "animate-spin" : ""}`} />
            {t(($) => $.update.status[status])}
          </span>
        )}
      </div>

      {status === "completed" && output && (
        <div className="rounded-lg border bg-success/5 px-3 py-2">
          <p className="text-caption text-success">{output}</p>
        </div>
      )}

      {(status === "failed" || status === "timeout") && error && (
        <div className="rounded-lg border border-destructive/20 bg-destructive/5 px-3 py-2">
          <p className="text-caption text-destructive">{error}</p>
          {status === "failed" && (
            <Button
              variant="ghost"
              size="xs"
              className="mt-1"
              onClick={handleUpdate}
            >
              {t(($) => $.update.retry)}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
