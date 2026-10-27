-- 监督材料新增执行轨迹与用量类型：其 source_id 指向 supervision_trace_material 的产物行。
ALTER TABLE supervision_material
    DROP CONSTRAINT IF EXISTS supervision_material_kind_check;
ALTER TABLE supervision_material
    ADD CONSTRAINT supervision_material_kind_check
    CHECK (kind IN ('issue','comment','task','message','trace','trace_usage'));
