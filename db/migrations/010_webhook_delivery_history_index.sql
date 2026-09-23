-- 010_webhook_delivery_history_index：项目 Webhook 投递记录按主体和时间查询的索引。
-- 只读门户查询复用 webhook_outbox.internal_app_id 作为项目主体，不改变投递状态语义。

CREATE INDEX IF NOT EXISTS idx_webhook_outbox_subject_history
    ON webhook_outbox (internal_app_id, created_at DESC, id DESC);
