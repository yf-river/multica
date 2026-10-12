import { CookingDetailPage } from "@multica/views/cooking";
export default async function Page({params}:{params:Promise<{tipId:string}>}) { const {tipId}=await params; return <CookingDetailPage id={tipId} kind="tips" />; }
