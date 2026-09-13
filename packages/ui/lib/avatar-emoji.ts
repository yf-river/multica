// 本文件是「emoji 头像」编解码工具：avatar_url 里以 `emoji:` 前缀标记一个
// emoji 头像，这里负责解析前缀取值与拼接前缀存值，并导出头像选择器的
// 推荐表情集。

/** emoji 头像的存储前缀（该模块私有，前端只有这一处定义格式标记） */
const AVATAR_EMOJI_PREFIX = "emoji:";

/**
 * 头像选择器提供的一键可选 emoji 集。刻意与后端给全新 Agent 随机分配的
 * 那组保持同一套（server/internal/handler/agent_avatar.go 的
 * `agentEmojiAvatars`），这样推荐项读起来是产品自家的一家人脸，而不是另一
 * 份无关列表。每行 8 个。
 */
export const AVATAR_EMOJI_SUGGESTIONS = [
  "🐙", "🦊", "🦉", "🐝", "🐼", "🐸", "🐯", "🦁",
  "🐨", "🐵", "🐧", "🐳", "🦋", "🌞", "🌙", "⭐",
  "🔥", "⚡", "🍀", "🌈", "🚀", "🤖", "👾", "🧠",
];

/**
 * 解析 avatar_url：是 `emoji:` 前缀就返回其中的 emoji 字符，否则返回 null
 *（说明是普通图片地址）。
 */
export function parseAvatarEmoji(value?: string | null): string | null {
  if (!value?.startsWith(AVATAR_EMOJI_PREFIX)) return null;

  const emoji = value.slice(AVATAR_EMOJI_PREFIX.length).trim();
  return emoji || null;
}

/**
 * 构造要存进 `avatar_url` 的 emoji 头像值：拼上 `emoji:` 前缀。
 * 前缀在本模块内保持私有，使标记格式在客户端只有这一个持有者，
 * 与后端的 `agentEmojiAvatarPrefix` 对应。
 */
export function formatAvatarEmoji(emoji: string): string {
  return AVATAR_EMOJI_PREFIX + emoji.trim();
}
