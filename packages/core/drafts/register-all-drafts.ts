// 本文件是「副作用导入模块」：把每个模块级草稿 store 都 import 一遍，确保在
// 任何清理路径执行前，它们的 `registerDraftCleanup` 调用都已经跑过。
//
// 自注册机制（见 cleanup-registry）只知道「已经被 import 过的」store。若清理
// 调用方只 import 了自己的模块，就会看到一份空注册表，从而漏掉那些碰巧还没被
// 加载的草稿持久化 key——这正是这套机制要修的问题的缩小版。从 `storage-cleanup`
// 导入本模块，可以保证无论在哪里执行清理，注册表都已是完整的。
//
// Chat 刻意不在清单里：它的 key 在 `createChatStore` 内部注册（该 store 是实例
// 级、由 chat provider 创建），所以只有那个实例存在时它的 key 才存在，注册也
// 绑定在同一生命周期。这里没有可提前注册它的可导入内容。

import "../issues/stores/draft-store";
import "../issues/stores/quick-create-store";
import "../issues/stores/comment-draft-store";
import "../projects/draft-store";
import "../feedback/draft-store";
import "../agents/manual-draft-store";
