// 本文件职责：skill 文件树——把文件路径列表组织成树形展示（SKILL.md 置顶、目录优先、
// 其余按名排序），支持选中/展开收起；可选提供编辑动作（编辑/重命名/删除，主文件保留
// 编辑但不可改名删除）。只读视图则完全不带操作菜单。
"use client";

import { useRef, useState } from "react";
import {
  ChevronRight,
  ChevronDown,
  FileText,
  File,
  Folder,
  FolderOpen,
  MoreHorizontal,
  Pencil,
  PencilLine,
  Trash2,
} from "lucide-react";
import { Input } from "@multica/ui/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * 一行文件除了「选中」外还能提供的动作。只读查看器不传它，树就不会出现一个
 * 之后又必须拒绝的操作。
 *
 * `reservedPath` 收窄菜单而不是移除菜单：主文件（如 SKILL.md）不能改名/删除，
 * 但它和其它行一样需要被编辑，所以保留「编辑」入口、去掉另外两个。
 */
export interface FileTreeActions {
  /** 把该文件在编辑器里打开，光标直接落在其中。 */
  onEdit: (path: string) => void;
  /** 返回错误消息；路径可用时返回空串。 */
  validatePath: (path: string, existing: string[]) => string;
  onRename: (from: string, to: string) => void;
  onDelete: (path: string) => void;
  /** 拥有 skill 主内容的路径，不可移动或删除。 */
  reservedPath: string;
}

// ---------------------------------------------------------------------------
// 树形数据结构
// ---------------------------------------------------------------------------

/** 一个树节点：文件或目录，目录带 children，叶子为文件。 */
interface FileTreeNode {
  name: string;
  path: string;
  isDirectory: boolean;
  children: FileTreeNode[];
}

/**
 * 把平铺的文件路径列表建成一棵排序好的树。
 *
 * 按 `/` 分段逐层插入；排序规则：SKILL.md 恒排第一，之后目录在前文件在后，
 * 同级再按名字字典序。排序会就地递归完成。
 */
function buildTree(filePaths: string[]): FileTreeNode[] {
  const root: FileTreeNode[] = [];

  for (const filePath of filePaths) {
    const parts = filePath.split("/");
    let current = root;

    for (let i = 0; i < parts.length; i++) {
      const name = parts[i]!;
      const isLast = i === parts.length - 1;
      const path = parts.slice(0, i + 1).join("/");

      let existing = current.find((n) => n.name === name);

      if (!existing) {
        existing = {
          name,
          path,
          isDirectory: !isLast,
          children: [],
        };
        current.push(existing);
      }

      if (!isLast) {
        current = existing.children;
      }
    }
  }

  function sortNodes(nodes: FileTreeNode[]): FileTreeNode[] {
    nodes.sort((a, b) => {
      if (a.path === "SKILL.md") return -1;
      if (b.path === "SKILL.md") return 1;
      if (a.isDirectory !== b.isDirectory) return a.isDirectory ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
    for (const node of nodes) {
      if (node.isDirectory) sortNodes(node.children);
    }
    return nodes;
  }

  return sortNodes(root);
}

/** 按扩展名挑文件图标：markdown 用文档图标，其余用通用文件图标。 */
function getFileIcon(name: string) {
  if (name.endsWith(".md") || name.endsWith(".mdx")) return FileText;
  return File;
}

// ---------------------------------------------------------------------------
// 树节点渲染
// ---------------------------------------------------------------------------

/**
 * 单个树节点（文件或目录）的行。
 *
 * 目录行可点击展开/收起；文件行可被选中，并在提供 actions 时显示尾部「⋯」菜单
 * （含编辑/重命名/删除）。重命名就地展开成一个输入行（见 RenameRow）。
 */
function TreeNodeItem({
  node,
  selectedPath,
  onSelect,
  allPaths,
  actions,
  depth = 0,
}: {
  node: FileTreeNode;
  selectedPath: string;
  onSelect: (path: string) => void;
  allPaths: string[];
  actions?: FileTreeActions;
  depth?: number;
}) {
  const { t } = useT("skills");
  const [renaming, setRenaming] = useState(false);
  const menuButtonRef = useRef<HTMLButtonElement | null>(null);
  const [expanded, setExpanded] = useState(true);
  const isSelected = node.path === selectedPath;

  if (node.isDirectory) {
    const FolderIcon = expanded ? FolderOpen : Folder;
    const ChevronIcon = expanded ? ChevronDown : ChevronRight;

    return (
      <div>
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          className="flex h-8 w-full items-center gap-1.5 rounded-md pr-2.5 text-left text-caption text-muted-foreground transition-colors hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          style={{ paddingLeft: `${depth * 12 + 10}px` }}
        >
          <ChevronIcon className="h-3 w-3 shrink-0" />
          <FolderIcon className="h-3.5 w-3.5 shrink-0" />
          <span className="truncate">{node.name}</span>
        </button>
        {expanded && (
          <div>
            {node.children.map((child) => (
              <TreeNodeItem
                key={child.path}
                node={child}
                selectedPath={selectedPath}
                onSelect={onSelect}
                allPaths={allPaths}
                actions={actions}
                depth={depth + 1}
              />
            ))}
          </div>
        )}
      </div>
    );
  }

  const Icon = getFileIcon(node.name);
  // 这是两个不同的问题：这一行「有没有菜单」（一旦查看器可编辑，任何一行都有），
  // 以及菜单里「能不能提供重命名/删除」（被保留的主文件不行）。
  const hasMenu = !!actions;
  const canModify = !!actions && node.path !== actions.reservedPath;

  if (renaming && actions) {
    return (
      <RenameRow
        node={node}
        depth={depth}
        allPaths={allPaths}
        actions={actions}
        onDone={() => setRenaming(false)}
      />
    );
  }

  return (
    <div
      className={cn(
        "group/row relative flex items-center rounded-md",
        isSelected
          ? "bg-surface-selected"
          : "hover:bg-surface-hover",
      )}
      // 右键弹出与尾部按钮相同的菜单。菜单锚定在那个按钮上而不是光标处，让每一行只
      // 有「一个菜单」，而不是旁边再挂一个右键菜单根节点。
      onContextMenu={
        hasMenu
          ? (event) => {
              event.preventDefault();
              menuButtonRef.current?.click();
            }
          : undefined
      }
    >
      <button
        type="button"
        role="tab"
        aria-selected={isSelected}
        onClick={() => onSelect(node.path)}
        className={cn(
          "flex h-8 min-w-0 flex-1 items-center gap-2 rounded-md pr-2.5 text-left text-caption transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          isSelected
            ? "font-medium text-surface-selected-foreground"
            : "text-muted-foreground group-hover/row:text-foreground",
        )}
        style={{ paddingLeft: `${depth * 12 + 10}px` }}
      >
        <Icon className="h-3.5 w-3.5 shrink-0" />
        <span className="truncate">{node.name}</span>
      </button>
      {actions && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <button
                ref={menuButtonRef}
                type="button"
                aria-label={t(($) => $.file_tree.actions.label, {
                  path: node.path,
                })}
                // 行被 hover、或其中某个元素持焦点时才显示——否则十个文件就是十颗
                // 按钮。`after:-inset-1` 把命中区扩大到 20px 字形之外。
                //
                // `relative` 是承重墙而非装饰：它让「这颗按钮」成为那个 ::after 的
                // 包含块。去掉它，伪元素会相对最近的有定位祖先（整行）解析，于是这块
                // 隐形命中区铺满整行——又因为是绝对定位、画在文档流里的名字按钮之上，
                // 吃掉对文件名的每一次点击：想选一个支持文件结果弹出了这个菜单
                // （MUL-5654）。opacity-0 并不退出命中测试，所以即使未 hover 整行也
                // 会变得不可点。
                className="relative mr-1 shrink-0 rounded p-0.5 text-faint-foreground opacity-0 transition-opacity after:absolute after:-inset-1 hover:text-foreground group-hover/row:opacity-100 focus-visible:opacity-100 aria-expanded:opacity-100"
                onClick={(event) => event.stopPropagation()}
              >
                <MoreHorizontal className="h-3.5 w-3.5" />
              </button>
            }
          />
          <DropdownMenuContent align="end" className="w-36">
            <DropdownMenuItem onClick={() => actions.onEdit(node.path)}>
              <PencilLine />
              {t(($) => $.file_tree.actions.edit)}
            </DropdownMenuItem>
            {canModify && (
              <>
                <DropdownMenuItem onClick={() => setRenaming(true)}>
                  <Pencil />
                  {t(($) => $.file_tree.actions.rename)}
                </DropdownMenuItem>
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() => actions.onDelete(node.path)}
                >
                  <Trash2 />
                  {t(($) => $.file_tree.actions.delete)}
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );
}

/**
 * 重命名发生在行内而不是弹窗里：名字在「它被读取的地方」就地编辑，周边的路径也仍然
 * 可见、方便比对。校验交给调用方，树里不重复维护一份「什么路径合法」的规则。
 */
function RenameRow({
  node,
  depth,
  allPaths,
  actions,
  onDone,
}: {
  node: FileTreeNode;
  depth: number;
  allPaths: string[];
  actions: FileTreeActions;
  onDone: () => void;
}) {
  const [value, setValue] = useState(node.path);
  const [error, setError] = useState("");

  const submit = () => {
    const next = value.trim();
    if (next === node.path) {
      onDone();
      return;
    }
    const message = actions.validatePath(
      next,
      allPaths.filter((path) => path !== node.path),
    );
    if (message) {
      setError(message);
      return;
    }
    actions.onRename(node.path, next);
    onDone();
  };

  return (
    <div style={{ paddingLeft: `${depth * 12 + 10}px` }} className="pr-1">
      <Input
        autoFocus
        value={value}
        onChange={(event) => {
          setValue(event.target.value);
          setError("");
        }}
        onBlur={submit}
        onKeyDown={(event) => {
          if (event.key === "Enter") submit();
          if (event.key === "Escape") onDone();
        }}
        className="h-7 font-mono text-caption"
      />
      {error && (
        <p role="alert" className="mt-1 text-caption text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 对外组件
// ---------------------------------------------------------------------------

/**
 * skill 文件树。
 *
 * 把文件路径列表建成树并渲染；点击叶子通知 `onSelect`。不传 `actions` 时是纯只读
 * 视图（无任何操作菜单）。
 */
export function FileTree({
  filePaths,
  selectedPath,
  onSelect,
  actions,
}: {
  filePaths: string[];
  selectedPath: string;
  onSelect: (path: string) => void;
  /** 省略此参数即渲染为只读树。 */
  actions?: FileTreeActions;
}) {
  const { t } = useT("skills");
  const tree = buildTree(filePaths);

  if (tree.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-8 text-muted-foreground">
        <FolderOpen className="h-5 w-5 text-faint-foreground" />
        <p className="mt-2 text-caption">{t(($) => $.file_tree.no_files)}</p>
      </div>
    );
  }

  // 这里不加 `role="tablist"`：列表语义由调用方负责——被拆成「主文件 + 支持文件」
  // 两组的栏仍然只是一个 tab 列表。
  return (
    <div className="flex flex-col gap-0.5">
      {tree.map((node) => (
        <TreeNodeItem
          key={node.path}
          node={node}
          selectedPath={selectedPath}
          onSelect={onSelect}
          allPaths={filePaths}
          actions={actions}
        />
      ))}
    </div>
  );
}
