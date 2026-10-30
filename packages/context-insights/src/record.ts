export interface LocaleService { getLocale?(): { active: string }; subscribe?(listener:()=>void):()=>void }
export function asRecord(value:unknown):Record<string,unknown>|null {return value!==null&&typeof value==='object'?value as Record<string,unknown>:null}
export function numOf(value:unknown):number {return typeof value==='number'&&Number.isFinite(value)?value:0}
