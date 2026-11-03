-- 订阅控制版本、永久命令回执和缓存失效依据；由既有迁移入口在事务中执行。
-- 先锁定原订阅表，确保历史初始化和触发器安装之间没有未记录的控制变更。
LOCK TABLE user_subscriptions IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE IF NOT EXISTS subscription_control_slots (
 user_id BIGINT NOT NULL CHECK (user_id > 0),
 group_id BIGINT NOT NULL CHECK (group_id > 0),
 revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
 subscription_id BIGINT CHECK (subscription_id > 0),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(user_id, group_id)
);
COMMENT ON TABLE subscription_control_slots IS '用户与分组的持久订阅控制槽，删除订阅后保留版本';
COMMENT ON COLUMN subscription_control_slots.user_id IS '计费用户标识';
COMMENT ON COLUMN subscription_control_slots.group_id IS '订阅分组标识';
COMMENT ON COLUMN subscription_control_slots.revision IS '单调递增控制版本，不随撤销或删除重置';
COMMENT ON COLUMN subscription_control_slots.subscription_id IS '当前订阅或最新撤销订阅标识，无记录为空';
COMMENT ON COLUMN subscription_control_slots.updated_at IS '控制槽最近变更时间';

CREATE INDEX IF NOT EXISTS idx_subscription_control_current
 ON user_subscriptions(user_id, group_id, (deleted_at IS NULL) DESC, deleted_at DESC, id DESC);

-- 原有效记录优先，其次取最新撤销记录；重复执行不改写既有控制版本。
INSERT INTO subscription_control_slots(user_id, group_id, revision, subscription_id)
 SELECT DISTINCT ON (user_id, group_id) user_id, group_id, 1, id
 FROM user_subscriptions
 ORDER BY user_id, group_id, (deleted_at IS NULL) DESC, deleted_at DESC, id DESC
 ON CONFLICT(user_id, group_id) DO NOTHING;

CREATE OR REPLACE FUNCTION track_subscription_control_revision() RETURNS TRIGGER
 LANGUAGE plpgsql AS $$
DECLARE
 old_user BIGINT; old_group BIGINT; new_user BIGINT; new_group BIGINT;
 target RECORD; old_revision BIGINT; current_subscription BIGINT;
BEGIN
 IF TG_OP <> 'INSERT' THEN old_user := OLD.user_id; old_group := OLD.group_id; END IF;
 IF TG_OP <> 'DELETE' THEN new_user := NEW.user_id; new_group := NEW.group_id; END IF;
 -- 所有旧写入口在原事务中共同锁槽；迁移用户/分组时按固定顺序锁定两个槽。
 FOR target IN
  SELECT DISTINCT v.user_id, v.group_id
  FROM (VALUES(old_user,old_group),(new_user,new_group)) AS v(user_id,group_id)
  WHERE v.user_id IS NOT NULL AND v.group_id IS NOT NULL
  ORDER BY v.user_id,v.group_id
 LOOP
  INSERT INTO subscription_control_slots(user_id,group_id) VALUES(target.user_id,target.group_id)
   ON CONFLICT(user_id,group_id) DO NOTHING;
  SELECT revision INTO old_revision FROM subscription_control_slots
   WHERE user_id=target.user_id AND group_id=target.group_id FOR UPDATE;
  IF old_revision = 9223372036854775807 THEN
   RAISE EXCEPTION 'SUBSCRIPTION_CONTROL_REVISION_EXHAUSTED' USING ERRCODE='22003';
  END IF;
  SELECT id INTO current_subscription FROM user_subscriptions
   WHERE user_id=target.user_id AND group_id=target.group_id
   ORDER BY (deleted_at IS NULL) DESC,deleted_at DESC,id DESC LIMIT 1;
  UPDATE subscription_control_slots
   SET revision=old_revision+1,subscription_id=current_subscription,updated_at=clock_timestamp()
   WHERE user_id=target.user_id AND group_id=target.group_id;
 END LOOP;
 RETURN NULL;
END $$;

-- 明确控制字段的写入才推进版本；普通消费仅累加用量及updated_at，不进入控制锁。
-- 窗口字段即使原值相同也推进版本，使零用量的手工重置仍具备可验证回执。
CREATE OR REPLACE TRIGGER subscription_control_revision
 AFTER INSERT OR DELETE OR UPDATE OF user_id,group_id,status,starts_at,expires_at,deleted_at,
 daily_window_start,weekly_window_start,monthly_window_start
 ON user_subscriptions FOR EACH ROW EXECUTE FUNCTION track_subscription_control_revision();

CREATE TABLE IF NOT EXISTS subscription_control_commands (
 app_id VARCHAR(128) NOT NULL CHECK (app_id ~ '^[A-Za-z0-9_-]+$'),
 command_id UUID NOT NULL CHECK (command_id <> '00000000-0000-0000-0000-000000000000'),
 actor_uid BIGINT NOT NULL CHECK (actor_uid > 0),
 user_id BIGINT NOT NULL CHECK (user_id > 0),
 group_id BIGINT NOT NULL CHECK (group_id > 0),
 subscription_id BIGINT NOT NULL CHECK (subscription_id >= 0),
 expected_revision BIGINT NOT NULL CHECK (expected_revision >= 0 AND expected_revision < 9223372036854775807),
 action VARCHAR(20) NOT NULL CHECK (action IN ('assign','extend','revoke','restore','reset_quota')),
 days INTEGER NOT NULL CHECK (days BETWEEN -36500 AND 36500),
 reset_period VARCHAR(10) NOT NULL,
 request_digest VARCHAR(64) NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
 status VARCHAR(12) NOT NULL CHECK (status IN ('succeeded','rejected')),
 receipt JSONB NOT NULL CHECK (jsonb_typeof(receipt)='object' AND octet_length(receipt::text)<=8192),
 finished_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(app_id,command_id),
 CHECK ((action='assign' AND subscription_id=0 AND days>0 AND reset_period='')
  OR (action='extend' AND subscription_id>0 AND expected_revision>0 AND days<>0 AND reset_period='')
  OR (action IN ('revoke','restore') AND subscription_id>0 AND expected_revision>0 AND days=0 AND reset_period='')
  OR (action='reset_quota' AND subscription_id>0 AND expected_revision>0 AND days=0 AND reset_period IN ('daily','weekly','monthly','all'))),
 CHECK (COALESCE(receipt->>'status'=status AND receipt->>'command_id'=command_id::text
  AND receipt->>'request_digest'=request_digest AND receipt->>'protocol'='workmesh.subscription-control.v1',FALSE))
);
COMMENT ON TABLE subscription_control_commands IS '按集成应用隔离的永久订阅命令及不可变终态回执，不使用短期幂等清理';
COMMENT ON COLUMN subscription_control_commands.app_id IS '已认证集成应用标识';
COMMENT ON COLUMN subscription_control_commands.command_id IS '原固定命令UUID';
COMMENT ON COLUMN subscription_control_commands.actor_uid IS '平台独立管理员引用，仅用于审计';
COMMENT ON COLUMN subscription_control_commands.user_id IS '命令固定计费用户';
COMMENT ON COLUMN subscription_control_commands.group_id IS '命令固定订阅分组';
COMMENT ON COLUMN subscription_control_commands.subscription_id IS '原订阅标识，分配时为零';
COMMENT ON COLUMN subscription_control_commands.expected_revision IS '命令固定旧控制版本';
COMMENT ON COLUMN subscription_control_commands.action IS '固定业务动作';
COMMENT ON COLUMN subscription_control_commands.days IS '调整天数，无对应参数时为零';
COMMENT ON COLUMN subscription_control_commands.reset_period IS '固定额度重置周期，无对应参数时为空';
COMMENT ON COLUMN subscription_control_commands.request_digest IS '规范命令SHA256摘要';
COMMENT ON COLUMN subscription_control_commands.status IS '已确定成功或业务拒绝';
COMMENT ON COLUMN subscription_control_commands.receipt IS '有界权威终态快照，不包含用户正文或凭据';
COMMENT ON COLUMN subscription_control_commands.finished_at IS '权威处理完成时间';
COMMENT ON COLUMN subscription_control_commands.created_at IS '命令回执登记时间';
CREATE INDEX IF NOT EXISTS idx_subscription_control_command_owner
 ON subscription_control_commands(app_id,user_id,group_id,created_at DESC,command_id);

CREATE OR REPLACE FUNCTION keep_subscription_control_receipt() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'SUBSCRIPTION_CONTROL_RECEIPT_IMMUTABLE' USING ERRCODE='23514';
END $$;
CREATE OR REPLACE TRIGGER subscription_control_receipt_immutable
 BEFORE UPDATE OR DELETE ON subscription_control_commands FOR EACH ROW
 EXECUTE FUNCTION keep_subscription_control_receipt();

CREATE TABLE IF NOT EXISTS subscription_control_cache_jobs (
 app_id VARCHAR(128) NOT NULL,
 command_id UUID NOT NULL,
 status VARCHAR(12) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','review','completed')),
 attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
 lease_id UUID,
 lease_until TIMESTAMPTZ,
 next_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 completed_at TIMESTAMPTZ,
 PRIMARY KEY(app_id,command_id),
 FOREIGN KEY(app_id,command_id) REFERENCES subscription_control_commands(app_id,command_id)
);
COMMENT ON TABLE subscription_control_cache_jobs IS '订阅事务提交后的缓存失效恢复依据，失败不重做业务命令';
COMMENT ON COLUMN subscription_control_cache_jobs.app_id IS '原命令集成应用';
COMMENT ON COLUMN subscription_control_cache_jobs.command_id IS '原固定命令标识';
COMMENT ON COLUMN subscription_control_cache_jobs.status IS '待处理、处理中、待人工核查或完成';
COMMENT ON COLUMN subscription_control_cache_jobs.attempts IS '有界失效尝试次数';
COMMENT ON COLUMN subscription_control_cache_jobs.lease_id IS '当前处理租约标识';
COMMENT ON COLUMN subscription_control_cache_jobs.lease_until IS '租约到期时间';
COMMENT ON COLUMN subscription_control_cache_jobs.next_at IS '下次可处理时间';
COMMENT ON COLUMN subscription_control_cache_jobs.updated_at IS '最近处理时间';
COMMENT ON COLUMN subscription_control_cache_jobs.completed_at IS '缓存失效确认时间';
CREATE INDEX IF NOT EXISTS idx_subscription_control_cache_due
 ON subscription_control_cache_jobs(next_at,app_id,command_id) WHERE status IN ('pending','running');
