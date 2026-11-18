-- 追加供应商结果与核对审计，不保存音频、文本或供应商凭据。
ALTER TABLE speech_meter_segments ADD COLUMN IF NOT EXISTS supplier_state TEXT NOT NULL DEFAULT 'not_sent' CHECK(supplier_state IN ('not_sent','inflight','unknown','confirmed'));
ALTER TABLE speech_meter_segments ADD COLUMN IF NOT EXISTS provider_request_id TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS speech_meter_reconciliations (
 meter_id TEXT PRIMARY KEY REFERENCES speech_meter_segments(id),
 actor_id BIGINT NOT NULL REFERENCES users(id),
 decision TEXT NOT NULL CHECK(decision IN ('settled','released')),
 quantity NUMERIC(20,8) NOT NULL CHECK(quantity>=0),
 evidence TEXT NOT NULL CHECK(length(evidence) BETWEEN 1 AND 200),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_speech_supplier_recovery ON speech_meter_segments(supplier_state,updated_at);
