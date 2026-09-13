// 本文件职责：skills 组件统一出口——对外只暴露页面级组件（SkillsPage 与 SkillDetailPage），
// 列表/对话框/文件树等内部组件不外泄。
export { default as SkillsPage } from "./skills-page";
export { SkillDetailPage } from "./skill-detail-page";
