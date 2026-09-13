import { useState, useEffect, useCallback, useRef, type DragEvent } from "react";

interface UseFileDropZoneOptions {
  onDrop: (files: File[]) => void;
  enabled?: boolean;
}

/**
 * 把一组拖放事件处理器绑定给一个「放下文件即可上传」的区域。
 *
 * @param onDrop 有文件放下时回调（一次给全量 File 数组）。
 * @param enabled 为 false 时忽略拖放并隐藏拖拽高亮。
 * @returns isDragOver（是否正拖拽文件到区域内）与要展开到目标元素上的
 *  drag 事件 props。
 */
function useFileDropZone({ onDrop, enabled = true }: UseFileDropZoneOptions) {
  const [isDragOver, setIsDragOver] = useState(false);
  const onDropRef = useRef(onDrop);
  onDropRef.current = onDrop;

  // 任何文档级 drop/dragend 都清掉高亮（例如用户把文件放到了区域之外）。
  useEffect(() => {
    if (!enabled) return;
    const clear = () => setIsDragOver(false);
    document.addEventListener("drop", clear);
    document.addEventListener("dragend", clear);
    return () => {
      document.removeEventListener("drop", clear);
      document.removeEventListener("dragend", clear);
    };
  }, [enabled]);

  const handleDragEnter = useCallback(
    (e: DragEvent) => {
      e.preventDefault();
      if (enabled && e.dataTransfer.types.includes("Files")) {
        setIsDragOver(true);
      }
    },
    [enabled],
  );

  const handleDragOver = useCallback((e: DragEvent) => {
    e.preventDefault();
  }, []);

  const handleDragLeave = useCallback((e: DragEvent) => {
    if (!e.currentTarget.contains(e.relatedTarget as Node)) {
      setIsDragOver(false);
    }
  }, []);

  const handleDrop = useCallback(
    (e: DragEvent) => {
      const alreadyHandled = e.nativeEvent.defaultPrevented;
      e.preventDefault();
      setIsDragOver(false);
      if (alreadyHandled || !enabled) return;
      const files = e.dataTransfer?.files;
      if (files?.length) {
        onDropRef.current(Array.from(files));
      }
    },
    [enabled],
  );

  const dropZoneProps = {
    onDragEnter: handleDragEnter,
    onDragOver: handleDragOver,
    onDragLeave: handleDragLeave,
    onDrop: handleDrop,
  };

  return { isDragOver: enabled && isDragOver, dropZoneProps };
}

export { useFileDropZone };
