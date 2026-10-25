import type { AgentTask } from "@multica/core/types";

// 执行时间包含模型和工具；旧记录没有细分证据时只显示可确认的区间。
export function runTiming(task: Pick<AgentTask,"created_at"|"started_at"|"completed_at"|"dispatched_at">) {
  const duration=(a:string|null|undefined,b:string|null|undefined)=>{
    if(!a||!b)return null;
    const ms=Date.parse(b)-Date.parse(a);
    return Number.isFinite(ms)&&ms>=0?ms:null;
  };
  return {queue:duration(task.created_at,task.dispatched_at??task.started_at),preparation:duration(task.dispatched_at,task.started_at),execution:duration(task.started_at,task.completed_at)};
}
