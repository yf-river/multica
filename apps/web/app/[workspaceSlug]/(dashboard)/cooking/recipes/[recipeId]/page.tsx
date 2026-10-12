import { CookingDetailPage } from "@multica/views/cooking";
export default async function Page({params}:{params:Promise<{recipeId:string}>}) { const {recipeId}=await params; return <CookingDetailPage id={recipeId} kind="recipes" />; }
