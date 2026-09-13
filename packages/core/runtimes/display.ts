// Runtime 的展示名/展示标签工具：自定义覆盖、并总是保留底层 provider 家族的信息。
import type { AgentRuntime } from "../types";

/**
 * 一台 runtime 要显示的名字（MUL-4217）：设了用户自定义覆盖就用它，否则用 daemon
 * 提议的默认名。防御省略 custom_name 的旧后端，以及纯空白覆盖。
 * @param runtime 含 name/custom_name 的 runtime。
 * @returns 展示名。
 */
export function runtimeDisplayName(
  runtime: Pick<AgentRuntime, "name" | "custom_name">,
): string {
  const custom = runtime.custom_name?.trim();
  return custom ? custom : runtime.name;
}

/**
 * 一个总是让 provider 家族浮出水面的 runtime 标签（即便设了自定义别名，#5260）。
 * daemon 把 provider 烤进 `name`（"Codex (host)"），但用户别名经 runtimeDisplayName
 * 把整串换掉，藏起了这台 runtime 实际由哪个 CLI 支撑。存在别名时我们用括号把
 * provider 重新附上；没有别名时 `name` 已携带它，所以原样返回避免重复
 * （"Codex (host) (codex)"）。
 * @param runtime 含 name/custom_name/provider 的 runtime。
 * @returns 展示标签。
 */
export function runtimeDisplayLabel(
  runtime: Pick<AgentRuntime, "name" | "custom_name" | "provider">,
): string {
  const display = runtimeDisplayName(runtime);
  const hasCustom = !!runtime.custom_name?.trim();
  const provider = runtime.provider?.trim();
  if (!hasCustom || !provider) return display;
  return `${display} (${providerDisplayName(provider)})`;
}

/**
 * provider slug 中"人类可读名不只是 slug 的首字母大写"的那些。
 * 必须镜像 daemon 的 `runtimeDisplayNameOverrides`（server/internal/daemon/daemon.go）：
 * daemon 在无别名场景把该显示名烤进 `name`（例如 "Trae (host)"），所以别名标签必须
 * 用完全相同的名字，否则两条路径会漂移（#5260）。今天 `qoderclicn`、`codearts`、`dsh`、
 * `traecli`、`qwen`、`qwenpaw`、`mcode`、`zeroclaw` 需要覆盖——其它 provider 在两边
 * 都是各自 slug 的首字母大写。请与 daemon 的映射保持同步。
 */
const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  codearts: "CodeArts",
  dsh: "DeepSeek Harness",
  qoderclicn: "Qoder CN",
  traecli: "Trae",
  qwen: "Qwen Code",
  qwenpaw: "QwenPaw",
  mcode: "MiniMax Code",
  omp: "Oh-My-Pi",
  zeroclaw: "ZeroClaw",
};

/**
 * 把 provider slug 映射成展示名，与 daemon 的 providerDisplayName 一致：
 * 命中显式覆盖表用覆盖值，否则返回首字母大写的 slug。
 * @param provider 运行时 provider 标识。
 * @returns 展示名。
 */
export function providerDisplayName(provider: string): string {
  const known = PROVIDER_DISPLAY_NAMES[provider];
  if (known) return known;
  return provider
    .split("-")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
