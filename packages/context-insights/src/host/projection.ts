import type {z} from 'zod'
import type {SessionEvent} from '@deepseek-ai/dsh-session'
import type {ContextTimeline,ContextActivity,ContextHeaders} from '../shared/types.js'
type ProjectionMap={contextTimeline:ContextTimeline;contextActivity:ContextActivity;contextHeaders:ContextHeaders}
export interface WiredProjection<K extends keyof ProjectionMap,S> {
 key:K;stateSchema:z.ZodType<S>;init():S;apply(state:S,event:SessionEvent):S;
 wire:{viewSchema:z.ZodType<ProjectionMap[K]>;view(state:S):ProjectionMap[K]};stateVersion:number;
}
