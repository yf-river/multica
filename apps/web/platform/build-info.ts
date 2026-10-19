import { readFile } from "node:fs/promises";
import type { BuildInfo } from "@multica/core/config";

/** 只公开当前 Web 进程所属产物的身份，不暴露源码目录和文件清单。 */
export async function readBuildInfo(): Promise<BuildInfo | null> {
  const manifest = process.env.MULTICA_RELEASE_MANIFEST;
  if (!manifest) return null;
  const data = JSON.parse(await readFile(manifest, "utf8"));
  if (!data.built_at || !Number.isFinite(Date.parse(data.built_at)) ||
      !/^[a-f0-9]{40}$/.test(data.source_commit) || !/^[a-f0-9]{64}$/.test(data.source_digest)) {
    throw new Error("运行产物缺少有效构建信息");
  }
  return { builtAt: data.built_at, commit: data.source_commit, digest: data.source_digest };
}
