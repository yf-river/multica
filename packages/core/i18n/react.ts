// 本文件是「纯客户端」的 i18n 入口：它依赖 react-i18next，而后者在模块加载时
// 就会调用 React.createContext()。从非客户端上下文（RSC / proxy.ts）导入本文件
// 会因 Next.js 在这些上下文里打包了阉割版 React 而报
// "createContext is not a function"。因此必须配合 "use client"，或只在客户端
// 组件树里导入。
export { createI18n } from "./create-i18n";
export { I18nProvider } from "./provider";
