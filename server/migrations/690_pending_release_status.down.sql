DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM issue_status WHERE category='pending_release' AND NOT is_system)
       OR EXISTS (SELECT 1 FROM issue WHERE status='pending_release') THEN
        RAISE EXCEPTION '待发布状态仍在使用，不能回退';
    END IF;
END $$;
DELETE FROM issue_status WHERE category='pending_release' AND is_system;
ALTER TABLE issue_status DROP CONSTRAINT issue_status_category_check;
ALTER TABLE issue_status ADD CONSTRAINT issue_status_category_check CHECK (category IN ('backlog','todo','in_progress','in_review','done','blocked','cancelled'));
