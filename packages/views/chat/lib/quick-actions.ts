// 快捷操作协议清理工具：在响应流式输出期间隐藏保留的 agent-to-server 快捷操作页脚。
/**
 * 在响应仍在流式输出期间，隐藏保留的 agent-to-server 快捷操作页脚。
 * 持久化的消息内容在服务端已经被剥掉了；这个函数补上那个短暂的窗口期——
 * 否则一个不完整的 JSON 围栏可能会在实时 timeline 里闪一下。
 *
 * @param content - 消息正文内容。
 * @returns 去掉末尾快捷操作协议围栏后的内容。
 */
export function stripChatQuickActionsProtocol(content: string): string {
  const matches = [
    ...content.matchAll(/(?:^|\r?\n)```quick-actions(?:\r?\n|$)/g),
  ];
  const match = matches.at(-1);
  if (!match || match.index == null) return content;

  const footerStart = match.index;
  const bodyStart = footerStart + match[0].length;
  const closingFence = /\r?\n```/.exec(content.slice(bodyStart));
  if (closingFence) {
    const afterFence = content.slice(
      bodyStart + closingFence.index + closingFence[0].length,
    );
    // 围栏后面还有正文，说明这是回复里的一个普通示例，
    // 而不是保留在末尾的协议页脚。
    if (afterFence.trim() !== "") return content;
  }

  return content.slice(0, footerStart).trimEnd();
}
