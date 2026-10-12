import fs from "node:fs";
import path from "node:path";

const directory = path.resolve(".run/evidence/menu-performance");
const read = name => JSON.parse(fs.readFileSync(path.join(directory, `${name}.json`), "utf8"));
const beforeLocal = read("before-local");
const beforeLatency = read("before-latency");
const afterLocal = read("after-local");
const afterLatency = read("after-latency");
// 烹饪页面的加载提示需要等到菜谱显示；使用补测的完整内容可用条件。
for (const [dataset, suffix] of [[beforeLocal, "local"], [beforeLatency, "latency"]]) {
  const correction = read(`before-${suffix}-cooking`);
  if (!correction.complete || correction.release_id !== dataset.release_id) throw new Error("Invalid cooking supplement");
  const targets = new Set(correction.samples.map(x => x.href));
  dataset.samples = [...dataset.samples.filter(x => !targets.has(x.href)), ...correction.samples];
}
const percentile = (values, q) => [...values].sort((a,b) => a-b)[Math.max(0, Math.ceil(values.length*q)-1)];
const median = values => {
  const sorted = [...values].sort((a,b)=>a-b);
  const middle = Math.floor(sorted.length/2);
  return sorted.length%2 ? sorted[middle] : (sorted[middle-1]+sorted[middle])/2;
};
function stats(dataset, href, phase) {
  const samples = dataset.samples.filter(x=>x.href===href && x.phase===phase);
  const expected = phase === "cold" ? dataset.cold_runs : dataset.warm_runs;
  if (!dataset.complete || dataset.errors.length || samples.length!==expected || samples.some(x=>x.error || !Number.isFinite(x.ready_ms))) throw new Error(`Incomplete measurement: ${dataset.phase} ${href} ${phase}`);
  const bad = samples.flatMap(x=>x.requests.filter(r=>r.status>=400 || (r.failed && r.failed!=="net::ERR_ABORTED")));
  if (bad.length) throw new Error(`Failed request: ${dataset.phase} ${href}`);
  return { median_ms: median(samples.map(x=>x.ready_ms)), p90_ms: percentile(samples.map(x=>x.ready_ms),0.9),
    route_requests: samples.reduce((n,x)=>n+x.requests.filter(r=>r.type==="fetch" && r.path.startsWith(`/${dataset.workspace_slug}/`)).length,0),
    api_requests: samples.reduce((n,x)=>n+x.requests.filter(r=>r.path.startsWith("/api/")).length,0),
    long_task_ms: samples.reduce((n,x)=>n+x.long_task_ms,0) };
}
if(beforeLocal.release_id!==beforeLatency.release_id || afterLocal.release_id!==afterLatency.release_id) throw new Error("Release mismatch");
if ([beforeLatency,afterLocal,afterLatency].some(d=>JSON.stringify(d.menus)!==JSON.stringify(beforeLocal.menus))) throw new Error("Menu inventory mismatch");
const rows=beforeLocal.menus.map(item=> {
  const values = Object.fromEntries([["local",beforeLocal,afterLocal],["latency",beforeLatency,afterLatency]].map(([profile,before,after])=>[profile,{
    cold_before:stats(before,item.href,"cold"),cold_after:stats(after,item.href,"cold"),
    warm_before:stats(before,item.href,"warm"),warm_after:stats(after,item.href,"warm"),
  }]));
  for(const v of Object.values(values)) {
    v.improvement_percent=Math.round((1-v.warm_after.median_ms/v.warm_before.median_ms)*100);
    v.regression=v.warm_after.median_ms>v.warm_before.median_ms*1.1 && v.warm_after.median_ms-v.warm_before.median_ms>=50;
  }
  return {...item,...values};
});
const report={before_release:beforeLocal.release_id,after_release:afterLocal.release_id,menu_count:rows.length,
  measured_clicks:[beforeLocal,beforeLatency,afterLocal,afterLatency].reduce((n,d)=>n+d.samples.length,0),
  profiles:{local:{latency_ms:0},latency:{latency_ms:100}},rows};
fs.writeFileSync(path.join(directory,"comparison.json"),JSON.stringify(report,null,2));
const ms=n=>String(Math.round(n));
const sections=["# 菜单切换性能对比", "", `修复前版本：\`${report.before_release}\`；修复后版本：\`${report.after_release}\`。`, "",
  `真实联调 API、同一测试账号与工作区、Chromium 1440×900，共 ${rows.length} 个菜单、${report.measured_clicks} 次计时。每菜单首次进入 3 次，重复切换 10 次。`, "",
  "计时从真实点击事件到目标正文或空态可见且加载占位结束；标题出现和 networkidle 不作为完成标准。首次进入使用独立浏览器上下文，重复切换使用已访问页面。", "",
  "本次性能修复只设置 Next.js 客户端页面路由复用 30 秒，业务数据缓存、WebSocket 和权限逻辑未修改。并行发布的新联调版本已包含该配置，同时包含独立的烹饪功能修改；下表为联调版本的实测对比，烹饪页的全部变化不能单独归因于性能配置。", "",
  "两种条件均在服务器上的真实浏览器测试：本机直连，以及通过 Chromium 增加固定 100ms 请求延迟。后者是受控网络条件，并非用户设备网络实测。", ""];
for(const [key,title] of [["local","本机直连"],["latency","固定 100ms 网络延迟"]]) {
  sections.push(`## ${title}`,"", "单位：毫秒；首次为中位数，重复切换提供中位数和 P90。", "",
    "| 菜单 | 首次前 | 首次后 | 重复前 | 重复后 | P90 前 | P90 后 | 重复改善 |", "|---|---:|---:|---:|---:|---:|---:|---:|");
  for(const r of rows){const v=r[key];sections.push(`| ${r.label} | ${ms(v.cold_before.median_ms)} | ${ms(v.cold_after.median_ms)} | ${ms(v.warm_before.median_ms)} | ${ms(v.warm_after.median_ms)} | ${ms(v.warm_before.p90_ms)} | ${ms(v.warm_after.p90_ms)} | ${v.improvement_percent}% |`);}
  const sum=field=>rows.reduce((n,r)=>n+r[key][field].route_requests,0);
  sections.push("", `重复切换页面路由请求：${sum("warm_before")} → ${sum("warm_after")} 次（各 170 次点击）。`, "");
}
sections.push("## 证据与限制", "", "- JSON 保存逐次耗时、请求状态、点击事件耗时和主线程长任务；不保存密码、token、请求正文或页面正文。",
  "- 首次进入每菜单仅 3 次，易受代码块加载、React 加载占位调度和机器负载影响；不以该样本证明首次加载稳定改善。",
  "- 请求取消 net::ERR_ABORTED 与真正请求失败分别记录；Next.js 可在路由流已满足渲染后取消剩余读取。",
  "- 烹饪指南基线使用补测文件，等待其专用加载文案消失；原始完整基线保留供核对。", "");
for (const profile of ["local","latency"]) {
  const isolated=read(`isolated-${profile}`);
  if(!isolated.complete || isolated.errors.length)throw new Error("Isolated verification failed");
  const baseline=profile==="local"?beforeLocal:beforeLatency;
  sections.push(`## 隔离构建核对：${profile==="local"?"本机直连":"固定 100ms 延迟"}`,"",
    "应用源码基于旧联调快照，只有路由缓存配置不同；独立 Web 端口连接真实联调 API。用于核对页面路由请求减少的原因。", "",
    "| 菜单 | 重复前 | 隔离修复后 | 路由请求前/后 |", "|---|---:|---:|---:|");
  for(const href of new Set(isolated.samples.map(x=>x.href))){const b=stats(baseline,href,"warm"),a=stats(isolated,href,"warm");const label=isolated.menus.find(x=>x.href===href).label;
    sections.push(`| ${label} | ${ms(b.median_ms)} | ${ms(a.median_ms)} | ${b.route_requests}/${a.route_requests} |`);}
  sections.push("");
}
fs.writeFileSync(path.join(directory,"comparison.md"),sections.join("\n"));
console.log(JSON.stringify({report:path.join(directory,"comparison.md"),menus:rows.length,clicks:report.measured_clicks,regressions:rows.filter(r=>r.local.regression||r.latency.regression).map(r=>r.label)},null,2));
