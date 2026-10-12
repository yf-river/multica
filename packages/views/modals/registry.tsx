// 本文件职责：弹窗注册表——监听 modal store 的当前弹窗类型，把对应的弹窗组件挂载出来，
// 并常驻渲染全局的 issue 上限升级对话框。
"use client";

import type { ReactNode } from "react";
import { useModalStore } from "@multica/core/modals";
import { CreateIssueDialog } from "./create-issue-dialog";
import { CreateProjectModal } from "./create-project";
import { CreateSquadModal } from "./create-squad";
import { SetParentIssueModal } from "./set-parent-issue";
import { AddChildIssueModal } from "./add-child-issue";
import { DeleteIssueConfirmModal } from "./delete-issue-confirm";
import { RunConfirmModal } from "./run-confirm";

/**
 * 全局弹窗注册表。
 *
 * 读取 modal store 里当前要展示的弹窗类型，用 switch 匹配到对应组件并挂载；
 * store 为空（modal 为 null）时不挂载任何弹窗。应用中所有通过 useModalStore 打开的弹窗都汇聚到这里。
 */
export function ModalRegistry() {
  const modal = useModalStore((s) => s.modal);
  const data = useModalStore((s) => s.data);
  const close = useModalStore((s) => s.close);

  let activeModal: ReactNode = null;
  switch (modal) {
  // create-issue 与 quick-create-issue 两种弹窗都打开同一个外壳（CreateIssueDialog），
  // 只是初始模式不同。这样弹窗内的「手动 ↔ Agent」切换是即时的——只换内部面板，
  // Dialog 根节点一直挂在 DOM 上。
  case "create-issue":
      activeModal = (
        <CreateIssueDialog
          onClose={close}
          initialMode="manual"
          data={data}
        />
      );
      break;
    case "quick-create-issue":
      activeModal = (
        <CreateIssueDialog
          onClose={close}
          initialMode="agent"
          data={data}
        />
      );
      break;
    case "create-project":
      activeModal = <CreateProjectModal onClose={close} />;
      break;
    case "create-squad":
      activeModal = <CreateSquadModal onClose={close} />;
      break;
    case "issue-set-parent":
      activeModal = <SetParentIssueModal onClose={close} data={data} />;
      break;
    case "issue-add-child":
      activeModal = <AddChildIssueModal onClose={close} data={data} />;
      break;
    case "issue-delete-confirm":
      activeModal = <DeleteIssueConfirmModal onClose={close} data={data} />;
      break;
    case "issue-run-confirm":
      activeModal = <RunConfirmModal onClose={close} data={data} />;
      break;
  }

  return (
    <>
      {activeModal}
    </>
  );
}
