# 第三方管理员集成网关 API

## 1. 用途

该网关供可信第三方系统调用现有管理员 API 使用，无需向第三方发放全局 Admin API Key。网关只负责签名验证、重放防护和请求转发，不复制或修改原管理员接口的业务逻辑。

允许的原管理员接口按以下规则映射：

```text
/api/v1/admin/<原路径>
        ->
/api/v1/integrations/admin/<原路径>
```

调用时必须保持原来的 HTTP 方法、查询参数、请求体和响应格式不变，只替换 URL 前缀。例如：

```text
POST /api/v1/admin/users/123/balance
POST /api/v1/integrations/admin/users/123/balance
```

因此现有用户、余额、订阅、分组、账户、渠道和报表等允许的管理员接口都可复用。

以下高风险接口不能经由网关调用：管理员 API Key 管理、网关凭据管理、备份、系统管理和清空审计日志。WebSocket、Step-Up 和 TOTP 交互操作仍须使用管理员 JWT。

## 2. 管理员配置网关凭据

管理员在“管理员设置 -> 第三方集成网关”中生成或轮换凭据。第三方凭据与全局 Admin API Key 独立。

| 字段 | 类型 | 描述 |
| --- | --- | --- |
| `integration_id` | string | 公开的集成标识，作为 `X-Integration-ID` 请求头传入。 |
| `signing_secret` | string | HMAC 签名密钥，仅在生成或轮换的响应中完整返回一次，不能作为请求头发送。 |

必须通过 HTTPS 传输。签名密钥应保存至第三方系统的密钥管理服务；一旦泄露，应立即轮换。

### 2.1 网关凭据管理接口

以下接口使用正常管理员认证（管理员 JWT 或全局 Admin API Key），不能经由第三方网关转发。

| 接口名称 | 方法与路径 | 用途 | 传入字段 | 响应 `data` 字段 |
| --- | --- | --- | --- | --- |
| 查询网关凭据状态 | `GET /api/v1/admin/settings/integration-admin` | 查询是否已配置，仅返回脱敏 ID。 | 无。 | `exists`：boolean，是否已配置；`masked_integration_id`：string，脱敏后的集成 ID。 |
| 生成或轮换网关凭据 | `POST /api/v1/admin/settings/integration-admin/regenerate` | 首次生成或轮换凭据。轮换后旧密钥立即失效。 | 无请求体。 | `integration_id`：string；`signing_secret`：string，仅本次响应返回。 |
| 删除网关凭据 | `DELETE /api/v1/admin/settings/integration-admin` | 删除凭据并立即停用网关。 | 无请求体。 | `message`：string，操作结果。 |

## 3. 请求头与签名

每个网关请求必须传入下列请求头。

| 请求头 | 类型 | 是否必填 | 描述 |
| --- | --- | --- | --- |
| `X-Integration-ID` | string | 是 | 管理员生成的集成标识。 |
| `X-Timestamp` | integer | 是 | 当前 Unix 秒级时间戳，服务端允许与当前时间相差 5 分钟。 |
| `X-Nonce` | string | 是 | 每个 HTTP 请求唯一的随机值，建议 UUID v4。服务端通过 Redis 保留 5 分钟，用于防重放。 |
| `X-Signature` | string | 是 | 小写十六进制 HMAC-SHA256 签名。 |
| `Idempotency-Key` | string | 写操作必填 | 幂等键。网络重试同一业务请求时复用；不同内容不可复用。 |
| `Content-Type` | string | JSON 请求必填 | 使用 `application/json`。 |

### 3.1 签名原文

对实际发送的请求体原始字节计算 SHA-256。不要在计算后解析、格式化或重新序列化 JSON。签名原文严格为六行：

```text
<HTTP_METHOD>
<原始 /api/v1/admin 路径，不含查询字符串>
<INTEGRATION_ID>
<UNIX_TIMESTAMP_SECONDS>
<NONCE>
<SHA256_HEX_OF_RAW_BODY>
```

其中第二行必须是原管理员路径 `/api/v1/admin/...`，不能使用网关路径，也不包含查询字符串。

```text
X-Signature = hex(HMAC-SHA256(signing_secret, canonical_string))
```

每次重试都必须使用新的 `X-Timestamp`、`X-Nonce` 和 `X-Signature`。仅在重试相同业务且请求内容相同时，才保持相同的 `Idempotency-Key`。

### 3.2 Node.js 调用示例

```js
import crypto from 'node:crypto'

const base = 'https://api.example.com'
const integrationId = process.env.SUB2API_INTEGRATION_ID
const secret = process.env.SUB2API_SIGNING_SECRET
const targetPath = '/api/v1/admin/users/provision'
const gatewayPath = '/api/v1/integrations/admin/users/provision'
const body = JSON.stringify({
  email: 'alice@example.com', password: 'change-me-123', username: 'alice',
  balance: 10,
  api_key: { name: 'default' },
  subscription: { group_id: 12, validity_days: 30, notes: 'order-1001' }
})
const timestamp = Math.floor(Date.now() / 1000).toString()
const nonce = crypto.randomUUID()
const bodyHash = crypto.createHash('sha256').update(body).digest('hex')
const canonical = ['POST', targetPath, integrationId, timestamp, nonce, bodyHash].join('\n')
const signature = crypto.createHmac('sha256', secret).update(canonical).digest('hex')

const response = await fetch(base + gatewayPath, {
  method: 'POST', body,
  headers: {
    'Content-Type': 'application/json', 'X-Integration-ID': integrationId,
    'X-Timestamp': timestamp, 'X-Nonce': nonce, 'X-Signature': signature,
    'Idempotency-Key': 'order-1001-provision'
  }
})
console.log(await response.json())
```

### 3.3 Python 调用示例

```python
import hashlib, hmac, json, os, time, uuid, requests

base = 'https://api.example.com'
integration_id = os.environ['SUB2API_INTEGRATION_ID']
secret = os.environ['SUB2API_SIGNING_SECRET']
target_path = '/api/v1/admin/users/123/balance'
gateway_path = '/api/v1/integrations/admin/users/123/balance'
body = json.dumps({'balance': 25, 'operation': 'add', 'notes': 'order-1001'}, separators=(',', ':'))
timestamp = str(int(time.time()))
nonce = str(uuid.uuid4())
body_hash = hashlib.sha256(body.encode()).hexdigest()
canonical = '\n'.join(['POST', target_path, integration_id, timestamp, nonce, body_hash])
signature = hmac.new(secret.encode(), canonical.encode(), hashlib.sha256).hexdigest()

r = requests.post(base + gateway_path, data=body, headers={
    'Content-Type': 'application/json', 'X-Integration-ID': integration_id,
    'X-Timestamp': timestamp, 'X-Nonce': nonce, 'X-Signature': signature,
    'Idempotency-Key': 'order-1001-credit'
})
print(r.json())
```

### 3.4 Go 调用示例

```go
body := []byte(`{"user_id":123,"group_id":12,"validity_days":30}`)
targetPath := "/api/v1/admin/subscriptions/assign"
timestamp := strconv.FormatInt(time.Now().Unix(), 10)
nonce := uuid.NewString()
bodyHash := sha256.Sum256(body)
canonical := strings.Join([]string{"POST", targetPath, integrationID, timestamp, nonce, hex.EncodeToString(bodyHash[:])}, "\n")
mac := hmac.New(sha256.New, []byte(secret))
mac.Write([]byte(canonical))
signature := hex.EncodeToString(mac.Sum(nil))
req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/integrations/admin/subscriptions/assign", bytes.NewReader(body))
req.Header.Set("Content-Type", "application/json")
req.Header.Set("X-Integration-ID", integrationID)
req.Header.Set("X-Timestamp", timestamp)
req.Header.Set("X-Nonce", nonce)
req.Header.Set("X-Signature", signature)
req.Header.Set("Idempotency-Key", "order-1001-subscription")
resp, err := http.DefaultClient.Do(req)
```

### 3.5 curl 和 OpenSSL 调用示例

```bash
BODY='{"balance":25,"operation":"add","notes":"order-1001"}'
TARGET='/api/v1/admin/users/123/balance'
GATEWAY='/api/v1/integrations/admin/users/123/balance'
TIMESTAMP="$(date +%s)"
NONCE="$(uuidgen | tr '[:upper:]' '[:lower:]')"
BODY_HASH="$(printf '%s' "$BODY" | openssl dgst -sha256 -hex | sed 's/^.* //')"
CANONICAL="POST
$TARGET
$SUB2API_INTEGRATION_ID
$TIMESTAMP
$NONCE
$BODY_HASH"
SIGNATURE="$(printf '%s' "$CANONICAL" | openssl dgst -sha256 -hmac "$SUB2API_SIGNING_SECRET" -hex | sed 's/^.* //')"

curl -X POST "${BASE}${GATEWAY}" \
  -H 'Content-Type: application/json' \
  -H "X-Integration-ID: $SUB2API_INTEGRATION_ID" \
  -H "X-Timestamp: $TIMESTAMP" \
  -H "X-Nonce: $NONCE" \
  -H "X-Signature: $SIGNATURE" \
  -H 'Idempotency-Key: order-1001-credit' \
  --data-binary "$BODY"
```

## 4. 一次性开通普通用户

### 接口

```text
POST /api/v1/integrations/admin/users/provision
```

原管理员路径为 `POST /api/v1/admin/users/provision`。用途：一次请求创建普通用户、用户 API Key、可选初始余额和可选订阅套餐。该接口是写操作，必须提供 `Idempotency-Key`。

### 4.1 请求字段

| 字段 | 类型 | 是否必填 | 描述 |
| --- | --- | --- | --- |
| `email` | string | 是 | 唯一邮箱地址。 |
| `password` | string | 是 | 登录密码，最少 6 个字符；响应不会返回该字段。 |
| `username` | string | 否 | 显示名称。 |
| `notes` | string | 否 | 管理备注。 |
| `concurrency` | integer | 否 | 用户并发上限，必须大于等于 0。 |
| `rpm_limit` | integer | 否 | 用户 RPM 上限，`0` 表示不限。 |
| `allowed_groups` | integer[] | 否 | 用户可用的专属分组 ID 列表。 |
| `balance` | number | 否 | 初始余额，必须大于等于 0。 |
| `api_key.name` | string | 是 | API Key 显示名称。 |
| `api_key.group_id` | integer | 否 | API Key 绑定的分组 ID。 |
| `api_key.custom_key` | string | 否 | 自定义 API Key；省略时自动生成。 |
| `api_key.quota` | number | 否 | Key 配额，`0` 表示不限。 |
| `api_key.expires_in_days` | integer | 否 | 有效天数，范围为 1-36500。 |
| `api_key.ip_whitelist` | string[] | 否 | 允许来源 IP/CIDR 列表。 |
| `api_key.ip_blacklist` | string[] | 否 | 拒绝来源 IP/CIDR 列表。 |
| `api_key.rate_limit_5h` | number | 否 | 5 小时滚动窗口请求上限，`0` 表示不限。 |
| `api_key.rate_limit_1d` | number | 否 | 1 天滚动窗口请求上限，`0` 表示不限。 |
| `api_key.rate_limit_7d` | number | 否 | 7 天滚动窗口请求上限，`0` 表示不限。 |
| `subscription.group_id` | integer | 否 | 订阅类型分组 ID；提供 `subscription` 时必填且必须大于 0。 |
| `subscription.validity_days` | integer | 否 | 订阅有效天数，范围为 1-36500，省略时默认 30 天。 |
| `subscription.notes` | string | 否 | 订阅备注。 |

### 4.2 请求示例

```json
{
  "email": "alice@example.com",
  "password": "change-me-123",
  "username": "alice",
  "notes": "订单 order-1001",
  "balance": 10,
  "concurrency": 5,
  "rpm_limit": 120,
  "allowed_groups": [12],
  "api_key": {
    "name": "default",
    "group_id": 12,
    "quota": 0,
    "expires_in_days": 30,
    "ip_whitelist": ["203.0.113.0/24"],
    "rate_limit_1d": 10000
  },
  "subscription": {
    "group_id": 12,
    "validity_days": 30,
    "notes": "订单 order-1001"
  }
}
```

成功响应的 `data` 包含 `user`、`api_key` 和 `subscription`。`api_key.key` 是完整的 API Key，应只在创建响应中安全保存；密码不会返回。创建用户后的订阅或 API Key 创建失败时，服务端会删除刚创建的用户，避免留下可用账户。

## 5. 常用既有管理员接口映射

| 原管理员接口 | 第三方网关接口 | 用途 | 请求字段 |
| --- | --- | --- | --- |
| `GET /api/v1/admin/users` | `GET /api/v1/integrations/admin/users` | 查询用户列表。 | 无 body，原查询参数原样透传。 |
| `POST /api/v1/admin/users/:id/balance` | `POST /api/v1/integrations/admin/users/:id/balance` | 设置、增加或扣减用户余额。 | `balance`：number，必须大于 0；`operation`：`set`、`add` 或 `subtract`；`notes`：string，可选。 |
| `POST /api/v1/admin/subscriptions/assign` | `POST /api/v1/integrations/admin/subscriptions/assign` | 向已有用户分配订阅套餐。 | 保持该原接口的字段和格式不变。 |
| `POST /api/v1/admin/users/provision` | `POST /api/v1/integrations/admin/users/provision` | 一次性创建用户、API Key、余额和订阅。 | 见第 4 节。 |

除第 1 节列出的禁止路径外，其他现有管理员接口均可依照相同映射规则调用。

## 6. 常见错误

| HTTP 状态码 | 错误码或原因 | 含义 |
| --- | --- | --- |
| 401 | `INTEGRATION_AUTH_FAILED` | 缺少身份请求头，或 Integration ID 不存在、不匹配、已停用。 |
| 401 | `INTEGRATION_SIGNATURE_INVALID` | 方法、原路径、请求头或原始请求体与签名不匹配。 |
| 401 | `INTEGRATION_TIMESTAMP_EXPIRED` | 时间戳超过服务端 5 分钟允许窗口。 |
| 403 | `INTEGRATION_ROUTE_FORBIDDEN` | 请求了禁止第三方调用的管理员路径。 |
| 409 | `INTEGRATION_NONCE_REPLAYED` | Nonce 已使用，判定为重放请求。 |
| 409 | 幂等冲突 | 相同 `Idempotency-Key` 被用于不同请求内容。 |
| 503 | `INTEGRATION_NONCE_STORE_UNAVAILABLE` | Redis 不可用，服务端拒绝请求以确保重放防护不会失效。 |
