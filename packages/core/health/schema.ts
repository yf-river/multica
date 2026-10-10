import { z } from "zod";

export const HealthProfileSchema = z
  .object({
    name: z.string().default(""),
    sex: z.string().default(""),
    birth_date: z.string().default(""),
  })
  .transform((p) => ({ name: p.name, sex: p.sex, birthDate: p.birth_date }));
export const HealthObservationSchema = z
  .object({
    id: z.string(),
    code: z.string(),
    name: z.string(),
    original_name: z.string().default(""),
    category: z.string().default("其他"),
    value: z.string(),
    unit: z.string().default(""),
    original_value: z.string().default(""),
    original_unit: z.string().default(""),
    reference_range: z.string().default(""),
    abnormal: z.string().default(""),
    observed_at: z.string().default(""),
    method: z.string().default(""),
    specimen: z.string().default(""),
    site: z.string().default(""),
    source: z.string(),
    report_id: z.string().default(""),
    page: z.number().default(0),
    evidence: z.string().default(""),
    status: z.string(),
    uncertainty: z.string().default(""),
    supersedes: z.string().default(""),
    measurement_group_id: z.string().default(""),
  })
  .transform((o) => ({
    id: o.id,
    code: o.code,
    name: o.name,
    originalName: o.original_name,
    category: o.category,
    value: o.value,
    unit: o.unit,
    originalValue: o.original_value,
    originalUnit: o.original_unit,
    referenceRange: o.reference_range,
    abnormal: o.abnormal,
    observedAt: o.observed_at,
    method: o.method,
    specimen: o.specimen,
    site: o.site,
    source: o.source,
    reportId: o.report_id,
    page: o.page,
    evidence: o.evidence,
    status: o.status,
    uncertainty: o.uncertainty,
    supersedes: o.supersedes,
    measurementGroupId: o.measurement_group_id,
  }));
export const HealthOverviewSchema = z
  .object({
    profile: HealthProfileSchema,
    revision: z.number(),
    epoch: z.number(),
    age: z.number().nullable(),
    observations: z.array(HealthObservationSchema),
    current: z.array(HealthObservationSchema),
  })
  .transform((p) => ({ ...p, available: true }));
export const HealthResultSchema = z
  .object({
    exam_date: z.string().default(""),
    institution: z.string().default(""),
    patient_name: z.string().default(""),
    observations: z.array(HealthObservationSchema),
    pages: z.array(
      z.object({
        page: z.number(),
        status: z.string(),
        note: z.string().default(""),
      }),
    ),
    analysis: z.string().default(""),
    conclusions: z.string().default(""),
    uncertainty: z.string().default(""),
    context_revision: z.number().default(0),
  })
  .transform((r) => ({
    examDate: r.exam_date,
    institution: r.institution,
    patientName: r.patient_name,
    observations: r.observations,
    pages: r.pages,
    analysis: r.analysis,
    conclusions: r.conclusions,
    uncertainty: r.uncertainty,
    contextRevision: r.context_revision,
  }));
export const HealthReportSchema = z
  .object({
    id: z.string(),
    filename: z.string(),
    sha256: z.string(),
    size_bytes: z.number(),
    page_count: z.number(),
    issue_id: z.string().nullable().optional(),
    agent_id: z.string().nullable().optional(),
    attempt: z.number(),
    epoch: z.number(),
    created_at: z.string(),
    task_status: z.string().default(""),
    issue_status: z.string().default(""),
    result: HealthResultSchema.nullable().optional(),
    result_revision: z.number().default(0),
    applied: z.boolean().default(false),
    effective_observations: z.array(HealthObservationSchema).default([]),
  })
  .transform((r) => ({
    id: r.id,
    filename: r.filename,
    sha256: r.sha256,
    sizeBytes: r.size_bytes,
    pageCount: r.page_count,
    issueId: r.issue_id,
    agentId: r.agent_id,
    attempt: r.attempt,
    epoch: r.epoch,
    createdAt: r.created_at,
    taskStatus: r.task_status,
    issueStatus: r.issue_status,
    result: r.result,
    resultRevision: r.result_revision,
    applied: r.applied,
    effectiveObservations: r.effective_observations,
  }));
export const HealthReportsSchema = z.object({
  items: z.array(HealthReportSchema),
});
export const HealthReportHistorySchema = z.object({
  items: z.array(
    z
      .object({
        attempt: z.number(),
        revision: z.number(),
        result: HealthResultSchema,
        created_at: z.string(),
      })
      .transform((r) => ({
        attempt: r.attempt,
        revision: r.revision,
        result: r.result,
        createdAt: r.created_at,
      })),
  ),
});
export type HealthExtraction = z.infer<
  typeof HealthReportHistorySchema
>["items"][number];
export const HealthRevisionSchema = z
  .object({
    revision: z.number(),
    observations: z.array(HealthObservationSchema).default([]),
    state: z
      .object({
        profile: HealthProfileSchema,
        observation_ids: z.array(z.string()),
        current_observation_ids: z.array(z.string()).default([]),
      })
      .transform((s) => ({
        profile: s.profile,
        observationIds: s.observation_ids,
        currentObservationIds: s.current_observation_ids,
      })),
    reason: z.string(),
    created_at: z.string(),
  })
  .transform((r) => ({
    revision: r.revision,
    observations: r.observations,
    state: r.state,
    reason: r.reason,
    createdAt: r.created_at,
  }));
export const HealthRevisionsSchema = z.object({
  items: z.array(HealthRevisionSchema),
});
export const HealthReceiptSchema = z.object({ revision: z.number() });
export type HealthProfile = z.infer<typeof HealthProfileSchema>;
export type HealthObservation = z.infer<typeof HealthObservationSchema>;
export type HealthOverview = z.infer<typeof HealthOverviewSchema>;
export type HealthReport = z.infer<typeof HealthReportSchema>;
export type HealthRevision = z.infer<typeof HealthRevisionSchema>;
export const EMPTY_HEALTH: HealthOverview = {
  available: false,
  profile: { name: "", sex: "", birthDate: "" },
  revision: 0,
  epoch: 0,
  age: null,
  observations: [],
  current: [],
};
export function observationWire(o: Partial<HealthObservation>) {
  return {
    name: o.name,
    code: o.code,
    category: o.category,
    value: o.value,
    unit: o.unit,
    reference_range: o.referenceRange,
    observed_at: o.observedAt,
    method: o.method,
    specimen: o.specimen,
    site: o.site,
    supersedes: o.supersedes,
    measurement_group_id: o.measurementGroupId,
  };
}
export function healthMetricKey(o: HealthObservation) {
  return [o.code, o.unit, o.specimen, o.method, o.site].join("\u001f");
}

export function isHealthAbnormal(o: Pick<HealthObservation, "abnormal">) {
  return !["", "-", "—", "--", "−", "normal", "正常", "未见异常", "无异常", "阴性"].includes(o.abnormal.trim().toLowerCase());
}
