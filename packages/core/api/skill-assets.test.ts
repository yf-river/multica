// @vitest-environment node
import { describe, it, expect } from "vitest";
import { SkillFileSchema } from "./schemas";

describe("skill asset response", () => {
 it("keeps binary metadata and tolerates malformed optional fields", () => {
   const good = SkillFileSchema.parse({id:"file",skill_id:"skill",path:"image.png",kind:"binary",content_type:"image/png",size_bytes:42,mode:493,sha256:"hash"});
   expect(good).toMatchObject({kind:"binary",content:"",size_bytes:42,mode:493});
   const drift = SkillFileSchema.parse({id:"file",skill_id:"skill",path:"notes.md",size_bytes:-1,mode:"bad",content_type:null,sha256:[]});
   expect(drift).toMatchObject({kind:"text",content:"",size_bytes:0,mode:420,sha256:""});
 });
});
