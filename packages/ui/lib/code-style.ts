// 本文件提供「代码等宽字体排版」的关闭连字（ligature）工具类：
// 代码里出现 >=、!= 等时若被字体连字合并显示，容易误读，这里显式关掉。

/** 给元素自身关掉连字 */
export const CODE_LIGATURE_CLASS =
  "[font-variant-ligatures:none] [font-feature-settings:'liga'_0]"

/** 给元素内的 <code> 后代关掉连字（用于包含多个代码片段的容器） */
export const CODE_LIGATURE_DESCENDANT_CLASS =
  '[&_code]:[font-variant-ligatures:none] [&_code]:[font-feature-settings:"liga"_0]'
