-- Runtime profiles are workspace-shared definitions. The former visibility
-- column advertised an unsupported private mode: every supported create path
-- forced workspace, while list, daemon pull, and registration could not
-- enforce creator-only access. Remove the inert surface instead of retaining
-- a misleading second contract.
ALTER TABLE runtime_profile DROP COLUMN IF EXISTS visibility;
