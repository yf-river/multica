// task-transcript 对外出口：把"任务执行实录"（任务转录弹窗、按钮、时间线构建、
// 敏感信息脱敏）相关的组件与纯函数统一从这里导出，外部引用一律走本文件。

export { AgentTranscriptDialog } from "./agent-transcript-dialog";
export { TranscriptButton } from "./transcript-button";
export { appendTimelineItem, buildTimeline, coalesceTimelineItems, type TimelineItem } from "./build-timeline";
export { redactSecrets } from "./redact";
