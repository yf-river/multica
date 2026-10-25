-- 有回复的已删除评论保留空占位，应用层负责清理生命周期。
ALTER TABLE comment ADD COLUMN deleted_at TIMESTAMPTZ NULL;
