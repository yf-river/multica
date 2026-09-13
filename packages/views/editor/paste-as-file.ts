/**
 * 超过该字符数的纯文本粘贴，会上传为 `pasted-text.txt` 附件，而不是当正文
 * 插入。
 *
 * 聊天是唯一选择开启它的编辑器；原因见 `content-editor.tsx` 里的
 * `pasteAsFileThreshold`。常量与调用点分开，是为了以后加入的 composer 在
 * 同一阈值转换——两个界面不一致的话，同一内容可能进的时候是文件、出的时候
 * 变成文本。
 *
 * 这个数字是产品判断而非技术限制：本技术栈里没有任何东西限制消息长度
 * （客户端没有 schema 上限，服务端只拒绝空内容，所有列都是无界 TEXT）。
 * ~4k 以下粘贴仍可当文本滚动阅读；再往上正文就不堪读了，附件是更好的
 * 容器。作为参照：ChatGPT 在 10,000 字符、Discord 在 2,000 字符的消息
 * 上限处转换。
 *
 * 与 `extensions/markdown-paste.ts` 的 `LARGE_PASTE_TEXT_THRESHOLD` **不是**
 * 一回事——那个（50,000）是解析器逃逸阀，防止巨型粘贴跑 markdown 管线，
 * 适用于包括从不转文件的编辑器在内的所有编辑器。
 */
export const PASTE_AS_FILE_THRESHOLD = 4000;
