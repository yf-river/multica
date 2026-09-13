// 本文件是 settings 设置模块的统一出口，把页面组件和布局相关组件集中从这里导出，方便外部一处引用。

export { SettingsPage } from "./components";
export {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
  SettingsTab,
} from "./components/settings-layout";
export type {
  SettingsSaveStatus,
} from "./components/settings-layout";
