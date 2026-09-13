import { defaultUrlTransform } from 'react-markdown'
import { defaultSchema, type Options } from 'rehype-sanitize'

// 本文件提供产品级 Markdown 渲染统一使用的 sanitize 白名单 schema 与 URL 变换，是全局唯一副本，所有渲染面都应引用而非复制。
/**
 * 所有产品级 Markdown 渲染器（Chat 用 ui/markdown/Markdown.tsx，Issue/Comment
 * 用 views/editor/readonly-content.tsx）共用的权威 sanitize schema + URL 变换。
 *
 * 这是唯一副本。两个渲染器此前各自维护了一份字面副本，且已经发生漂移——
 * readonly 那份为 `==highlight==` 放行了 <mark>，而 chat 那份没有。安全相关的
 * 白名单在两地维护意味着将来任何 XSS 修复都要改两处，漏一处就是一个洞。新增
 * 渲染面时应导入本文件，永远不要复制它（MUL-4922）。
 *
 * 下面两处门槛必须对“什么是合法的行内图片”达成一致：`protocols.src` +
 * `attributes.img`（sanitize 侧）和 `markdownUrlTransform`（react-markdown 侧）。
 * 要改就一起改。
 */
export const markdownSanitizeSchema: Options = {
  ...defaultSchema,
  // 放行 <mark>（文本高亮）——由 highlightToHtml 从 `==text==` 生成。
  // 它不带任何属性，所以只需放行标签名。
  tagNames: [...(defaultSchema.tagNames ?? []), 'mark'],
  protocols: {
    ...defaultSchema.protocols,
    href: [...(defaultSchema.protocols?.href ?? []), 'mention', 'slash'],
    // 放行行内 data-URI 图片（二维码、图表、base64 截图）。
    // 这里的 scheme 门只放行 `data:`；下面 attributes.img 再进一步收紧到
    // image/*，所以非图片的 data URI 仍会被拒绝。
    src: [...(defaultSchema.protocols?.src ?? []), 'data'],
  },
  attributes: {
    ...defaultSchema.attributes,
    div: [
      ...(defaultSchema.attributes?.div ?? []),
      'dataType',
      'dataHref',
      'dataFilename',
    ],
    code: [
      ...(defaultSchema.attributes?.code ?? []),
      ['className', /^language-/],
      ['className', /^math-/],
      ['className', /^hljs/],
    ],
    img: [
      // 去掉默认的裸 `src` 项，让下面的值白名单成为 findDefinition 解析到的
      // 那个——它按名字返回第一个匹配，所以一个裸的 `src` 字符串会遮蔽（并
      // 禁用）我们的白名单。
      ...(defaultSchema.attributes?.img ?? []).filter(
        (attr) => (typeof attr === 'string' ? attr : attr[0]) !== 'src',
      ),
      'alt',
      // 放行行内 data:image/* URI，其它 src 形式（http/https/站内相对）保持
      // 原样：负向前瞻保留所有非 data 值，而 data: 被进一步收紧到图片。
      ['src', /^data:image\//i, /^(?!data:)/i],
    ],
  },
}

/**
 * 放行 Multica 内部协议和行内图片通过 react-markdown 的 URL 门槛，
 * 其余保持默认安全策略。
 */
export function markdownUrlTransform(url: string): string {
  if (url.startsWith('mention://')) return url
  if (url.startsWith('slash://skill/')) return url
  // defaultUrlTransform 会把所有 data: URL 清空成 ''，即便 rehype-sanitize
  // 保留了它也会被清掉 src。这里与 markdownSanitizeSchema 里的 image/* 收紧
  // 保持一致，让两道门槛对“什么是合法行内图片”认知统一。
  if (/^data:image\//i.test(url)) return url
  return defaultUrlTransform(url)
}
