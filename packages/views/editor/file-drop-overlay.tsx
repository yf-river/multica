/**
 * FileDropOverlay——拖文件进编辑器时盖在上层的虚线提示遮罩。
 *
 * 纯视觉元素：显示在编辑区域之上、不拦截指针（pointer-events-none），
 * 是否展示由外层根据拖拽状态控制。
 */
function FileDropOverlay() {
  return (
    <div className="absolute inset-0 z-10 rounded-[inherit] border border-dashed border-brand/20 bg-brand/[0.02] pointer-events-none" />
  );
}

export { FileDropOverlay };
