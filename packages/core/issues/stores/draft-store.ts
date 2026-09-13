// 创建议题弹窗的"整份草稿"store（MUL-5181）：一份草稿拆成共享/手动/Agent 三段，
// 切换表单模式时互不销毁对方的内容。
//
//   shared       —— 不管用哪种方式提交都归属议题本身：项目、优先级、截止日、附件。
//   manual       —— 手动表单自己的状态：标题、描述、状态、开始日、负责人、标签、自定义属性。
//   agent        —— Agent 表单自己的状态：自由文本提示词 + 挑中的 actor（Agent 或小组）。
//   activeMode   —— 草稿当前正在哪种表单里被编辑。
//
// 拆分之前，`switchToAgent` 把标题+描述拼进提示词然后清空手动字段，`switchToManual`
// 把提示词拷进描述却留着旧提示词——于是一趟往返既丢了手动内容又把过期提示词翻了出来。
// 拆成独立槽位后，切换对另一方数据是零操作；唯一的跨写是"首次辅助初始化"：当目标
// 槽位仍为空时面板会填一份默认值。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import type {
  IssueStatus,
  IssuePriority,
  IssueAssigneeType,
  IssuePropertyValues,
} from "../../types";
import type { CreateMode } from "./create-mode-store";
import type { QuickCreateActorType } from "./quick-create-store";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";
import { registerDraftCleanup } from "../../drafts/cleanup-registry";
import { normalizeStoredUploads, type DraftUpload } from "../../drafts/draft-upload";

/** 无论用哪种方式提交都归属议题本身的那部分字段。 */
export interface IssueCreateShared {
  projectId?: string;
  priority: IssuePriority;
  dueDate: string | null;
  /** 弹窗的上传池（占位 + 已完成），被手动描述的 markdown 或 Agent 提示词的
   *  markdown 引用。共用一个池，图片才能在任一模式的切换中存活；每条提交路径
   *  只发送自己内容引用的那批 id。协调器持有（MUL-5181 L2）：
   *  选中瞬间写入的占位在弹窗关闭后仍在，加载时仍 `uploading` 的占位在水合时被丢弃。 */
  attachments: DraftUpload[];
}

/** 手动创建表单自己的那部分字段。 */
export interface IssueCreateManual {
  title: string;
  description: string;
  status: IssueStatus;
  startDate: string | null;
  assigneeType?: IssueAssigneeType;
  assigneeId?: string;
  /** 创建弹窗里选中的标签 id。议题创建成功后才挂标签（创建接口不收标签），
   *  所以这里存的是纯 id 列表，而不是完整的 Label 对象。 */
  labelIds: string[];
  propertyValues: IssuePropertyValues;
}

/** Agent 创建表单自己的那部分字段。 */
export interface IssueCreateAgent {
  prompt: string;
  actorType?: QuickCreateActorType;
  actorId?: string;
}

/** 创建弹窗草稿的完整形态：共享/手动/Agent 三段 + 当前编辑模式。 */
export interface IssueCreateDraft {
  shared: IssueCreateShared;
  manual: IssueCreateManual;
  agent: IssueCreateAgent;
  activeMode: CreateMode;
}

/** 返回一份全空的共享段默认值。 */
const emptyShared = (): IssueCreateShared => ({
  projectId: undefined,
  priority: "none",
  dueDate: null,
  attachments: [],
});

/** 返回一份全空的手动段默认值。 */
const emptyManual = (): IssueCreateManual => ({
  title: "",
  description: "",
  status: "todo",
  startDate: null,
  assigneeType: undefined,
  assigneeId: undefined,
  labelIds: [],
  propertyValues: {},
});

/** 返回一份全空的 Agent 段默认值。 */
const emptyAgent = (): IssueCreateAgent => ({
  prompt: "",
  actorType: undefined,
  actorId: undefined,
});

interface IssueDraftStore {
  draft: IssueCreateDraft;
  /** 仅内存使用。存在时，写入瞄准的是一个隔离的"源上下文"创建会话，持久化继续
   *  序列化这份普通备份——这样刷新页面不会把源草稿泄漏进普通创建流程。 */
  isolatedDraftBackup?: IssueCreateDraft;
  // 最近一次提交时挑选的负责人。跨草稿持久化，让创建弹窗能用用户最近的选择
  // 预填负责人选择器，而不是每次空手打开。
  lastAssigneeType?: IssueAssigneeType;
  lastAssigneeId?: string;
  /** 合并更新共享段字段。 */
  setShared: (patch: Partial<IssueCreateShared>) => void;
  /** 合并更新手动段字段。 */
  setManual: (patch: Partial<IssueCreateManual>) => void;
  /** 合并更新 Agent 段字段。 */
  setAgent: (patch: Partial<IssueCreateAgent>) => void;
  /** 切换当前正在编辑的表单模式。 */
  setActiveMode: (mode: CreateMode) => void;
  /** 清空草稿（保留负责人偏好）。 */
  clearDraft: () => void;
  /** 开始一段隔离的"源上下文"创建会话（备份当前草稿、另起干净草稿）。 */
  beginIsolatedDraft: () => void;
  /** 结束隔离会话，恢复被备份的普通草稿。 */
  endIsolatedDraft: () => void;
  /** 记录本次提交选的负责人，供下次预填。 */
  setLastAssignee: (type?: IssueAssigneeType, id?: string) => void;
  /** 当前草稿是否含有任何可恢复内容。 */
  hasDraft: () => boolean;
}

/** 判断一份持久化的旧草稿是否是 MUL-5181 之前的扁平结构。 */
function isLegacyFlatDraft(d: Record<string, unknown>): boolean {
  return (
    !("manual" in d) &&
    !("shared" in d) &&
    ("title" in d || "status" in d || "labelIds" in d || "description" in d)
  );
}

// 旧版本持久化的草稿要么缺后期新增的子字段、要么用的是 MUL-5181 前的扁平形态。
// 这里补默认值让所有读取点都能依赖声明的 IssueCreateDraft 形状，不用各自防御；
// 同时把遗留的扁平草稿提升进 manual/shared 槽位（那类 store 里本没有 Agent
// 提示词——它活在 `multica_quick_create` 里，不随迁移带过来）。
function migrateDraft(raw: unknown): IssueCreateDraft {
  const d = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;

  if (isLegacyFlatDraft(d)) {
    return {
      shared: {
        ...emptyShared(),
        projectId: d.projectId as string | undefined,
        priority: (d.priority as IssuePriority) ?? "none",
        dueDate: (d.dueDate as string | null) ?? null,
        // 旧版本持久化的是裸 Attachment 行；normalize 把它们包成 `uploaded`
        // 占位（同时丢弃过期的 `uploading` 占位）。
        attachments: normalizeStoredUploads(d.attachments),
      },
      manual: {
        ...emptyManual(),
        title: (d.title as string) ?? "",
        description: (d.description as string) ?? "",
        status: (d.status as IssueStatus) ?? "todo",
        startDate: (d.startDate as string | null) ?? null,
        assigneeType: d.assigneeType as IssueAssigneeType | undefined,
        assigneeId: d.assigneeId as string | undefined,
        labelIds: Array.isArray(d.labelIds) ? (d.labelIds as string[]) : [],
        propertyValues:
          d.propertyValues && typeof d.propertyValues === "object"
            ? (d.propertyValues as IssuePropertyValues)
            : {},
      },
      agent: emptyAgent(),
      activeMode: "manual",
    };
  }

  const sharedRaw = (d.shared as Partial<IssueCreateShared> & { attachments?: unknown }) ?? {};
  return {
    shared: {
      ...emptyShared(),
      ...sharedRaw,
      // L2 前的嵌套草稿存的是裸 Attachment 行；每次加载同样丢弃 `uploading` 占位
      // （字节已经没了）。
      attachments: normalizeStoredUploads(sharedRaw.attachments),
    },
    manual: { ...emptyManual(), ...((d.manual as Partial<IssueCreateManual>) ?? {}) },
    agent: { ...emptyAgent(), ...((d.agent as Partial<IssueCreateAgent>) ?? {}) },
    activeMode: d.activeMode === "agent" ? "agent" : "manual",
  };
}

export const useIssueDraftStore = create<IssueDraftStore>()(
  persist(
    (set, get) => ({
      draft: migrateDraft(undefined),
      lastAssigneeType: undefined,
      lastAssigneeId: undefined,
      setShared: (patch) =>
        set((s) => ({ draft: { ...s.draft, shared: { ...s.draft.shared, ...patch } } })),
      setManual: (patch) =>
        set((s) => ({ draft: { ...s.draft, manual: { ...s.draft.manual, ...patch } } })),
      setAgent: (patch) =>
        set((s) => ({ draft: { ...s.draft, agent: { ...s.draft.agent, ...patch } } })),
      setActiveMode: (mode) =>
        set((s) => ({ draft: { ...s.draft, activeMode: mode } })),
      clearDraft: () =>
        set((s) => ({
          draft: {
            shared: emptyShared(),
            manual: {
              ...emptyManual(),
              // 清空时把上次选的负责人预填进新手动草稿，方便连续创建。
              assigneeType: s.lastAssigneeType,
              assigneeId: s.lastAssigneeId,
            },
            agent: emptyAgent(),
            activeMode: s.draft.activeMode,
          },
        })),
      beginIsolatedDraft: () =>
        set((s) => {
          // 已在隔离会话里则不动，防止重复备份。
          if (s.isolatedDraftBackup) return s;
          return {
            isolatedDraftBackup: s.draft,
            draft: {
              shared: emptyShared(),
              manual: {
                ...emptyManual(),
                assigneeType: s.lastAssigneeType,
                assigneeId: s.lastAssigneeId,
              },
              agent: emptyAgent(),
              activeMode: "agent",
            },
          };
        }),
      endIsolatedDraft: () =>
        set((s) => s.isolatedDraftBackup
          ? { draft: s.isolatedDraftBackup, isolatedDraftBackup: undefined }
          : s),
      setLastAssignee: (type, id) =>
        set({ lastAssigneeType: type, lastAssigneeId: id }),
      hasDraft: () => {
        const { manual, agent, shared } = get().draft;
        return !!(
          manual.title ||
          manual.description ||
          agent.prompt ||
          Object.keys(manual.propertyValues).length > 0 ||
          // 只算可恢复的上传：用户从未关闭的失败/中断残留不能永远把侧边栏的
          // 草稿圆点钉住。
          shared.attachments.some((u) => u.status === "uploaded" || u.status === "uploading")
        );
      },
    }),
    {
      name: "multica_issue_draft",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      // 隔离的"源上下文"草稿绝不能进 localStorage：整个会话期间持久化普通备份，
      // 崩溃/刷新因此恢复的是用户的普通创建草稿，而不是源特定输入。
      partialize: (state) => ({
        draft: state.isolatedDraftBackup ?? state.draft,
        lastAssigneeType: state.lastAssigneeType,
        lastAssigneeId: state.lastAssigneeId,
      }),
      merge: (persistedState, currentState) => {
        const persisted = (persistedState ?? {}) as Partial<IssueDraftStore> & {
          draft?: unknown;
        };
        return {
          ...currentState,
          ...persisted,
          draft: migrateDraft(persisted.draft),
        };
      },
    },
  ),
);

// 切换工作区后重水合该工作区的创建草稿。
registerForWorkspaceRehydration(() => useIssueDraftStore.persist.rehydrate());

// 登出等清理：整体重置（而非 clearDraft）。
// 注意：clearDraft 刻意保留"上次负责人"偏好并把它种进新草稿的手动段——对同一用户
// 连续建议题是正确的；但登出时这样做会把上一个用户最近选的负责人交给本标签页的
// 下一次登录。
registerDraftCleanup({
  storageKey: "multica_issue_draft",
  workspaceScoped: true,
  resetInMemory: () =>
    useIssueDraftStore.setState({
      draft: migrateDraft(undefined),
      lastAssigneeType: undefined,
      lastAssigneeId: undefined,
      isolatedDraftBackup: undefined,
    }),
});
