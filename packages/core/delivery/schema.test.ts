// @vitest-environment node
import { expect, it } from "vitest";
import { deliverySnapshotSchema } from "./schema";
it("accepts an empty workspace result but rejects incomplete or unknown delivery states",()=>{
 expect(deliverySnapshotSchema.parse({plan:null,deliveries:[],can_confirm:false}).deliveries).toEqual([]);
 expect(deliverySnapshotSchema.safeParse({plan:null,deliveries:[{id:"delivery",phase:"ready"}],can_confirm:true}).success).toBe(false);
 expect(deliverySnapshotSchema.safeParse({plan:null,can_confirm:true}).success).toBe(false);
});

it("rejects malformed authorization provenance and defaults missing receipts",()=>{
 const base={plan:null,deliveries:[],can_confirm:false};
 expect(deliverySnapshotSchema.parse(base).authorizations).toEqual([]);
 expect(deliverySnapshotSchema.safeParse({...base,authorizations:[{id:"receipt",kind:"release",scope:"test",revoked:false,comment_snapshot:{content:"yes"},created_at:"now"}]}).success).toBe(false);
});
