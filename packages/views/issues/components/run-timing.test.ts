// @vitest-environment node
import {expect,it} from "vitest";
import {runTiming} from "./run-timing";
it("separates timestamp intervals without counting overlaps",()=>{
 expect(runTiming({created_at:"2026-10-25T00:00:00Z",dispatched_at:"2026-10-25T00:01:00Z",started_at:"2026-10-25T00:03:00Z",completed_at:"2026-10-25T00:23:00Z"})).toEqual({queue:60000,preparation:120000,execution:1200000});
});
it("keeps missing or invalid evidence unknown",()=>{
 expect(runTiming({created_at:"invalid",dispatched_at:null,started_at:null,completed_at:null})).toEqual({queue:null,preparation:null,execution:null});
});
