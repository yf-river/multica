// 本文件是 markdown 模块的统一出口：把 Markdown 渲染、代码块、流式渲染以及各类预处理函数集中 re-export，方便外部一次性引用。
export { Markdown, MemoizedMarkdown, type MarkdownProps, type RenderMode } from './Markdown'
export { CodeBlock, InlineCode, type CodeBlockProps } from './CodeBlock'
export { StreamingMarkdown, type StreamingMarkdownProps } from './StreamingMarkdown'
export { preprocessLinks, detectLinks, hasLinks, shouldAutoLink } from './linkify'
export {
  preprocessIssueIdentifiers,
  isIssueIdentifier,
  ISSUE_IDENTIFIER_PATTERN,
} from './issue-identifiers'
export { preprocessMentionShortcodes } from './mentions'
export { markdownSanitizeSchema, markdownUrlTransform } from './sanitize'
export {
  preprocessFileCards,
  isCdnUrl,
  isFileCardUrl,
  isAllowedFileCardHref,
  FILE_CARD_URL_PATTERN,
} from './file-cards'
