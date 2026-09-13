/**
 * 沙箱 HTML 附件 iframe 的「锚点导航」补丁。
 *
 * HTML 附件预览会把用户提供的文档挂载在
 * `<iframe sandbox="allow-scripts" srcdoc={...}>` 里——刻意**不带**
 * `allow-same-origin`，因为内容来自不可信的用户上传，同源会让它够到
 * cookie / localStorage / parent.document。
 *
 * 这个安全姿态有个副作用：在 Chromium 里，被沙箱的 srcdoc iframe 处于
 * 不透明源，浏览器会把对 `<a href="#section">` 的点击视为跨源 frame 导航——
 * 于是被静默拒绝，不滚动、也不报错。参见 whatwg/html#3537 与 crbug 40191760；
 * 这是规范 + 实现层的共识行为，不是等版本就能等掉的 bug。
 *
 * 修复在 iframe 内部完成：往文档末尾追加一小段脚本，监听片段链接点击并
 * 自行调用 `scrollIntoView`。脚本运行在 iframe 自身的不透明源里——与用户
 * HTML 在 `allow-scripts` 下已有的能力相同，碰不到父页面。补丁只拦截
 * `href="#..."` 的点击：尊重用户 HTML 安装的 preventDefault 处理器，
 * 目标 id 不存在时也不干预（让文档里的 SPA / 标签式路由自行处理）。
 */
const FRAGMENT_NAV_SHIM = `<script>
(function(){
  document.addEventListener('click', function(e) {
    if (e.defaultPrevented) return;
    var t = e.target;
    if (!t || typeof t.closest !== 'function') return;
    var a = t.closest('a[href]');
    if (!a) return;
    var href = a.getAttribute('href');
    if (!href || href.charAt(0) !== '#' || href === '#') return;
    var id;
    try { id = decodeURIComponent(href.slice(1)); } catch (_) { return; }
    if (!id) return;
    var dest = document.getElementById(id);
    if (!dest && typeof CSS !== 'undefined' && CSS.escape) {
      dest = document.querySelector('a[name="' + CSS.escape(id) + '"]');
    }
    if (!dest) return;
    e.preventDefault();
    dest.scrollIntoView({ behavior: 'smooth', block: 'start' });
  });
})();
</script>`;

/**
 * 在给定 HTML 末尾追加锚点导航补丁脚本。
 * @param html 原始 srcdoc HTML；为空时仅返回补丁脚本本身。
 */
export function withFragmentNavShim(html: string | undefined): string {
  return (html ?? "") + FRAGMENT_NAV_SHIM;
}

/** 暴露给单元测试，方便断言补丁脚本被原样追加。 */
export const __FRAGMENT_NAV_SHIM__ = FRAGMENT_NAV_SHIM;
