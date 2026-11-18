-- 语音计量仅保存账务元数据，不保存原始音频、转写、模板或总结。
CREATE TABLE IF NOT EXISTS speech_sessions (
 id TEXT PRIMARY KEY,
 api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
 user_id BIGINT NOT NULL REFERENCES users(id),
 group_id BIGINT NOT NULL REFERENCES groups(id),
 request_id TEXT NOT NULL,
 ticket_hash TEXT NOT NULL UNIQUE,
 model JSONB NOT NULL,
 language TEXT NOT NULL DEFAULT '' CHECK(length(language)<=32),
 state TEXT NOT NULL CHECK(state IN ('created','active','completed','interrupted')),
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(api_key_id,request_id)
);
CREATE INDEX IF NOT EXISTS idx_speech_sessions_owner ON speech_sessions(user_id,created_at DESC);
CREATE TABLE IF NOT EXISTS speech_meter_segments (
 id TEXT PRIMARY KEY,
 session_id TEXT NOT NULL REFERENCES speech_sessions(id),
 reservation_id TEXT NOT NULL,
 quantity NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK(quantity>=0),
 points NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK(points>=0),
 unit TEXT NOT NULL CHECK(unit IN ('seconds','characters')),
 state TEXT NOT NULL CHECK(state IN ('pending','ready','settled','released')),
 command JSONB,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_speech_meter_session ON speech_meter_segments(session_id,id);
CREATE INDEX IF NOT EXISTS idx_speech_meter_recovery ON speech_meter_segments(state,updated_at);
