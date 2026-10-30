import {redirect} from 'next/navigation';
export default async function Page({params,searchParams}:{params:Promise<{workspaceSlug:string}>;searchParams:Promise<Record<string,string|string[]|undefined>>}){
 const {workspaceSlug}=await params;const values=await searchParams;const q=new URLSearchParams();
 for(const [key,value]of Object.entries(values)){if(typeof value==='string')q.set(key,value);else if(Array.isArray(value))for(const item of value)q.append(key,item)}
 redirect(`/${encodeURIComponent(workspaceSlug)}/insights${q.size?'?'+q:''}`);
}
