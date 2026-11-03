-- 订阅控制显式应用/用户/分组授权，不从可维护外部标识推定归属，不自动回填历史授权。
CREATE TABLE IF NOT EXISTS subscription_control_grants (
 app_id VARCHAR(128) NOT NULL CHECK (app_id ~ '^[A-Za-z0-9_-]+$'),
 user_id BIGINT NOT NULL CHECK (user_id>0),
 group_id BIGINT NOT NULL CHECK (group_id>0),
 enabled BOOLEAN NOT NULL,
 revision BIGINT NOT NULL CHECK (revision>0),
 updated_by BIGINT NOT NULL CHECK (updated_by>0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(app_id,user_id,group_id)
);
COMMENT ON TABLE subscription_control_grants IS '上游本地管理员显式授予的集成订阅范围，默认无授权；历史记录不随账号删除而删除';
COMMENT ON COLUMN subscription_control_grants.app_id IS '已配置集成应用标识';
COMMENT ON COLUMN subscription_control_grants.user_id IS '真实计费普通用户标识，非平台管理员标识';
COMMENT ON COLUMN subscription_control_grants.group_id IS '真实专属订阅分组标识';
COMMENT ON COLUMN subscription_control_grants.enabled IS '当前授权是否有效';
COMMENT ON COLUMN subscription_control_grants.revision IS '授权并发版本，维护时单调递增';
COMMENT ON COLUMN subscription_control_grants.updated_by IS '实际维护授权的上游本地管理员';
COMMENT ON COLUMN subscription_control_grants.created_at IS '首次显式授权时间';
COMMENT ON COLUMN subscription_control_grants.updated_at IS '最近显式维护时间';

CREATE TABLE IF NOT EXISTS subscription_control_grant_events (
 command_id UUID PRIMARY KEY CHECK(command_id<>'00000000-0000-0000-0000-000000000000'),
 app_id VARCHAR(128) NOT NULL,
 user_id BIGINT NOT NULL CHECK(user_id>0),
 group_id BIGINT NOT NULL CHECK(group_id>0),
 revision BIGINT NOT NULL CHECK(revision>0),
 operator_id BIGINT NOT NULL CHECK(operator_id>0),
 enabled BOOLEAN NOT NULL,
 reason VARCHAR(2000) NOT NULL CHECK(octet_length(reason) BETWEEN 1 AND 2000),
 request_digest VARCHAR(64) NOT NULL CHECK(request_digest ~ '^[0-9a-f]{64}$'),
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object' AND octet_length(snapshot::text)<=4096),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(app_id,user_id,group_id,revision)
);
COMMENT ON TABLE subscription_control_grant_events IS '仅追加的订阅范围授权事件及永久原键结果，不记录集成凭据';
COMMENT ON COLUMN subscription_control_grant_events.command_id IS '本地授权操作固定UUID，用于永久幂等';
COMMENT ON COLUMN subscription_control_grant_events.app_id IS '原授权应用';
COMMENT ON COLUMN subscription_control_grant_events.user_id IS '原授权计费用户';
COMMENT ON COLUMN subscription_control_grant_events.group_id IS '原授权订阅分组';
COMMENT ON COLUMN subscription_control_grant_events.revision IS '此次生成的授权版本';
COMMENT ON COLUMN subscription_control_grant_events.operator_id IS '真实上游本地管理员';
COMMENT ON COLUMN subscription_control_grant_events.enabled IS '本次明确启用或停用结果';
COMMENT ON COLUMN subscription_control_grant_events.reason IS '有界人工授权理由';
COMMENT ON COLUMN subscription_control_grant_events.request_digest IS '固定授权参数摘要，不含凭据';
COMMENT ON COLUMN subscription_control_grant_events.snapshot IS '此次固定授权结果，后续变更不覆盖';
COMMENT ON COLUMN subscription_control_grant_events.created_at IS '授权事件提交时间';
CREATE INDEX IF NOT EXISTS idx_subscription_control_grant_history
 ON subscription_control_grant_events(app_id,user_id,group_id,created_at DESC,command_id);

CREATE OR REPLACE FUNCTION keep_subscription_control_grant_event() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'SUBSCRIPTION_CONTROL_GRANT_EVENT_IMMUTABLE' USING ERRCODE='23514';
END $$;
CREATE OR REPLACE TRIGGER subscription_control_grant_event_immutable
 BEFORE UPDATE OR DELETE ON subscription_control_grant_events
 FOR EACH ROW EXECUTE FUNCTION keep_subscription_control_grant_event();
