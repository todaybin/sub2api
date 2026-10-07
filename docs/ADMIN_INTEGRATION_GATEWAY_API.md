# 第三方管理员集成网关 API

## 1. 用途

该网关供可信第三方系统调用现有管理员 API 使用，必须同时提供全局 Admin API Key 和 AppID / Secret 签名。网关只负责签名验证、重放防护和请求转发，不复制或修改原管理员接口的业务逻辑。

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

管理员在“管理员设置 -> 第三方集成网关”中生成或轮换凭据。AppID / Secret 必须配合全局 Admin API Key 使用，缺少任一认证要素都会被拒绝。

| 字段 | 类型 | 描述 |
| --- | --- | --- |
| `appid` | string | 32 位小写十六进制 AppID（16 个随机字节，无前缀），作为 `X-App-Id` 请求头传入。 |
| `secret` | string | 64 位小写十六进制 HMAC 签名密钥（32 个随机字节，无前缀），仅在生成或轮换的响应中完整返回一次，不能作为请求头发送。 |

必须通过 HTTPS 传输。签名密钥应保存至第三方系统的密钥管理服务；一旦泄露，应立即轮换。

### 2.1 网关凭据管理接口

以下接口使用管理员 JWT 认证，不能经由第三方网关转发。

| 接口名称 | 方法与路径 | 用途 | 传入字段 | 响应 `data` 字段 |
| --- | --- | --- | --- | --- |
| 查询网关凭据状态 | `GET /api/v1/admin/settings/integration-admin` | 查询是否已配置，仅返回脱敏 AppID。 | 无。 | `exists`：boolean，是否已配置；`masked_appid`：string，脱敏后的 AppID。 |
| 生成或轮换网关凭据 | `POST /api/v1/admin/settings/integration-admin/regenerate` | 首次生成或轮换凭据。轮换后原凭据立即失效。 | 无请求体。 | `appid`：string；`secret`：string，仅本次响应返回。 |
| 删除网关凭据 | `DELETE /api/v1/admin/settings/integration-admin` | 删除凭据并立即停用网关。 | 无请求体。 | `message`：string，操作结果。 |

## 3. 请求头与签名

每个网关请求必须传入下列请求头。

| 请求头 | 类型 | 是否必填 | 描述 |
| --- | --- | --- | --- |
| `x-api-key` | string | 是 | 当前全局 Admin API Key，不能独立使用。 |
| `X-App-Id` | string | 是 | 管理员生成的集成标识。 |
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
<APPID>
<UNIX_TIMESTAMP_SECONDS>
<NONCE>
<SHA256_HEX_OF_RAW_BODY>
```

其中第二行必须是原管理员路径 `/api/v1/admin/...`，不能使用网关路径，也不包含查询字符串。

```text
X-Signature = hex(HMAC-SHA256(secret, canonical_string))
```

每次重试都必须使用新的 `X-Timestamp`、`X-Nonce` 和 `X-Signature`。仅在重试相同业务且请求内容相同时，才保持相同的 `Idempotency-Key`。

### 3.2 Node.js 调用示例

以下为 Node.js 18+ 的通用函数；保存为 `.mjs`，配置三个环境变量后调用。`base` 修改为实际服务地址。

```js
import crypto from 'node:crypto'

const base = 'https://api.example.com'
const appid = process.env.SUB2API_APP_ID
const secret = process.env.SUB2API_APP_SECRET
const adminApiKey = process.env.SUB2API_ADMIN_API_KEY
if (!appid || !secret || !adminApiKey) throw new Error('缺少集成凭据环境变量')

async function callAdmin(method, targetPath, { body, query, idempotencyKey } = {}) {
  method = method.toUpperCase()
  if (!targetPath.startsWith('/api/v1/admin/') || /[?#]/.test(targetPath)) {
    throw new Error('targetPath 必须是原管理员路径，不含查询字符串')
  }
  const isRead = method === 'GET' || method === 'HEAD'
  if (!isRead && !idempotencyKey) throw new Error('写操作必须提供幂等键')
  const rawBody = isRead ? '' : JSON.stringify(body ?? {})
  const timestamp = Math.floor(Date.now() / 1000).toString()
  const nonce = crypto.randomUUID()
  const bodyHash = crypto.createHash('sha256').update(rawBody).digest('hex')
  const canonical = [method, targetPath, appid, timestamp, nonce, bodyHash].join('\n')
  const signature = crypto.createHmac('sha256', secret).update(canonical).digest('hex')
  const url = new URL(targetPath.replace('/api/v1/admin/', '/api/v1/integrations/admin/'), base)
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value !== undefined && value !== null) url.searchParams.set(key, String(value))
  }
  const headers = {
    'X-App-Id': appid,
    'x-api-key': adminApiKey,
    'X-Timestamp': timestamp, 'X-Nonce': nonce, 'X-Signature': signature
  }
  if (!isRead) headers['Content-Type'] = 'application/json'
  if (idempotencyKey) headers['Idempotency-Key'] = idempotencyKey
  const response = await fetch(url, {
    method, headers, body: isRead ? undefined : rawBody
  })
  const result = await response.json()
  if (!response.ok || result.code !== 0) {
    throw new Error(`HTTP ${response.status}: ${result.message ?? '请求失败'}`)
  }
  return result.data
}

const provisioned = await callAdmin('POST', '/api/v1/admin/users/provision', {
  body: {
    external_id: 'partner:alice-1001',
    email: 'alice@example.com', password: 'change-me-123', username: 'alice',
    balance: 10,
    api_key: { name: 'default-smart' },
    subscription: { group_id: 12, validity_days: 30, notes: 'order-1001' }
  },
  idempotencyKey: 'order-1001-provision'
})
// 首次新增 Key 时，将密钥保存到可信后端；不要打印完整响应。
console.log({ userId: provisioned.user.id })
```

### 3.3 Python 调用示例

```python
import hashlib, hmac, json, os, time, uuid, requests

base = 'https://api.example.com'
appid = os.environ['SUB2API_APP_ID']
secret = os.environ['SUB2API_APP_SECRET']
admin_api_key = os.environ['SUB2API_ADMIN_API_KEY']
target_path = '/api/v1/admin/users/123/balance'
gateway_path = '/api/v1/integrations/admin/users/123/balance'
body = json.dumps({'balance': 25, 'operation': 'add', 'notes': 'order-1001'}, separators=(',', ':'))
timestamp = str(int(time.time()))
nonce = str(uuid.uuid4())
body_hash = hashlib.sha256(body.encode()).hexdigest()
canonical = '\n'.join(['POST', target_path, appid, timestamp, nonce, body_hash])
signature = hmac.new(secret.encode(), canonical.encode(), hashlib.sha256).hexdigest()

r = requests.post(base + gateway_path, data=body, headers={
    'Content-Type': 'application/json', 'X-App-Id': appid,
    'x-api-key': admin_api_key,
    'X-Timestamp': timestamp, 'X-Nonce': nonce, 'X-Signature': signature,
    'Idempotency-Key': 'order-1001-credit'
})
print(r.json())
```

### 3.4 Go 调用示例

```go
appid := os.Getenv("SUB2API_APP_ID")
secret := os.Getenv("SUB2API_APP_SECRET")
adminAPIKey := os.Getenv("SUB2API_ADMIN_API_KEY")
body := []byte(`{"user_id":123,"group_id":12,"validity_days":30}`)
targetPath := "/api/v1/admin/subscriptions/assign"
timestamp := strconv.FormatInt(time.Now().Unix(), 10)
nonce := uuid.NewString()
bodyHash := sha256.Sum256(body)
canonical := strings.Join([]string{"POST", targetPath, appid, timestamp, nonce, hex.EncodeToString(bodyHash[:])}, "\n")
mac := hmac.New(sha256.New, []byte(secret))
mac.Write([]byte(canonical))
signature := hex.EncodeToString(mac.Sum(nil))
req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/integrations/admin/subscriptions/assign", bytes.NewReader(body))
req.Header.Set("Content-Type", "application/json")
req.Header.Set("X-App-Id", appid)
req.Header.Set("x-api-key", adminAPIKey)
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
$SUB2API_APP_ID
$TIMESTAMP
$NONCE
$BODY_HASH"
SIGNATURE="$(printf '%s' "$CANONICAL" | openssl dgst -sha256 -hmac "$SUB2API_APP_SECRET" -hex | sed 's/^.* //')"

curl -X POST "${BASE}${GATEWAY}" \
  -H 'Content-Type: application/json' \
  -H "x-api-key: $SUB2API_ADMIN_API_KEY" \
  -H "X-App-Id: $SUB2API_APP_ID" \
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

原管理员路径为 `POST /api/v1/admin/users/provision`。用途：幂等确保普通用户存在、合并可用模型分组，并为缺失分组创建独立 API Key。调用方应使用稳定的 `external_id`，并通过 `api_keys` 定义需要创建的 Key。该接口是写操作，必须提供 `Idempotency-Key`。

重复调用时先按 `external_id` 查找账号，未找到时才按邮箱精确认领。若命中多个 `external_id`、邮箱已属于管理员账号，或同一请求重复声明分组，返回冲突或参数错误。已存在且有效、路由模式及分组集合相同的 Key 不会重复创建，也不会再次返回密钥明文；新增 Key 的明文只在本次响应返回。

### 4.1 请求字段

| 字段 | 类型 | 是否必填 | 描述 |
| --- | --- | --- | --- |
| `external_id` | string | 建议必填 | 第三方系统稳定账号标识，最长 160 字符，只允许字母、数字、点、下划线、冒号和连字符。 |
| `email` | string | 是 | 唯一邮箱地址。 |
| `password` | string | 是 | 登录密码，最少 6 个字符；响应不会返回该字段。 |
| `username` | string | 否 | 显示名称。 |
| `notes` | string | 否 | 管理备注。 |
| `concurrency` | integer | 否 | 用户并发上限，必须大于等于 0。 |
| `rpm_limit` | integer | 否 | 用户 RPM 上限，`0` 表示不限。 |
| `allowed_groups` | integer[] | 否 | 用户可用的专属分组 ID 列表。 |
| `balance` | number | 否 | 初始余额，必须大于等于 0。 |
| `api_key.name` | string | 是 | API Key 显示名称。 |
| `api_key.group_id` | integer | 否 | 大于 0；提供时创建 `single` 单分组 Key，省略时默认创建 `smart` 智能 Key。 |
| `api_key.routing_mode` | string | 否 | `smart` 或 `single`；与 `group_id` 必须一致。 |
| `api_key.routing_strategy` | string | 否 | `auto`（默认）、`sequential`（按顺序）、`price`、`speed`、`random`。`auto` 自动使用当前账号全部可用分组。 |
| `api_key.smart_group_ids` | integer[] | 按策略 | `auto` 不需要传，系统自动使用该用户当前有权使用的全部有效分组；`sequential` 必须传入至少一个分组并严格按数组顺序尝试；`price`、`speed`、`random` 可传入以限制候选分组，省略时使用该用户当前有权使用的全部有效分组。候选集合在创建时固定，不会随新分组自动扩展。与 `group_id` 互斥。 |
| `api_key.custom_key` | string | 否 | 自定义 API Key；省略时自动生成。 |
| `api_key.quota` | number | 否 | Key 配额，`0` 表示不限。 |
| `api_key.expires_in_days` | integer | 否 | 有效天数，范围为 1-36500。 |
| `api_key.ip_whitelist` | string[] | 否 | 允许来源 IP/CIDR 列表。 |
| `api_key.ip_blacklist` | string[] | 否 | 拒绝来源 IP/CIDR 列表。 |
| `api_key.rate_limit_5h` | number | 否 | 5 小时滚动窗口费用上限（USD），`0` 表示不限。 |
| `api_key.rate_limit_1d` | number | 否 | 1 天滚动窗口费用上限（USD），`0` 表示不限。 |
| `api_key.rate_limit_7d` | number | 否 | 7 天滚动窗口费用上限（USD），`0` 表示不限。 |
| `api_keys` | object[] | 否 | 批量 Key 定义，元素字段与 `api_key` 相同；`api_key` 与 `api_keys` 至少提供一个。一个请求内相同路由与候选分组集合只能出现一次。 |
| `subscription.group_id` | integer | 否 | 订阅类型分组 ID；提供 `subscription` 时必填且必须大于 0。 |
| `subscription.validity_days` | integer | 否 | 订阅有效天数，范围为 1-36500，省略时默认 30 天。 |
| `subscription.notes` | string | 否 | 订阅备注。 |

### 4.2 请求示例

```json
{
  "external_id": "workmesh:site-1001",
  "email": "alice@example.com",
  "password": "change-me-123",
  "username": "alice",
  "notes": "订单 order-1001",
  "balance": 10,
  "concurrency": 5,
  "rpm_limit": 120,
  "allowed_groups": [12],
  "api_keys": [
    {
      "name": "workmesh-group-12",
      "group_id": 12,
      "quota": 0,
      "ip_whitelist": ["203.0.113.0/24"],
      "rate_limit_1d": 10000
    },
    {
      "name": "workmesh-group-18",
      "group_id": 18,
      "quota": 0
    }
  ],
  "subscription": {
    "group_id": 12,
    "validity_days": 30,
    "notes": "订单 order-1001"
  }
}
```

成功响应的 `data` 包含 `created`、`external_id`、`user`、`api_keys` 和 `subscription`。每个 `api_keys[]` 项包含 `routing_mode`、`routing_strategy`、`smart_group_ids`（智能模式）、`created` 和 `key_fingerprint`，只有 `created=true` 时才包含完整 `key`；同一个幂等请求的重放会恢复原成功响应，包括该密钥。密码不会返回。创建用户后的订阅或 API Key 创建失败时，服务端会删除刚创建的用户；更新已有用户失败时会删除本轮新 Key 并恢复原分组和备注。

## 5. 账号、余额、Key、登录 Token 与用量接口

本节网关接口均要求第 3 节的完整联合认证。响应统一为 HTTP 200：

```json
{"code":0,"message":"success","data":{}}
```

下列响应示例仅展示 `data` 中的关键字段；账号、Key 响应还包含时间、状态及其他管理字段。账号指平台普通用户，与 `/accounts` 中承载上游模型服务的供应商账号不同。登录 JWT、模型 API Key、Token 用量也分别是三种概念。

| 用途 | 方法与网关路径 | 幂等键 |
| --- | --- | --- |
| 创建普通账号 | `POST /api/v1/integrations/admin/users` | 必填 |
| 获取账号信息 | `GET /api/v1/integrations/admin/users/:id` | 无 |
| 查询账号列表 | `GET /api/v1/integrations/admin/users` | 无 |
| 增加余额 | `POST /api/v1/integrations/admin/users/:id/balance` | 必填 |
| 新增 Key | `POST /api/v1/integrations/admin/users/:id/api-keys` | 必填 |
| 查询账号 Keys | `GET /api/v1/integrations/admin/users/:id/api-keys` | 无 |
| 签发用户登录 Token | `POST /api/v1/integrations/admin/users/:id/token` | 必填 |
| 查询账号或指定 Key 用量 | `GET /api/v1/integrations/admin/users/:id/usage` | 无 |
| 查询用户或指定 Key 的模型 | `GET /api/v1/integrations/admin/users/:id/models` | 无 |
| 查询分组模型并集 | `GET /api/v1/integrations/admin/groups/models` | 无 |
| 一次性开通账号与 Keys | `POST /api/v1/integrations/admin/users/provision` | 必填，见第 4 节 |

`:id` 为账号创建响应中的 `data.id`，或 provision 响应中的 `data.user.id`，必须使用数字 ID；`external_id` 用于 provision 的认领，不直接替代路径 ID。

### 5.1 创建账号

```http
POST /api/v1/integrations/admin/users
Content-Type: application/json
Idempotency-Key: create-user-order-1001
```

```json
{
  "email":"alice@example.com",
  "password":"change-me-123",
  "username":"alice",
  "role":"user",
  "balance":0,
  "concurrency":5,
  "rpm_limit":120,
  "allowed_groups":[12,18],
  "restrict_public_groups":true,
  "notes":"订单 order-1001"
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `email` | 是 | 唯一邮箱，格式必须有效；重复邮箱返回冲突，不认领已有账号。 |
| `password` | 是 | 至少 6 字符；响应不返回密码或密码哈希。 |
| `username`、`notes` | 否 | 显示名称、管理员备注。 |
| `role` | 否 | 省略默认 `user`。集成开通普通用户使用 `user`；管理员操作存在额外认证要求。 |
| `balance` | 否 | 初始余额（USD），建议提供非负数；增量充值使用 5.3 接口。 |
| `concurrency` | 否 | 并发上限；省略或 0 时采用后端默认值。 |
| `rpm_limit` | 否 | 每分钟请求上限，0 表示不限。 |
| `allowed_groups` | 否 | 授予的分组 ID 列表。单独新增 Key 不自动授予分组权限。 |
| `restrict_public_groups` | 否 | 默认 false：公开标准分组也可使用；true：公开分组也必须在授权列表中。订阅组仍须有有效订阅。 |

响应 `data` 示例：

```json
{"id":123,"email":"alice@example.com","username":"alice","role":"user","balance":0,"concurrency":5,"rpm_limit":120,"status":"active","allowed_groups":[12,18],"restrict_public_groups":true}
```

该接口只创建账号。需要同时确保账号和 Key 时使用 provision；provision 认领已有账号后不会覆盖已有密码、余额、名称或并发设置，充值仍须单独调用余额接口。

### 5.2 获取账号信息与 Keys

```text
GET /api/v1/integrations/admin/users/123
GET /api/v1/integrations/admin/users/123/api-keys?page=1&page_size=20
GET /api/v1/integrations/admin/users?search=alice%40example.com&page=1&page_size=20
```

账号详情返回上述账号对象，还包含 `frozen_balance`、`created_at`、`updated_at` 等字段。不存在的账号返回 404。用户列表的 `search` 是模糊匹配，不能直接把第一条结果当作邮箱精确匹配。

Keys 和用户列表使用分页结构：

```json
{"items":[],"total":0,"page":1,"page_size":20,"pages":0}
```

Keys 列表含 `id`、`user_id`、`name`、`routing_mode`、`routing_strategy`、`group_id`、`smart_group_ids`、`status`、额度及到期信息。该管理员接口可能包含 Key 明文，应只在可信后端调用。

### 5.3 增加余额

```http
POST /api/v1/integrations/admin/users/123/balance
Idempotency-Key: recharge-order-1002
```

```json
{"balance":10,"operation":"add","notes":"支付订单 order-1002"}
```

`balance` 是本次操作金额（USD），必须大于 0；`operation=add` 增加余额，`subtract` 扣减，`set` 设置为指定余额。响应 `data` 是修改后的完整账号对象。假设原余额为 3，上例响应 `balance=13`；相同幂等键重试不会再次增加 10。

订单系统应保存充值订单号与幂等键的对应关系。每个新订单使用新幂等键；发生超时后复用同一幂等键和原 body，并生成新的时间戳、Nonce 和签名。更多支付接口见 [支付集成文档](ADMIN_PAYMENT_INTEGRATION_API.md)。

### 5.4 新增 Key（默认智能分组）

```http
POST /api/v1/integrations/admin/users/123/api-keys
Idempotency-Key: create-key-user-123-001
```

最小请求：

```json
{"name":"default-smart"}
```

不传 `group_id` 默认创建 `routing_mode=smart`、`routing_strategy=auto`，候选分组为创建时该用户有权限绑定的全部有效分组。这是一份固定集合，后续新增分组不会自动加入。没有可用分组时返回 400，不创建无分组 Key。使用 `routing_strategy=sequential` 时必须传 `smart_group_ids`，服务端严格保留数组顺序并按该顺序尝试分组。

指定智能候选分组及规则：

```json
{"name":"smart-price","routing_mode":"smart","routing_strategy":"price","smart_group_ids":[12,18],"quota":50,"expires_in_days":30}
```

显式绑定单分组：

```json
{"name":"group-12","group_id":12,"quota":50}
```

请求字段与第 4 节 `api_key` 字段相同，`name` 必填。`group_id` 必须大于 0；指定后模式为 `single`。`smart_group_ids` 与 `group_id` 互斥，所有分组 ID 必须大于 0、有效且获授权。订阅组需要该用户的有效订阅。重复的智能候选 ID 会去重。

`quota` 和 `rate_limit_5h/1d/7d` 都以 USD 计量，0 表示不限；这些字段不是 Token 或请求次数上限。`expires_in_days` 可为 1–36500，省略则不设过期。IP 黑白名单接受 IP/CIDR。

响应 `data` 示例：

```json
{"id":456,"user_id":123,"name":"default-smart","key":"sk-example-replace-me","routing_mode":"smart","routing_strategy":"auto","group_id":null,"smart_group_ids":[12,18],"status":"active","quota":0,"quota_used":0,"expires_at":null}
```

该接口创建新的 Key；每个新的业务操作应使用新的幂等键，同一幂等键的重试返回原结果。provision 用于确保目标路由模式及候选分组集合存在有效 Key。

策略说明：`auto` 使用账号当前全部可用分组并按系统默认调度规则选择；`sequential` 严格按 `smart_group_ids` 顺序依次尝试；`price` 比较预估用户费用；`speed` 优先有样本的低首字延迟线路；`random` 在有容量的候选分组间等概率选择。会话和续传优先遵守原线路归属。实际计费按最终选中的分组记录。详见 [智能路由规则](SMART_ROUTING.md)。

### 5.5 签发用户登录 Token（JWT）

```http
POST /api/v1/integrations/admin/users/123/token
Idempotency-Key: login-user-123-session-001
```

请求体必须为 JSON，可以传 `{}`。需要绑定最终用户网络指纹时传入：

```json
{"client_ip":"203.0.113.5","user_agent":"Mozilla/5.0 ..."}
```

两个字段必须同时提供或同时省略；`client_ip` 为 IPv4/IPv6 地址，`user_agent` 最长 512 字节。Token 不会自动绑定集成服务端的 IP/UA。提供时必须填写最终用户在浏览器请求中使用的真实 IP 与 UA，否则开启会话绑定后会校验失败；省略时签发不带指纹绑定的会话。

仅允许活跃普通用户；管理员、停用用户、启用 TOTP 的用户返回 403，TOTP 用户应走正常登录验证。响应头包含 `Cache-Control: no-store`，日志只记录调用者与目标账号 ID，不记录 Token。

```json
{"user_id":123,"access_token":"<JWT>","refresh_token":"<REFRESH_TOKEN>","expires_in":900,"token_type":"Bearer"}
```

`expires_in` 是 Access Token 有效秒数，实际值取系统配置，示例 900 不代表固定期限。此接口签发新会话，不读取用户以前的 Token。相同幂等键会返回原会话，开启新会话须使用新幂等键。Token 在重放时仍沿用原签发时间和到期时间。

使用 `Authorization: Bearer <access_token>` 调用 `/api/v1/user`、`/api/v1/usage/stats` 等普通用户接口。刷新使用 `POST /api/v1/auth/refresh`，body 为 `{"refresh_token":"..."}`，刷新后保存新 Token 对。JWT 用于登录管理面；调用 `/v1/chat/completions` 等模型接口使用 Key，不使用登录 JWT。

### 5.6 查询账号或指定 Key 的 Token 用量

```text
GET /api/v1/integrations/admin/users/123/usage?start_date=2026-10-01&end_date=2026-10-06&timezone=Asia%2FShanghai
GET /api/v1/integrations/admin/users/123/usage?api_key_id=456&start_time=2026-10-01T00%3A00%3A00Z&end_time=2026-10-02T00%3A00%3A00Z
```

| 查询参数 | 说明 |
| --- | --- |
| `api_key_id` | 可选，正整数；省略汇总该账号所有 Keys，提供时只统计该 Key。该 Key 必须属于路径账号，其他用户的 Key 返回 404。 |
| `start_time`、`end_time` | 成对提供，RFC3339（含时区或 `Z`），按 `[start_time,end_time)` 查询；开始必须早于结束。 |
| `start_date`、`end_date` | 成对提供，`YYYY-MM-DD`，包含起止两个自然日；结束日转换成下一日 00:00 的排除边界。不能与 time 参数混用。 |
| `timezone` | 日期和相对时间的 IANA 时区，如 `Asia/Shanghai`；默认 `UTC`，非法时区返回 400。RFC3339 时间按其自身时区解释。 |
| `period` | 没有显式起止范围时使用：`today`、`week`（最近 7 天）、`month`（最近一个月，默认）、`all`（全部历史）。显式范围优先。 |

带正偏移的 RFC3339 值须 URL 编码 `+` 为 `%2B`。日期查询使用自然日边界，在夏令时切换时也正确处理下一日零点。只传一侧时间、日期格式非法、范围倒置或无效 period 返回 400。

响应 `data` 示例：

```json
{
  "user_id":123,
  "api_key_id":456,
  "start_time":"2026-10-01T00:00:00Z",
  "end_time":"2026-10-02T00:00:00Z",
  "total_requests":2,
  "total_input_tokens":100,
  "total_output_tokens":25,
  "total_cache_creation_tokens":4,
  "total_cache_read_tokens":6,
  "total_cache_tokens":10,
  "total_tokens":135,
  "total_cost":0.15,
  "total_actual_cost":0.12,
  "total_account_cost":0.08,
  "average_duration_ms":800
}
```

`total_tokens` 是输入、输出、缓存创建和缓存读取四项之和；`total_cache_tokens` 为后两项之和。`total_cost` 是标准计费，`total_actual_cost` 是用户实际扣费，`total_account_cost` 是内部上游成本（管理员可见），费用单位均为 USD。未传 Key 时不返回 `api_key_id`。无记录返回真实零统计，不是固定占位数据。统计来自已写入用量日志，刚完成的异步写入可能有短暂延迟。

### 5.7 当前登录账号的用量

用户持有 JWT 时，可以直接查询自己，不需要知道账号 ID，也不需要管理员网关凭据：

```http
GET /api/v1/usage/stats?start_time=2026-10-01T00%3A00%3A00Z&end_time=2026-10-02T00%3A00%3A00Z
Authorization: Bearer <access_token>
```

也支持 `api_key_id`、`start_date`、`end_date`、`timezone`、`period=today|week|month`。账号始终取 JWT 身份，传入 `user_id` 不会改变统计对象；指定其他账号的 Key 返回 403。未指定范围和 period 时沿用用户面板默认范围：用户时区最近 7 天起始日零点至明日零点。用户日期参数允许单侧范围；完整日期范围按包含结束自然日处理。省略 timezone 沿用系统默认时区。

响应字段与 5.6 的统计指标相同，但不含管理员内部账号成本、上游路径及额外的 `user_id/start_time/end_time` 字段。需要详细请求记录时调用 `GET /api/v1/usage`，使用相同时间过滤与分页参数。该用户接口接受登录 JWT；模型 API Key 的标准模型列表入口见下节。

### 5.8 分组模型列表与同名去重

管理员查询全部有效分组的模型并集，或指定一组分组：

```text
GET /api/v1/integrations/admin/groups/models
GET /api/v1/integrations/admin/groups/models?group_ids=12,18
```

`group_ids` 是可选的逗号分隔正整数 ID；非法值返回 400，找不到的分组返回 404。空模型集合返回空数组，不补充默认模型。

查询某账号有权使用的分组模型，或进一步限定到其指定 Key：

```text
GET /api/v1/integrations/admin/users/123/models
GET /api/v1/integrations/admin/users/123/models?api_key_id=456
```

响应 `data` 示例：

```json
{"object":"list","models":[{"id":"gpt-5","object":"model","group_ids":[12,18]},{"id":"gpt-5-mini","object":"model","group_ids":[18]}]}
```

同一个公开模型 ID（完全相同、区分大小写）只返回一次，`group_ids` 合并来源分组并按数值排序，模型按 ID 排序。公开别名不同则保留多条；不会按显示名称模糊合并。按账号查询只包含当前有权限的分组；指定 Key 时进一步取 Key 候选分组与当前权限的交集。分组白名单、模型映射和持久有效账号参与目录计算，临时冷却或并发占满不会改变目录。

模型客户端直接用 Key 获取 OpenAI 格式列表：

```http
GET /v1/models
Authorization: Bearer <model-api-key>
```

这里使用标准响应 `{"object":"list","data":[...]}`，不是管理员 `code/message/data` 包装。智能 Key 同样按公开模型 ID 去重；返回列表不预先选择计费分组，发送模型请求时才按策略选择。管理员 `/groups/:id/model-allowlist-candidates` 是白名单编辑候选值，不等同于实际可用模型目录。

### 5.9 推荐接入流程与重试

1. 第三方后端保存 Admin API Key、AppID、Secret，并为每个请求生成新 Nonce 与签名。
2. 用稳定 `external_id` 调用 provision，`api_key={"name":"default-smart"}` 即可默认智能分组；保存 `user.id` 和首次创建返回的 Key。订阅组首次开通先分配订阅，再创建 Key。
3. 后续充值调用余额 `operation=add`，用订单号生成固定幂等键。
4. 额外创建 Key 使用 5.4；指定分组传 `group_id`，智能模式传 `smart_group_ids` 或省略自动取当前授权集合。
5. 登录第三方用户面板时调用 5.5 获取 JWT；用 JWT 查询当前账号用量，或由可信后端用 5.6 查询指定账号/Key。
6. 展示模型使用账号/Key 模型目录；实际模型客户端可直接调用 `/v1/models`。

第 3.2 节的 `callAdmin` 对本节所有接口通用：输入原路径 `/api/v1/admin/...` 计算签名，然后请求对应网关 URL；查询参数保留在实际 URL 中，不加入签名路径，GET 使用空请求体。以下代码放在同一 `.mjs` 文件中，演示单独创建账号后充值、创建 Key、签发登录会话及查询：

```javascript
// 与 provision 示例择一创建账号；12、18 替换为系统中的有效分组 ID。
const createdUser = await callAdmin('POST', '/api/v1/admin/users', {
  body: { email: 'bob@example.com', password: 'change-me-456', allowed_groups: [12, 18] },
  idempotencyKey: 'partner-user-bob-1002'
})
const userPath = `/api/v1/admin/users/${createdUser.id}`
const account = await callAdmin('GET', userPath)
await callAdmin('POST', `${userPath}/balance`, {
  body: { balance: 10, operation: 'add', notes: 'order-1002' },
  idempotencyKey: 'order-1002'
})
const createdKey = await callAdmin('POST', `${userPath}/api-keys`, {
  body: { name: 'default-smart' }, idempotencyKey: 'bob-key-001'
})
const login = await callAdmin('POST', `${userPath}/token`, {
  body: {}, idempotencyKey: 'bob-login-session-001'
})
const timeQuery = {
  start_date: '2026-10-01', end_date: '2026-10-06', timezone: 'Asia/Shanghai'
}
const accountUsage = await callAdmin('GET', `${userPath}/usage`, { query: timeQuery })
const keyUsage = await callAdmin('GET', `${userPath}/usage`, {
  query: { ...timeQuery, api_key_id: createdKey.id }
})
const models = await callAdmin('GET', `${userPath}/models`, {
  query: { api_key_id: createdKey.id }
})
const groupModels = await callAdmin('GET', '/api/v1/admin/groups/models', {
  query: { group_ids: '12,18' }
})
// 当前账号使用登录 JWT 查询；此请求不走管理员网关。
const currentResponse = await fetch(new URL(`/api/v1/usage/stats?${new URLSearchParams(timeQuery)}`, base), {
  headers: { Authorization: `Bearer ${login.access_token}` }
})
const currentUsage = await currentResponse.json()
if (!currentResponse.ok || currentUsage.code !== 0) throw new Error('当前账号用量查询失败')
// account、createdKey.key、login 和统计结果由业务后端按需要保存或使用。
```

成功幂等重放响应头为 `X-Idempotency-Replayed: true`。相同幂等键用于不同 body 返回 409。幂等记录有保留期限，超过期限不保证继续阻止新执行；充值应由订单系统长期保存处理状态，避免过期后重复处理充值请求。

集成 Key 创建、provision 与登录会话的幂等响应使用 AES-256-GCM 加密保存，重试可取回首次返回的密钥或 Token。加密密钥从持久 JWT Secret 派生；多实例必须使用一致的 JWT Secret，修改该 Secret 后原幂等响应无法重放（返回 503）。幂等存储不可用时拒绝执行集成写操作。

## 6. 常见错误

| HTTP 状态码 | 错误码或原因 | 含义 |
| --- | --- | --- |
| 400 | 参数错误 | 缺少必填字段、非法分组、无可用智能分组、时间格式或范围错误。 |
| 400 | `IDEMPOTENCY_KEY_REQUIRED` | 集成写操作缺少 `Idempotency-Key`，包括系统开启观察模式时。 |
| 403 | 权限不足 | 分组未授权；签发登录 Token 的目标为管理员、停用账号或启用了 TOTP。 |
| 404 | 账号、Key 或分组不存在 | 包括为某账号查询不属于它的 Key。 |
| 401 | `ADMIN_API_KEY_GATEWAY_REQUIRED` | 直接使用 API Key 调用原管理员入口，必须改用集成网关并提供 App 签名。 |
| 401 | `INTEGRATION_AUTH_FAILED` | 缺少认证请求头、Admin API Key 错误或未配置、AppID 不匹配或凭据已停用。 |
| 401 | `INTEGRATION_SIGNATURE_INVALID` | 方法、原路径、请求头或原始请求体与签名不匹配。 |
| 401 | `INTEGRATION_TIMESTAMP_EXPIRED` | 时间戳超过服务端 5 分钟允许窗口。 |
| 403 | `INTEGRATION_ROUTE_FORBIDDEN` | 请求了禁止第三方调用的管理员路径。 |
| 409 | `INTEGRATION_NONCE_REPLAYED` | Nonce 已使用，判定为重放请求。 |
| 409 | 幂等冲突 | 相同 `Idempotency-Key` 被用于不同请求内容。 |
| 503 | `INTEGRATION_AUTH_UNAVAILABLE` | 认证配置存储不可用，服务端拒绝请求。 |
| 503 | `INTEGRATION_NONCE_STORE_UNAVAILABLE` | Redis 不可用，服务端拒绝请求以确保重放防护不会失效。 |
| 503 | `IDEMPOTENCY_STORE_UNAVAILABLE` | 幂等存储不可用，或原加密响应无法解密。保存响应失败时业务可能已执行；恢复存储后复用原幂等键查询，不要改用新键重复创建或充值。 |
