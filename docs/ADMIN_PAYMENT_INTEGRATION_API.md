# ADMIN_PAYMENT_INTEGRATION_API

> 单文件中英双语文档 / Single-file bilingual documentation (Chinese + English)

---

### 签名调用函数 / Signed request helper

以下所有示例共用该函数。配置 `BASE`、`SUB2API_ADMIN_API_KEY`、`SUB2API_APP_ID`、`SUB2API_APP_SECRET`。每次重试重新签名，并保留相同业务的幂等键和请求体。

All examples below use this helper. Configure the four variables above. Sign each retry with a fresh nonce while preserving the same business idempotency key and body.

```bash
signed_admin() {
  local method="$1" path="$2" body="$3" idem="$4"
  local timestamp nonce body_hash canonical signature
  timestamp="$(date +%s)"
  nonce="$(openssl rand -hex 16)"
  body_hash="$(printf '%s' "$body" | openssl dgst -sha256 -hex | sed 's/^.* //')"
  canonical="$(printf '%s\n%s\n%s\n%s\n%s\n%s' "$method" "/api/v1/admin$path" "$SUB2API_APP_ID" "$timestamp" "$nonce" "$body_hash")"
  signature="$(printf '%s' "$canonical" | openssl dgst -sha256 -hmac "$SUB2API_APP_SECRET" -hex | sed 's/^.* //')"
  local args=(-X "$method" "${BASE}/api/v1/integrations/admin${path}"
    -H "x-api-key: $SUB2API_ADMIN_API_KEY" -H "X-App-Id: $SUB2API_APP_ID"
    -H "X-Timestamp: $timestamp" -H "X-Nonce: $nonce" -H "X-Signature: $signature")
  if [ -n "$idem" ]; then args+=(-H "Idempotency-Key: $idem"); fi
  if [ "$method" != GET ]; then args+=(-H 'Content-Type: application/json' --data-binary "$body"); fi
  curl "${args[@]}"
}
```

## 中文

### 目标
本文档用于对接外部支付系统（如 `sub2apipay`）与 Sub2API 的 Admin API，覆盖：
- 支付成功后充值
- 用户查询
- 人工余额修正
- 前端购买页参数透传

### 基础地址
- 生产：`https://<your-domain>`
- Beta：`http://<your-server-ip>:8084`

### 认证
所有外部调用必须经过 `/api/v1/integrations/admin/*`，同时提供：
- `x-api-key: admin-<64hex>`
- `X-App-Id`, `X-Timestamp`, `X-Nonce`, `X-Signature`
- `Content-Type: application/json`
- 幂等接口额外传：`Idempotency-Key`

管理员 JWT 用于原管理员入口。Admin API Key 不能独立使用；AppID / Secret 由管理员登录后生成，旧版带前缀凭据需重新生成。签名协议和迁移步骤见 [集成网关文档](ADMIN_INTEGRATION_GATEWAY_API.md)。

### 1) 一步完成创建并兑换
`POST /api/v1/integrations/admin/redeem-codes/create-and-redeem`

用途：原子完成“创建兑换码 + 兑换到指定用户”。

请求头：
- `x-api-key` + `X-App-Id` + `X-Timestamp` + `X-Nonce` + `X-Signature`
- `Idempotency-Key`

请求体示例：
```json
{
  "code": "s2p_cm1234567890",
  "type": "balance",
  "value": 100.0,
  "user_id": 123,
  "notes": "sub2apipay order: cm1234567890"
}
```

幂等语义：
- 同 `code` 且 `used_by` 一致：`200`
- 同 `code` 但 `used_by` 不一致：`409`
- 缺少 `Idempotency-Key`：`400`（`IDEMPOTENCY_KEY_REQUIRED`）

curl 示例：
```bash
signed_admin POST /redeem-codes/create-and-redeem \
  '{"code":"s2p_cm1234567890","type":"balance","value":100.00,"user_id":123,"notes":"sub2apipay order: cm1234567890"}' \
  pay-cm1234567890-success
```

### 2) 查询用户（可选前置校验）
`GET /api/v1/integrations/admin/users/:id`

```bash
signed_admin GET /users/123 '' ''
```

### 3) 余额调整（已有接口）
`POST /api/v1/integrations/admin/users/:id/balance`

用途：人工补偿 / 扣减，支持 `set` / `add` / `subtract`。

请求体示例（扣减）：
```json
{
  "balance": 100.0,
  "operation": "subtract",
  "notes": "manual correction"
}
```

```bash
signed_admin POST /users/123/balance \
  '{"balance":100.00,"operation":"subtract","notes":"manual correction"}' \
  balance-subtract-cm1234567890
```

### 4) 购买页 / 自定义页面 URL Query 透传（iframe / 新窗口一致）
当 Sub2API 打开 `purchase_subscription_url` 或用户侧自定义页面 iframe URL 时，会统一追加：
- `user_id`
- `token`
- `theme`（`light` / `dark`）
- `lang`（例如 `zh` / `en`，用于向嵌入页传递当前界面语言）
- `ui_mode`（固定 `embedded`）

示例：
```text
https://pay.example.com/pay?user_id=123&token=<jwt>&theme=light&lang=zh&ui_mode=embedded
```

### 5) 失败处理建议
- 支付成功与充值成功分状态落库
- 回调验签成功后立即标记“支付成功”
- 支付成功但充值失败的订单允许后续重试
- 重试保持相同 `code`、请求体和 `Idempotency-Key`，使用新的时间戳、Nonce 和签名

### 6) `doc_url` 配置建议
- 查看链接：`https://github.com/Wei-Shaw/sub2api/blob/main/docs/ADMIN_PAYMENT_INTEGRATION_API.md`
- 下载链接：`https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/docs/ADMIN_PAYMENT_INTEGRATION_API.md`

---

## English

### Purpose
This document describes the minimal Sub2API Admin API surface for external payment integrations (for example, `sub2apipay`), including:
- Recharge after payment success
- User lookup
- Manual balance correction
- Purchase page query parameter forwarding

### Base URL
- Production: `https://<your-domain>`
- Beta: `http://<your-server-ip>:8084`

### Authentication
All external calls must use `/api/v1/integrations/admin/*` with:
- `x-api-key: admin-<64hex>`
- `X-App-Id`, `X-Timestamp`, `X-Nonce`, `X-Signature`
- `Content-Type: application/json`
- `Idempotency-Key` for idempotent endpoints

Admin JWT is used for the original admin routes. Admin API Key cannot be used alone. Generate AppID / Secret after signing in as an administrator and rotate legacy prefixed credentials. See the [gateway signing and migration guide](ADMIN_INTEGRATION_GATEWAY_API.md).

### 1) Create and Redeem in one step
`POST /api/v1/integrations/admin/redeem-codes/create-and-redeem`

Use case: atomically create a redeem code and redeem it to a target user.

Headers:
- `x-api-key` + `X-App-Id` + `X-Timestamp` + `X-Nonce` + `X-Signature`
- `Idempotency-Key`

Request body:
```json
{
  "code": "s2p_cm1234567890",
  "type": "balance",
  "value": 100.0,
  "user_id": 123,
  "notes": "sub2apipay order: cm1234567890"
}
```

Idempotency behavior:
- Same `code` and same `used_by`: `200`
- Same `code` but different `used_by`: `409`
- Missing `Idempotency-Key`: `400` (`IDEMPOTENCY_KEY_REQUIRED`)

curl example:
```bash
signed_admin POST /redeem-codes/create-and-redeem \
  '{"code":"s2p_cm1234567890","type":"balance","value":100.00,"user_id":123,"notes":"sub2apipay order: cm1234567890"}' \
  pay-cm1234567890-success
```

### 2) Query User (optional pre-check)
`GET /api/v1/integrations/admin/users/:id`

```bash
signed_admin GET /users/123 '' ''
```

### 3) Balance Adjustment (existing API)
`POST /api/v1/integrations/admin/users/:id/balance`

Use case: manual correction with `set` / `add` / `subtract`.

Request body example (`subtract`):
```json
{
  "balance": 100.0,
  "operation": "subtract",
  "notes": "manual correction"
}
```

```bash
signed_admin POST /users/123/balance \
  '{"balance":100.00,"operation":"subtract","notes":"manual correction"}' \
  balance-subtract-cm1234567890
```

### 4) Purchase / Custom Page URL query forwarding (iframe and new tab)
When Sub2API opens `purchase_subscription_url` or a user-facing custom page iframe URL, it appends:
- `user_id`
- `token`
- `theme` (`light` / `dark`)
- `lang` (for example `zh` / `en`, used to pass the current UI language to the embedded page)
- `ui_mode` (fixed: `embedded`)

Example:
```text
https://pay.example.com/pay?user_id=123&token=<jwt>&theme=light&lang=zh&ui_mode=embedded
```

### 5) Failure handling recommendations
- Persist payment success and recharge success as separate states
- Mark payment as successful immediately after verified callback
- Allow retry for orders with payment success but recharge failure
- Keep the same `code`, body, and `Idempotency-Key` for retries; use a fresh timestamp, nonce, and signature

### 6) Recommended `doc_url`
- View URL: `https://github.com/Wei-Shaw/sub2api/blob/main/docs/ADMIN_PAYMENT_INTEGRATION_API.md`
- Download URL: `https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/docs/ADMIN_PAYMENT_INTEGRATION_API.md`
