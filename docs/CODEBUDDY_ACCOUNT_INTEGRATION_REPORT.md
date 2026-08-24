# CodeBuddy 账号接入开发报告

本文记录当前系统接入 CodeBuddy 的完整实现，包含账号创建、OAuth 授权、模型目录、OpenAI 兼容代理、计费、签到、积分余额、自动上下线和定时任务。内容同时作为后续接入其他 OAuth/API 账号类型的实现模板。

## 1. 目标与边界

CodeBuddy 账号在系统中使用：

- `platform = codebuddy`
- `type = oauth`
- OpenAI 兼容的 Chat Completions 入口
- CodeBuddy 自己的 OAuth access token / refresh token
- CodeBuddy 上游积分作为账号可用性指标

需要区分三类数据：

| 数据 | 来源 | 用途 |
| --- | --- | --- |
| OAuth token | CodeBuddy 授权接口 | 访问上游 API |
| 上游积分 | CodeBuddy billing/resource 接口 | 判断账号是否还有上游额度、自动上下线 |
| 本地 token 费用 | Sub2API 计费服务 | 扣除用户余额/订阅额度、记录使用量 |

上游积分不是用户余额；本地 token 费用也不会直接修改 CodeBuddy 上游积分。

## 2. 区域与域名

区域必须在创建授权前确定，不能在授权后混用：

| 区域 | OAuth/API 域名 | 计费/签到域名 | 登录页面 |
| --- | --- | --- | --- |
| 国内版 | `https://copilot.tencent.com` | `https://www.codebuddy.cn` | `https://www.codebuddy.cn/login?platform=VSCode&state=...` |
| 国际版 | `https://www.codebuddy.ai` | `https://www.workbuddy.ai` | `https://www.codebuddy.ai/login?platform=VSCode&state=...` |

`accounts.extra.codebuddy_region` 是区域的持久化真值；`credentials.region` 作为兼容字段。系统根据区域强制选择 API 域名，不能仅相信用户编辑的 `base_url`。

区域隔离规则：

1. 国内账号只能同步国内目录，过滤国际模型（例如 GPT、国际 Gemini/Claude）。
2. 国际账号只能同步国际目录，过滤国内专属模型（例如 Hy3、GLM、Kimi 国内卡）。
3. 请求 `/admin/accounts/codebuddy/models?account_id=...&region=...` 时，查询区域必须与账号区域一致。
4. 切换账号区域时，前端应清空旧区域的模型白名单和映射。

## 3. 添加账号流程

### 3.1 前端入口

文件：

- `frontend/src/components/account/CreateAccountModal.vue`
- `frontend/src/api/admin/accounts.ts`

流程：

1. 选择平台 `CodeBuddy`。
2. 选择 `国内版` 或 `国际版`。
3. 填写通用账号参数：名称、备注、代理、分组、并发、优先级、账号倍率、过期时间。
4. 点击授权，调用：

```http
POST /api/v1/admin/accounts/codebuddy/oauth/start
Content-Type: application/json

{
  "region": "domestic",
  "name": "sunday",
  "proxy_id": 0,
  "group_ids": [3],
  "concurrency": 10,
  "priority": 1,
  "rate_multiplier": 1
}
```

5. 后端返回 `session_id` 和 `auth_url`，浏览器打开 `auth_url`。
6. 前端轮询：

```http
POST /api/v1/admin/accounts/codebuddy/oauth/poll
{"session_id":"..."}
```

7. 授权成功后创建或更新账号，并保存 token、区域、身份信息和初始模型目录。

### 3.2 OAuth 上游交互

后端实现：`backend/internal/service/codebuddy_oauth_service.go`。

#### 创建授权状态

```http
POST {regional_api}/v2/plugin/auth/state?platform=VSCode
```

关键请求头：

```text
X-Domain: www.codebuddy.cn 或 www.codebuddy.ai
X-Product: SaaS
X-IDE-Name: VSCode
X-Requested-With: XMLHttpRequest
X-No-Authorization: true
X-No-User-Id: true
X-No-Enterprise-Id: true
X-No-Department-Info: true
```

上游返回 `state` 和 `authUrl`。`state` 由上游创建，不能由本地随意生成后直接拼登录 URL。

#### 轮询授权结果

```http
GET {regional_api}/v2/plugin/auth/token?state={state}
```

拿到 access token 后继续请求：

```http
GET {regional_api}/v2/plugin/login/account?state={state}
Authorization: Bearer {access_token}
```

该接口用于获取 `uid`、企业/租户 ID、域名和 refresh token。企业 ID 很重要，部分租户的模型目录没有它会返回空列表。

### 3.3 持久化凭据

账号 `credentials` 中保存以下字段：

```json
{
  "access_token": "...",
  "refresh_token": "...",
  "uid": "...",
  "enterprise_id": "...",
  "domain": "www.codebuddy.cn",
  "base_url": "https://copilot.tencent.com",
  "region": "domestic",
  "platform": "VSCode",
  "ide_type": "VSCode",
  "platform_version": "1.111.0",
  "product_version": "4.11.36344970",
  "deployment_type": "SaaS",
  "model": "auto",
  "models": ["auto"],
  "model_catalog": []
}
```

token 必须只保存在后端数据库，接口响应和日志不得输出完整 token。

## 4. 模型目录同步

### 4.1 接口

读取账号目录：

```http
GET /api/v1/admin/accounts/codebuddy/models?account_id=5
```

强制从上游同步并保存：

```http
POST /api/v1/admin/accounts/5/codebuddy/models/sync?region=domestic
```

### 4.2 上游探测顺序

CodeBuddy 不同版本/租户的配置接口不完全一致，后端按以下顺序尝试：

```text
GET /v2/plugin/accounts
GET /v3/config
GET /v2/config
GET /config/models
GET /v2/enterprise/{enterprise_id}/models   (存在企业 ID 时)
```

请求必须携带与 VS Code 插件一致的产品头：

```text
User-Agent: VSCode/1.111.0 CodeBuddy/4.11.36344970
X-IDE-Type: VSCode
X-IDE-Version: 1.111.0
X-Product-Version: 4.11.36344970
X-Product: SaaS
X-Domain: 区域对应 auth domain
X-User-Id: uid
X-Enterprise-Id: enterprise_id
X-Tenant-Id: enterprise_id
```

只接受实际解析出模型对象的响应。HTTP 200 但没有模型对象不算同步成功。

### 4.3 目录保存

模型目录同时保存到：

- `credentials.models`
- `credentials.model_catalog`
- `accounts.extra.codebuddy_models`
- `accounts.extra.codebuddy_model_catalog`
- `accounts.extra.codebuddy_model_source`

`model_catalog` 保留模型名称、输入/输出上限、工具调用、多模态、推理能力、供应商、企业标识和积分倍率：

```json
{
  "id": "gpt-5.6-sol",
  "name": "GPT-5.6-Luna",
  "credits": "x0.14",
  "credits_multiplier": 0.14,
  "max_input_tokens": 168000,
  "max_output_tokens": 32000,
  "supports_tool_call": true,
  "supports_images": true,
  "supports_reasoning": true,
  "vendor": "f",
  "is_enterprise": true
}
```

读取目录时允许使用最近一次成功保存的数据；“同步上游”操作必须严格访问远程，访问失败应明确返回诊断信息，不应静默覆盖为空。

## 5. 代理调用流程

CodeBuddy 对外归入 OpenAI 兼容入口，但上游实际只接受 Chat Completions：

```text
客户端 -> Sub2API OpenAI 兼容入口 -> CodeBuddy /v2/chat/completions
```

上游地址：

```http
POST {regional_api}/v2/chat/completions
Authorization: Bearer {access_token}
```

### 5.1 请求兼容处理

文件：`backend/internal/service/openai_gateway_codebuddy.go`。

1. 客户端可使用 OpenAI Chat Completions JSON。
2. 如果第一条消息不是 `system`，自动插入：

```json
{"role":"system","content":"You are a helpful assistant."}
```

这是因为 CodeBuddy 会拒绝以 user 消息开头的请求。

3. 自动补齐 VS Code 需要的请求头：

```text
X-Agent-Intent: craft
X-Conversation-ID
X-Conversation-Request-ID
X-Conversation-Message-ID
X-Request-ID
X-Model-ID
```

4. `model` 经过本地模型映射后作为上游模型 ID 发送。
5. CodeBuddy 上游返回 SSE，即使客户端请求非流式，也需要先消费 SSE，再组装成 OpenAI JSON 响应。
6. 流式客户端直接转发 SSE；非流式客户端使用缓冲适配器。
7. 工具调用、推理内容、finish reason 和 usage 需要从 SSE chunk 合并后再返回。

### 5.2 能力边界

CodeBuddy 账号只声明 Chat Completions 能力。不要把 CodeBuddy OAuth 账号路由到 ChatGPT `/responses` 或 OpenAI 官方域名，否则会出现 404、无响应或协议不匹配。

## 6. 计费设计

### 6.1 两层倍率

CodeBuddy 有两种不同倍率，必须分开：

1. **上游模型积分倍率**：来自 `model_catalog.credits_multiplier`，反映 CodeBuddy 使用该模型消耗多少上游积分。
2. **本地用户/账号倍率**：来自分组、用户专属倍率、账号 `rate_multiplier` 和高峰时段倍率，用于 Sub2API 对用户计费。

当前本地计算逻辑（不做货币换算）：

```text
基础 token 费用
  = 输入 token × 输入单价
  + 输出 token × 输出单价
  + 缓存写入 token × 缓存写入单价
  + 缓存读取 token × 缓存命中单价

CodeBuddy 上游抽象成本
  = token 数量 × 模型积分倍率 × 参考成本单位
    ÷ 参考积分 ÷ 每积分对应 Token 数

CodeBuddy 成本倍率
  = CodeBuddy 上游抽象成本 ÷ 本地基础 token 费用

用户实际费用
  = 本地基础 token 费用 × CodeBuddy 成本倍率
    × 分组/用户倍率 × 高峰倍率
```

默认校准值为 `70 / 2000 / 31874`，但不是写死的计费规则。管理员在每个 CodeBuddy 账号的添加/编辑表单中配置参考成本单位、参考积分和每积分对应 Token 数；修改后该账号的新请求立即使用新值。不同账号可以使用不同的上游成本校准，不进行汇率换算。

账号统计成本还会使用账号倍率；上游积分倍率只用于计算 CodeBuddy 成本倍率，不能直接替代用户倍率，也不能替代账号倍率。

分组倍率分为两种模式：

- `fixed`：直接使用分组的 `rate_multiplier`，再叠加账号、用户和高峰时段倍率。
- `dynamic`：普通 API Key 提供商以已确认的上游成本倍率为基准，再加上分组配置的 `dynamic_rate_markup_percent`；CodeBuddy 不读取 API Key 探测倍率，固定以基础倍率 `1.0` 加分组加价计算。CodeBuddy 模型的 `credits_multiplier` 始终只在请求级成本适配器中计算一次。

因此，动态分组不会把 CodeBuddy 模型积分倍率写回 `groups.rate_multiplier`，避免模型成本和分组加价重复相乘。

### 6.2 价格来源优先级

统一计费入口为 `BillingService.CalculateCostUnified`，价格优先级是：

1. 分组逐模型价格。
2. 渠道逐模型价格。
3. 系统内置基础价格。
4. 可识别的模型别名/实际模型回退价格。

CodeBuddy 目录中的积分倍率只参与 CodeBuddy 的提供商成本修正；用户扣费仍基于系统 token 价格和分组价格。

### 6.3 内置 CodeBuddy 价格

内置价格在 `backend/internal/service/billing_service.go`，存储为系统内部的抽象 token 计费单位/token。这里不做人民币、美元或其他货币换算；官方公布的每百万 token 数值直接作为本地基础价格单位：

```text
本地单位/token = 官方单位/百万 token / 1,000,000
```

当前覆盖 Hy3、Hy3 Preview/Agent、GLM-5V-Turbo 等 CodeBuddy 国内模型；目录同步得到的新模型如果没有本地价格，应由管理员在分组或渠道中补充明确价格，避免静默按 0 计费。

## 7. 上游积分、签到与自动上下线

### 7.1 余额探测

CodeBuddy 资源接口：

```http
POST {billing_endpoint}/v2/billing/meter/get-user-resource
Authorization: Bearer {access_token}
X-User-Id: {uid}
X-Enterprise-Id: {enterprise_id}
Content-Type: application/json
```

请求核心字段：

```json
{
  "PageNumber": 1,
  "PageSize": 100,
  "ProductCode": "p_tcaca",
  "Status": [0, 3],
  "PackageEndTimeRangeBegin": "当前时间",
  "PackageEndTimeRangeEnd": "当前时间 + 101 年"
}
```

后端从 `Response.Data.Accounts` 累加 `CycleCapacityRemain`，必要时兼容 `CapacityRemain`。只有接口成功且返回资源包时，余额才被视为可信。

余额保存位置：`accounts.extra.codebuddy_checkin.last_credit_remaining`。

### 7.2 签到接口

国内版：

```http
POST /v2/billing/meter/checkin-activity-status
POST /v2/billing/meter/checkin-status
POST /v2/billing/meter/daily-checkin
```

先查询活动和今日状态，再决定是否领取，避免重复签到。

国际版：

```http
POST /billing/ide/trial
```

国际版是一次性 Trial 领取；重复领取视为 `already`，不是系统错误。

签到后必须再次调用 `get-user-resource`，不能只相信签到接口返回的 `credit` 字段。

### 7.3 计划任务

签到由 `admin/accounts` 的定时计划统一调度，不再在编辑账号页面维护 Cron。

计划表：`scheduled_test_plans`。

新增字段：

```text
task_type = test       普通连接测试
task_type = checkin    CodeBuddy 签到/领取积分
```

迁移文件：`backend/migrations/229_scheduled_test_task_type.sql`。

创建签到计划示例：

```json
{
  "account_id": 5,
  "task_type": "checkin",
  "model_id": "",
  "cron_expression": "0 9 * * *",
  "enabled": true,
  "max_results": 100
}
```

`task_type=checkin` 只允许 CodeBuddy OAuth 账号；普通账号只能创建 `test` 计划。每次执行都会写入 `scheduled_test_results`，记录成功、失败、耗时、错误和剩余积分。

### 7.4 自动停止与恢复规则

可信余额为 0：

```text
账号当前 schedulable=true
    -> extra.codebuddy_checkin.auto_disabled_by_balance=true
    -> schedulable=false
```

余额恢复：

```text
余额 > 0 或签到接口明确授予 credit
且 auto_disabled_by_balance=true
    -> schedulable=true
    -> auto_disabled_by_balance=false
```

管理员手动关闭账号时，必须清除 `auto_disabled_by_balance` 标记，防止后续签到误把手动停用账号恢复上线。

## 8. Token 刷新与错误处理

刷新接口：

```http
POST {regional_api}/v2/plugin/auth/token/refresh
X-Refresh-Token: {refresh_token}
X-Auth-Refresh-Source: plugin
X-Domain: regional auth domain
X-Product: SaaS
X-IDE-Type: VSCode
X-IDE-Name: VSCode
X-IDE-Version: 1.111.0
X-Product-Version: 4.11.36344970
X-Requested-With: XMLHttpRequest
{}
```

CodeBuddy 请求遇到过期 access token 时，会使用 refresh token 重试一次。刷新成功后更新 access token、过期时间和轮换后的 refresh token。

错误分类：

- `401`：认证失效，标记账号错误，后续需要重新授权。
- `404`：优先检查区域 API 域名和路径，不要把国内/国际域名混用。
- `400`：检查 VS Code 产品头、User-Agent、uid、enterprise_id。
- `500/502`：保留上游诊断，不要把空目录保存成最新目录。
- 资源接口失败：余额保持上次值，不得据此自动下线。

## 9. 管理员接口清单

```text
POST /api/v1/admin/accounts/codebuddy/oauth/start
POST /api/v1/admin/accounts/codebuddy/oauth/poll
POST /api/v1/admin/accounts/:id/codebuddy/refresh
POST /api/v1/admin/accounts/:id/codebuddy/checkin
GET  /api/v1/admin/accounts/codebuddy/models?region=domestic
GET  /api/v1/admin/accounts/codebuddy/models?account_id=:id
POST /api/v1/admin/accounts/:id/codebuddy/models/sync?region=domestic
```

手动 `checkin` 与定时 `checkin` 共享同一套服务逻辑，避免手动测试和计划任务行为不一致。

## 10. 接入新账号类型的复用模板

新增平台建议按以下边界拆分：

### A. 平台定义

- 增加平台常量和前端平台选项。
- 明确 `type`：`oauth` 或 `apikey`。
- 明确支持的协议能力和默认上游 URL。

### B. 凭据适配器

- `Start/Poll`：创建授权状态、轮询 token、获取用户/租户上下文。
- `Refresh`：实现平台专属刷新请求。
- `TokenRefresher`：接入后台统一刷新调度。
- 禁止把 token 逻辑散落到 handler 或网关。

### C. 模型目录适配器

- 优先读取真实上游目录。
- 保存 ID 与完整元数据。
- 对区域、租户、企业范围做过滤。
- 区分“读取缓存”和“强制同步上游”。
- 任何 fallback 都必须标记来源。

### D. 网关协议适配器

- 明确客户端协议与上游协议是否一致。
- 统一处理 stream/non-stream。
- 处理必需 system prompt、请求 ID、会话 ID、工具调用和 usage。
- 将上游错误转换为可定位的本地错误。

### E. 计费适配器

- 提供模型默认价格和价格单位。
- 提供模型倍率，但与用户/分组倍率分离。
- 输入、输出、缓存写入、缓存读取、图片输入/输出分别计费。
- 价格缺失时明确告警，不允许意外免费。

### F. 额度与计划任务

- 实现余额/额度查询，并定义“可信余额”条件。
- 定义耗尽时的调度状态变化。
- 使用 `auto_disabled_by_*` 标记区分系统动作和管理员手动动作。
- 若有签到、试用或自动续期，作为 `scheduled_test_plans.task_type` 的新任务类型接入。
- 成功恢复只能恢复本服务自动停止的账号。

### G. 测试清单

至少覆盖：

1. 国内/国际 endpoint 互斥。
2. OAuth pending、completed、expired。
3. refresh token 轮换。
4. 上游目录成功、空目录、错误、缓存读取和强制同步。
5. 非 system 首消息自动修正。
6. SSE 转非流式 JSON。
7. 模型倍率与分组倍率同时存在时的计算。
8. 余额可信为 0 自动下线。
9. 签到获得积分自动恢复。
10. 管理员手动停用账号不被自动恢复。

## 11. 运维检查

出现“无响应”时按顺序检查：

1. 账号区域、`base_url`、`domain` 是否一致。
2. access token 是否存在且未过期。
3. `uid` 和 `enterprise_id` 是否保存。
4. User-Agent 和 VS Code 产品版本是否正确。
5. 上游是否要求第一条 system 消息。
6. 上游是否返回 SSE，网关是否完成 SSE 消费。
7. 使用的模型 ID 是否来自该账号当前区域目录。
8. 本地后端端口和数据库连接是否正常。

出现“模型同步失败”时，优先查看接口返回的逐路径诊断，例如 `/v3/config: HTTP 400`、`/v2/plugin/accounts: HTTP 200, no model objects`，不要直接增加本地 fallback 模型来掩盖上游接口问题。
