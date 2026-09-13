/**
 * workspace 模块：工作区名与 URL slug 的相互转换工具。
 * 中文工作区名先做拼音化（罗马音），从而生成可用的 URL；同时支持随机生成
 * 一个天体命名的中文工作区 + 稳定 slug，并识别 slug 冲突错误。
 */

import { pinyin } from "pinyin-pro";
import { CELESTIAL_WORKSPACE_NAMES } from "./celestial-workspace-names";

/** 合法的工作区 slug 正则：小写字母/数字，多段用单个 `-` 连接。 */
export const WORKSPACE_SLUG_REGEX = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

const WORKSPACE_SLUG_SUFFIX_ALPHABET = "abcdefghijklmnopqrstuvwxyz0123456789";
const WORKSPACE_SLUG_SUFFIX_LENGTH = 4;

/** 最长的汉字串（含扩展 A 区与兼容区）。 */
const HAN_RUN = /[㐀-䶿一-鿿豈-﫿]+/g;

/**
 * 把一段汉字罗马化成拼音，使中文名能产出 slug。
 *
 * 每个汉字串整体转换（而不是逐字转换）：pinyin-pro 用词组词典消多音字，
 * 所以「长沙」是 "changsha"（而非 "zhangsha"）、「重庆」是 "chongqing"
 * （而非 "zhongqing"）——但它必须能看到周围的字才能做到。音节之间不加分隔
 * 符拼接（「蜘蛛侠」→ "zhizhuxia"），因为一个词应读成一个 slug 段；前后的
 * 空格让一段汉字不会和相邻的拉丁字符黏在一起。
 */
function romanizeHan(name: string): string {
  return name.replace(HAN_RUN, (run) => {
    const syllables = pinyin(run, { toneType: "none", type: "array", v: true });
    return ` ${syllables.join("")} `;
  });
}

/**
 * 从工作区名自动生成一个 slug。
 *
 * 中文名先罗马化（蜘蛛侠 → "zhizhuxia"），这样创建表单能给一个完全没有
 * 拉丁字符的名字填上 URL——顺带就有了 issue 前缀（MUL-6050）。这是一个
 * 用户仍可在创建前编辑的推导默认值，而非写死的回退：对固定回退名（比如
 * "workspace"）的反对意见是它挑了个没用的 URL，且实例上第二个同名工作区
 * 会撞车——这两个问题对罗马化名字都不存在。
 *
 * 产品只有一个中文受众，所以每段汉字都用普通话拼音。假名、谚文、emoji 等
 * 其它不受支持的书写系统会被丢弃。
 */
export function nameToWorkspaceSlug(name: string): string {
  const romanized = romanizeHan(name);
  return romanized
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
}

/**
 * 生成一个随机的天体中文工作区名，同时保持一个稳定的 URL slug。
 * 若两个用户恰好生成同一个候选名，服务端的唯一 slug 约束仍是最终裁决者。
 */
export function randomCelestialWorkspaceIdentity(
  random: () => number = Math.random,
): { name: string; slug: string } {
  const celestial =
    CELESTIAL_WORKSPACE_NAMES[
      Math.floor(random() * CELESTIAL_WORKSPACE_NAMES.length)
    ]!;
  let suffix = "";

  for (let i = 0; i < WORKSPACE_SLUG_SUFFIX_LENGTH; i += 1) {
    suffix +=
      WORKSPACE_SLUG_SUFFIX_ALPHABET[
        Math.floor(random() * WORKSPACE_SLUG_SUFFIX_ALPHABET.length)
      ]!;
  }

  return {
    name: celestial.name,
    slug: `${celestial.slugBase}-${suffix}`,
  };
}

/**
 * 判断一个未知错误是否是工作区 slug 冲突（HTTP 409）。
 * 用于创建/改名工作区时把「slug 已存在」从其它错误中区分出来提示用户。
 */
export function isWorkspaceSlugConflict(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "status" in error &&
    (error as { status?: unknown }).status === 409
  );
}
