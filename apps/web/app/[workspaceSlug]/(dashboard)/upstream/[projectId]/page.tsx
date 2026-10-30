import { UpstreamDetailPage } from "@multica/views/upstream/upstream-page";

export default async function Page({params}:{params:Promise<{projectId:string}>}) {
 const {projectId}=await params;
 return <UpstreamDetailPage projectId={projectId}/>;
}
