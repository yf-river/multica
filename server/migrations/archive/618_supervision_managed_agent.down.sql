UPDATE agent SET system_key = NULL, updated_at = now()
WHERE system_key = 'quality_supervision';
