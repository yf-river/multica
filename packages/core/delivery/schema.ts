import { z } from "zod";
export const deliveryPhaseSchema = z.enum(["waiting_int", "testing", "awaiting_review", "approved", "preparing_release", "publishing", "published", "needs_changes", "cancelled"]);
export const deliveryCheckSchema = z.object({ name: z.string(), status: z.literal("passed"), evidence: z.string() });
export const deliverySchema = z.object({
  id: z.string(), issue_id: z.string(), source_commit: z.string(), source_branch: z.string(), base_commit: z.string(),
  phase: deliveryPhaseSchema, evidence: z.array(deliveryCheckSchema), candidate_id: z.string(), candidate_url: z.string(),
  approved_by: z.string().nullable(), approved_at: z.string().nullable(), error: z.string(), revision: z.number(), queue_ahead: z.number().or(z.string()).transform(Number),
  created_at: z.string(), updated_at: z.string(),
});
export const deliverySnapshotSchema = z.object({
  plan: z.object({ id: z.string(), comment_id: z.string(), comment_revision: z.number(), body: z.string(), stale: z.boolean(), confirmed_by: z.string().nullable(), confirmed_at: z.string().nullable() }).nullable(),
  deliveries: z.array(deliverySchema), can_confirm: z.boolean(),
});
export type DeliverySnapshot = z.infer<typeof deliverySnapshotSchema>;
export type Delivery = z.infer<typeof deliverySchema>;
