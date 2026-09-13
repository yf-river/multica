// 本文件职责：读取/校验 skill 的「来源（origin）」信息——skill 从哪导入（本地 runtime、
// ClawHub、Skills.sh、GitHub）或手动创建——以及据此判断可否重新拉取、能否作为链接打开。
import type { Skill, SkillSummary } from "@multica/core/types";

/**
 * 对 `Skill.config.origin` 的「判别式视图」——origin 是后端在 skill 从外部导入时
 * （本地 runtime、ClawHub、Skills.sh、GitHub）写入的一段 JSONB。手动创建没有 origin，
 * 这里为它们补一个 `{ type: "manual" }`，让消费方代码形态统一。
 */
export type OriginInfo = {
  type: "runtime_local" | "clawhub" | "skills_sh" | "github" | "manual";
  provider?: string;
  runtime_id?: string;
  source_path?: string;
  source_url?: string;
  owner?: string;
  repo?: string;
  ref?: string;
  path?: string;
  skill?: string;
  slug?: string;
};

/** 从 skill 上读出结构化的来源信息；type 不在白名单里（或缺失）一律归为手动创建。 */
export function readOrigin(skill: SkillSummary): OriginInfo {
  const raw = (skill.config?.origin ?? null) as
    | (OriginInfo & Record<string, unknown>)
    | null;
  if (raw?.type === "runtime_local") return raw;
  if (raw?.type === "clawhub") return raw;
  if (raw?.type === "skills_sh") return raw;
  if (raw?.type === "github") return raw;
  return { type: "manual" };
}

/**
 * 该 skill 能否从它导入的地方「重新下载」。只有托管来源够格：本地 runtime 的副本
 * 要通过 daemon 重新导入；手动创建 / 归档上传的 skill 则根本没有上游。服务端在
 * `POST /api/skills/:id/refresh` 上执行同样的规则。
 */
export function isRefreshableOrigin(origin: OriginInfo): boolean {
  return (
    (origin.type === "github" || origin.type === "skills_sh" || origin.type === "clawhub") &&
    typeof origin.source_url === "string" &&
    origin.source_url.length > 0
  );
}

// 各托管来源类型「合法允许指向」的主机名——这是服务端 `detectImportSource`
// 白名单（skill.go）的客户端镜像。按来源类型分 key，防止手改的配置把任意主机
// 伪装成 GitHub / Skills.sh / ClawHub 链接。
const ORIGIN_SOURCE_HOSTS: Partial<Record<OriginInfo["type"], readonly string[]>> = {
  github: ["github.com", "www.github.com"],
  skills_sh: ["skills.sh", "www.skills.sh"],
  clawhub: ["clawhub.ai", "www.clawhub.ai"],
};

/**
 * 把来源的 `source_url` 校验成可用的链接地址，校验不过则返回 null。
 *
 * `origin` 是持久化的 JSONB，任何能改 skill 的人都可原样写入（`PATCH /api/skills/:id`
 * 并不校验 `config`），所以某个值在变成 `href` 之前，必须先经受「当作跳转目标」的
 * 考验：只允许 http(s)，且主机名必须与该来源类型声明的一致。其余一切——manual/
 * runtime_local 来源、缺失或畸形的 URL、`data:` 等其它协议、主机/类型不匹配——都
 * 返回 null，调用方据此回退成纯文本展示。
 *
 * 这里刻意比 `isRefreshableOrigin` 更严：服务端的 refresh 路径自己会复核来源，
 * 并且也接受无协议的值（例如一个光秃秃的 ClawHub slug）——那种值可以刷新，
 * 却无法作为 `href` 使用。
 */
export function originSourceUrl(origin: OriginInfo | null): string | null {
  if (!origin) return null;
  const hosts = ORIGIN_SOURCE_HOSTS[origin.type];
  if (!hosts) return null;
  if (typeof origin.source_url !== "string" || origin.source_url.length === 0) {
    return null;
  }
  let parsed: URL;
  try {
    parsed = new URL(origin.source_url);
  } catch {
    return null;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return null;
  if (!hosts.includes(parsed.hostname.toLowerCase())) return null;
  return origin.source_url;
}

/**
 * skill 的文件总数：SKILL.md 恒存在，外加任意附加文件。接受 `SkillSummary` 是因为
 * 列表接口不返回 `files` 数组——那种情况下只能确定正文存在，计数回退为 1。
 */
export function totalFileCount(skill: Skill | SkillSummary): number {
  const files = (skill as Skill).files;
  return (files?.length ?? 0) + 1;
}
