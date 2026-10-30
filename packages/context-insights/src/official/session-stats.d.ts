import type {z} from 'zod';import type {SessionEvent} from '@deepseek-ai/dsh-session';
export interface Stats {turns:number;steps:number;llmMs:number;toolMs:number;ttftMs:number;ttftSteps:number;decodeMs:number;decodeTokens:number;[key:string]:unknown}
export const sessionStatsProjectionDefinition:{key:string;stateVersion:number;stateSchema:z.ZodType<Stats>;init():Stats;apply(state:Stats,event:SessionEvent):Stats;wire:{viewSchema:z.ZodType<Stats>;view(state:Stats):Stats}};
