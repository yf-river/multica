/**
 * 文件上传扩展模块——把粘贴/拖放/按钮选中的文件变成编辑器里的可渲染内容。
 *
 * 统一流程见 uploadAndInsertFile：先插入带 `uploading` 的占位——图片用 blob
 * 预览，其余画一张 fileCard——随后调用上传 handler；落定时用 settleUploadNode
 * 把占位节点换成真实附件（若结果真是图片会就地升级节点类型），失败则用
 * removeUploadNode 移除占位。占位节点通过 `uploadId` 与草稿 store 的
 * `clientUploadId` 关联，所以跨编辑器挂载的落定也能找回自己的节点。文件末尾的
 * createFileUploadExtension 负责把这一切接进 Tiptap 的 paste/drop 事件，并支持
 * 把超大纯文本粘贴合成 .txt 附件（pastedTextSources 保留源文本以便失败恢复）。
 */
import { Extension } from "@tiptap/core";
import { Plugin, PluginKey, TextSelection } from "@tiptap/pm/state";
import type { UploadResult } from "@multica/core/hooks/use-file-upload";
import { createSafeId } from "@multica/core/utils";

/**
 * 在文档里定位本次上传的占位节点，无论它长成哪种形态。
 *
 * `uploadId` 与草稿 store 里的 `clientUploadId` 是同一个值——在
 * {@link uploadAndInsertFile}（或重建路径）里一次性铸造，两份记录都带着它。
 * 同一个身份标识，让一次「落定」能在另一个挂载重建的文档里找到自己的节点。
 */

function findUploadNode(editor: any, uploadId: string): { pos: number; node: any } | null {
  let found: { pos: number; node: any } | null = null;
  editor.state.doc.descendants((node: any, pos: number) => {
    if (found) return false;
    if (
      (node.type.name === "fileCard" || node.type.name === "image") &&
      node.attrs.uploadId === uploadId
    ) {
      found = { pos, node };
      return false;
    }
    return undefined;
  });
  return found;
}

/** 移除该上传的占位节点（无论形态）。返回是否找到并移除。 */
export function removeUploadNode(editor: any, uploadId: string): boolean {
  const hit = findUploadNode(editor, uploadId);
  if (!hit) return false;
  editor.view.dispatch(editor.state.tr.delete(hit.pos, hit.pos + hit.node.nodeSize));
  return true;
}

/**
 * 把该上传的占位节点原地替换成已完成的附件。
 *
 * 没有任何节点带着这个 id 时返回 false——调用方由此得知文档并未展示
 * 本次上传，可以退回「追加到文档末尾」的策略。
 *
 * fileCard 落定成图片时会替换节点**类型**，而不只是 attrs：重建路径无法
 * 预知一次上传最终是不是图片（它只有文件名、没有字节），所以总是先写
 * 卡片，在这里让文档与实际到货的内容对齐。
 */

export function settleUploadNode(editor: any, uploadId: string, result: UploadResult): boolean {
  const hit = findUploadNode(editor, uploadId);
  if (!hit) return false;
  // 持久化的是稳定的按附件 URL，绝不是短时效签名 URL（MUL-3130）。
  // `link` 是「无工作区头像」分支的回退。
  const href = result.markdownLink || result.link;
  const isImage = (result.content_type ?? "").startsWith("image/");
  const tr = editor.state.tr;

  if (hit.node.type.name === "image") {
    tr.setNodeMarkup(hit.pos, undefined, {
      ...hit.node.attrs,
      src: href,
      alt: result.filename,
      uploading: false,
      uploadId: null,
    });
  } else if (isImage) {
    tr.replaceWith(
      hit.pos,
      hit.pos + hit.node.nodeSize,
      editor.schema.nodes.image.create({ src: href, alt: result.filename, uploading: false }),
    );
  } else {
    tr.setNodeMarkup(hit.pos, undefined, {
      ...hit.node.attrs,
      href,
      uploading: false,
      uploadId: null,
    });
  }
  editor.view.dispatch(tr);
  return true;
}

/**
 * 为「本文档还没展示的上传」写一个占位节点。
 *
 * 场景：composer 重新打开时，上一次挂载已经启动过某上传——字节跟着那次
 * 挂载一起没了，没有可渲染的预览，画张卡片是我们能诚实给出的全部；
 * {@link settleUploadNode} 会在这个结果真是图片时把它升级成图片。
 */

export function insertUploadPlaceholder(
  editor: any,
  upload: { uploadId: string; filename: string; size?: number },
): boolean {
  // 幂等：已经画过就算成功，这样「重试直到落定」的调用方不会被自己的
  // 第一次插入骗进无限重试。
  if (findUploadNode(editor, upload.uploadId)) return true;
  const endPos = editor.state.doc.content.size;
  editor
    .chain()
    .insertContentAt(endPos, {
      type: "fileCard",
      attrs: {
        filename: upload.filename,
        href: "",
        fileSize: upload.size ?? 0,
        uploading: true,
        uploadId: upload.uploadId,
      },
    })
    .run();
  return true;
}

/** 按 src 在文档里找第一个 image 节点的位置；找不到返回 null。 */
export function findImagePosBySrc(editor: any, src: string): number | null {
  if (!editor) return null;
  let imagePos: number | null = null;
  editor.state.doc.descendants((node: any, pos: number) => {
    if (imagePos !== null) return false;
    if (node.type.name === "image" && node.attrs.src === src) {
      imagePos = pos;
      return false;
    }
    return undefined;
  });
  return imagePos;
}

/**
 * 在后台线程读取图片的固有像素尺寸。解码失败或 API 不可用时（如测试里的
 * jsdom，`createImageBitmap` 未定义）返回 null——调用方退回不预留占位框。
 */
async function readImageDimensions(
  file: File,
): Promise<{ width: number; height: number } | null> {
  if (typeof createImageBitmap !== "function") return null;
  try {
    const bitmap = await createImageBitmap(file);
    const dims = { width: bitmap.width, height: bitmap.height };
    bitmap.close();
    return dims.width > 0 && dims.height > 0 ? dims : null;
  } catch {
    return null;
  }
}

/**
 * 量出文件的固有尺寸，写进刚插入的 image 节点，让浏览器在解码前就预留
 * 占位框（不产生布局跳动）。插入后即发即忘：以 blob `src` 为键，所以若
 * 上传替换早已换掉了它，就直接跳过——替换时通过 `...imageNode.attrs`
 * 会保留我们设好的 width/height。
 */
async function applyImageDimensions(editor: any, file: File, src: string) {
  const dims = await readImageDimensions(file);
  if (!dims) return;

  const imagePos = findImagePosBySrc(editor, src);
  if (imagePos === null) return;

  const imageNode = editor.state.doc.nodeAt(imagePos);
  if (!imageNode || imageNode.attrs.width) return;

  const tr = editor.state.tr.setNodeMarkup(imagePos, undefined, {
    ...imageNode.attrs,
    width: dims.width,
    height: dims.height,
  });
  editor.view.dispatch(tr);
}

/** 把光标移到图片后紧跟的段落开头（便于插入图片后立刻接着打字）。 */
function moveSelectionToParagraphAfterImage(editor: any, src: string) {
  const imagePos = findImagePosBySrc(editor, src);
  if (imagePos === null) return;

  const imageNode = editor.state.doc.nodeAt(imagePos);
  if (!imageNode) return;

  const afterImagePos = imagePos + imageNode.nodeSize;
  const $afterImage = editor.state.doc.resolve(afterImagePos);
  if ($afterImage.nodeAfter?.type.name !== "paragraph") return;

  const paragraphStart = afterImagePos + 1;
  const tr = editor.state.tr
    .setSelection(TextSelection.create(editor.state.doc, paragraphStart))
    .scrollIntoView();
  editor.view.dispatch(tr);
}

/**
 * 统一的上传流程：插入 blob 预览 → 上传 → 换成真实 URL。
 * 粘贴/拖放（光标处）与按钮上传（文档末尾）都走它。
 */
export async function uploadAndInsertFile(

  editor: any,
  file: File,
  handler: (file: File, uploadId: string) => Promise<UploadResult | null>,
  pos?: number,
) {
  const isImage = file.type.startsWith("image/");
  // 两份记录共用一个 id。handler 把它当作草稿的 `clientUploadId`，所以
  // 落在另一个挂载上的 settle 仍能找到本挂载画的节点——见 findUploadNode。
  const uploadId = createSafeId();

  if (isImage) {
    const blobUrl = URL.createObjectURL(file);
    const imgAttrs = { src: blobUrl, alt: file.name, uploading: true, uploadId };
    if (pos !== undefined) {
      editor.chain().focus().insertContentAt(pos, { type: "image", attrs: imgAttrs }).run();
    } else {
      editor.chain().focus().setImage(imgAttrs).run();
      moveSelectionToParagraphAfterImage(editor, blobUrl);
    }

    // 尽早预留图片占位框，异步解码才不会引起布局跳动。
    // 即发即忘：绝不能拖慢下面的 handler() 调用，同步插入契约（即时预览）
    // 依赖它。
    void applyImageDimensions(editor, file, blobUrl);

    try {
      const result = await handler(file, uploadId);
      // 上传可能活过本挂载（由协调器持有，MUL-5181）：等到落定时编辑器
      // 也许已经销毁。对着已销毁的 EditorView dispatch 会抛错，而 catch
      // 又会导致二次 dispatch——死编辑器的交付由回写路径负责，不归本次
      // 替换管。
      if (editor.isDestroyed) return;
      if (result) settleUploadNode(editor, uploadId, result);
      else removeUploadNode(editor, uploadId);
    } catch {
      if (!editor.isDestroyed) removeUploadNode(editor, uploadId);
    } finally {
      URL.revokeObjectURL(blobUrl);
    }
  } else {
    // 非图片：插入骨架 fileCard → 上传 → 用真实 URL 收尾。
    const cardAttrs = { filename: file.name, href: "", fileSize: file.size, uploading: true, uploadId };
    const insertContent = { type: "fileCard", attrs: cardAttrs };
    if (pos !== undefined) {
      editor.chain().focus().insertContentAt(pos, insertContent).run();
    } else {
      editor.chain().focus().insertContent(insertContent).run();
    }

    try {
      const result = await handler(file, uploadId);
      // 见图片分支：编辑器已销毁后的 settle 不得对着死 EditorView dispatch。
      if (editor.isDestroyed) return;
      if (result) settleUploadNode(editor, uploadId, result);
      else removeUploadNode(editor, uploadId);
    } catch {
      if (!editor.isDestroyed) removeUploadNode(editor, uploadId);
    }
  }
}

/**
 * 去掉同一 paste/drop 事件里的重复文件。
 * macOS/Chrome 有时会把同一个文件两次放进 FileList。
 */
function dedupFiles(files: FileList): File[] {
  const seen = new Set<string>();
  return Array.from(files).filter((file) => {
    const key = `${file.name}\0${file.size}\0${file.type}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

/** 超过阈值的大段粘贴被合成为 .txt 时，赋予该文件的文件名。 */
export const PASTED_TEXT_FILENAME = "pasted-text.txt";

/**
 * 粘贴成文件路径为每个合成文件保存的源文本，按把它带往下游的 File 实例
 * 索引。
 *
 * 与拖放文件不同，它没有别处的副本：文本从未写进文档，源头可能是一个
 * 用户已经关掉的标签页。所以无论谁持有草稿，在上传失败时都要能把它
 * 放回去。用 WeakMap 而不是往 File 上挂属性，让 File 保持上传层期望的
 * 原样，也让条目随上传一同消亡。用 {@link pastedTextSource} 读取；从磁盘
 * 来的 File 返回 undefined，不需要恢复。
 */
const pastedTextSources = new WeakMap<File, string>();

/** 记录某个合成的粘贴文件是基于哪段文本建出来的。 */
export function markPastedTextFile(file: File, text: string): File {
  pastedTextSources.set(file, text);
  return file;
}

/** 超过阈值粘贴的来源文本；真实文件返回 undefined。 */
export function pastedTextSource(file: File): string | undefined {
  return pastedTextSources.get(file);
}

/**
 * 创建文件上传扩展。
 *
 * @param onUploadFileRef 上传处理器的 ref（签名见类型）。
 * @param pasteAsFileThresholdRef 纯文本粘贴超过该字符数时，改为作为 .txt
 *   附件上传而不是插进文档。用 ref 是因为扩展数组在挂载时只建一次，而喂
 *   它的 prop 可能变化。Undefined / 0 表示所有粘贴都当文本——这是默认，
 *   从未主动开启的编辑器行为与之前完全一致。
 */
export function createFileUploadExtension(
  onUploadFileRef: React.RefObject<
    ((file: File, uploadId: string) => Promise<UploadResult | null>) | undefined
  >,
  pasteAsFileThresholdRef?: React.RefObject<number | undefined>,
) {
  return Extension.create({
    name: "fileUpload",
    addProseMirrorPlugins() {
      const { editor } = this;

      const handleFiles = async (files: File[]) => {
        const handler = onUploadFileRef.current;
        if (!handler) return false;
        for (const file of files) {
          await uploadAndInsertFile(editor, file, handler);
        }
        return true;
      };

      return [
        new Plugin({
          key: new PluginKey("fileUpload"),
          props: {
            handlePaste(_view, event) {
              const files = event.clipboardData?.files;
              if (!files?.length) {
                // 剪贴板上没有文件：这仍可能是一次大到宿主想让它变成附件
                // 而不是成千上万字符正文的粘贴（仅回合制 composer 会传
                // 阈值；文档编辑器从不传）。
                const threshold = pasteAsFileThresholdRef?.current;
                if (!threshold || threshold <= 0) return false;
                const text = event.clipboardData?.getData("text/plain") ?? "";
                if (text.length <= threshold) return false;
                if (!onUploadFileRef.current) return false;
                // 粘贴进代码块是唯一刻意保持行内的大粘贴——用户打开围栏
                // 就是想展示这个东西。把它转成附件等于夺走用户正要的东西。
                if (editor.isActive("codeBlock")) return false;
                const file = new File([text], PASTED_TEXT_FILENAME, { type: "text/plain" });
                handleFiles([markPastedTextFile(file, text)]);
                return true;
              }
              if (!onUploadFileRef.current) return false;
              handleFiles(dedupFiles(files));
              return true;
            },
            handleDrop(view, event) {
              const dragEvent = event as DragEvent;
              const files = dragEvent.dataTransfer?.files;
              if (!files?.length) return false;
              const handler = onUploadFileRef.current;
              if (!handler) return false;
              // 从鼠标坐标解析放置位置。
              // 只有第一个文件用放置位置；后续文件追加到末尾，避免过期
              // 位置的错位。
              const dropPos = view.posAtCoords({ left: dragEvent.clientX, top: dragEvent.clientY });
              const unique = dedupFiles(files);
              for (let i = 0; i < unique.length; i++) {
                const insertPos = i === 0 ? dropPos?.pos : undefined;
                uploadAndInsertFile(editor, unique[i]!, handler, insertPos);
              }
              return true;
            },
          },
        }),
      ];
    },
  });
}
