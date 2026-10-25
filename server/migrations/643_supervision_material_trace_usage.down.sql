-- 回滚后 trace_usage 材料不再可读；执行前需确认没有待处理的监督批次依赖该类型。
DELETE FROM supervision_material WHERE kind = 'trace_usage';
ALTER TABLE supervision_material
    DROP CONSTRAINT IF EXISTS supervision_material_kind_check;
ALTER TABLE supervision_material
    ADD CONSTRAINT supervision_material_kind_check
    CHECK (kind IN ('issue','comment','task','message','trace'));
