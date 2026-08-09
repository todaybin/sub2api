# 上游账号计费与余额管理功能清单

## 1. 功能目标

为管理员提供上游 API Key 账号的倍率、余额、订阅信息和实际使用量，并在余额不足时按开关配置自动停止账号调度。

## 2. 管理员设置

位置：管理员设置页的“上游倍率自动探测”区域。

- 上游倍率自动探测：控制是否周期性访问上游账号并读取声明倍率。
- 自动探测余额/用量：按账号独立控制余额/订阅查询；关闭倍率探测不影响已单独开启的余额探测。
- 探测间隔：按分钟设置周期，必须先启用倍率自动探测。
- 余额不足自动下线：默认关闭。
- 只有全局探测、账号余额探测和“余额不足自动下线”都开启时，系统才允许因余额为零自动停止账号调度。

## 3. 账号列表展示

位置：/admin/accounts。

API Key 类型账号的“上游计费”列会显示：

- 余额型账号：上游余额。
- 订阅型账号：订阅号。
- 当前上游倍率。
- 当前统计周期内的请求次数。
- 当前统计周期内的 Token 总量。
- 探测状态、数据时间和自动停止状态。

旧版本上游返回数据如果没有余额或使用量字段，前端仍会兼容显示倍率和原有状态。

## 4. 上游计费接口

接口：

    GET /v1/sub2api/billing

接口使用当前认证的上游 API Key 获取计费信息，响应包括：

- billing_mode：balance 或 subscription。
- balance：余额型账号的余额。
- subscription_id：订阅型账号的订阅号。
- usage.requests：请求次数。
- usage.total_tokens：Token 总量。
- usage.period_start：统计周期开始时间。
- usage.period_end：统计周期结束时间。
- usage.scope：固定为 api_key，只统计当前 API Key。
- usage.period：固定为 current_billing_period。

## 5. 统计口径

- 统计对象是当前认证的上游 API Key，不会混入其他 Key 的用量。
- 订阅型账号按当前有效订阅周期统计。
- 余额型账号按系统配置时区的当前自然月统计。
- 统计数据仅用于展示和管理员判断，不会改变本地已有的账号“今日统计”。
- 获取计费信息不会触发账号最后使用时间等副作用。

## 6. 余额不足自动停止规则

满足以下全部条件时，账号会自动停止调度：

1. 系统开启全局上游探测。
2. 系统开启余额不足自动下线。
3. 当前账号开启自动探测余额/用量（历史账号未保存新开关时沿用旧余额回退行为）。
4. 账号是余额型账号。
5. 探测结果明确显示余额小于等于零。

自动停止的实际动作：

- 将账号 schedulable 设置为 false。
- 不修改账号的 status 字段。
- 记录自动停止时间，管理员可以在账号列表提示中看到。
- 不会因为一次失败探测就自动下线。
- 订阅型账号不会因为钱包余额被自动停止。
- 系统不会自动恢复被停止的账号。

## 7. 手动恢复

管理员必须主动打开账号的调度开关，账号才会重新参与调度。手动修改调度状态时会清除自动停止标记。

## 8. 数据一致性与并发保护

- 探测结果和账号调度状态使用原子条件更新。
- 并发探测或管理员手动修改时使用 CAS 保护，避免旧探测结果覆盖新状态。
- 手动调度操作优先于旧的自动探测结果。

## 9. 相关测试

已覆盖：

- 上游计费接口余额、订阅和用量响应。
- 当前 API Key 的请求次数和 Token 统计。
- 中间件对计费接口的认证及副作用隔离。
- 全局自动停止开关。
- 余额为零时的调度状态更新。
- 订阅账号不因余额自动停止。
- 并发探测与手动调度的 CAS 保护。
- 管理员设置页开关保存。
- 管理员账号列表的余额、订阅号和用量展示。

## 10. 余额兼容查询与触发方式

`GET /v1/sub2api/billing` 仍是倍率和计费信息的主接口。当该接口成功但没有返回有效余额时，可在 API Key 账号编辑页选择余额查询方式：

- `auto`：优先请求本系统原生 `{base_url}/v1/usage`，读取 `remaining`、`balance` 或 `quota.remaining`；不兼容时再请求通用 `{base_url}/user/balance`。
- `generic`：请求 `{base_url}/user/balance`，使用账号 API Key 作为 Bearer Token，读取数值或数字字符串格式的 `balance`。
- `new_api`：请求 `{base_url}/api/user/self`，使用 New API 用户页面生成的 PAT（系统访问令牌），余额按 `data.quota / 500000` 换算为 USD。新版 New API 不需要 `New-Api-User`；用户 ID 仅用于兼容仍校验该请求头的旧版部署。

这里的 `{base_url}` 是站点根路径：如果模型地址填写为 `https://www.juaiapi.com/v1`，余额请求会自动去掉末尾 `/v1`，实际请求 `https://www.juaiapi.com/api/user/self`，不会拼成 `/v1/api/user/self`。倍率接口不存在或返回非标准响应时，只要余额查询成功仍会保存余额快照。

余额币种默认采用上游接口返回值。若上游错误标注币种，可在账号中覆盖为 USD 或 CNY；覆盖只改变展示单位，不做汇率换算。请求复用账号代理、TLS 指纹、超时、响应大小限制和自定义请求头，且禁止跨域重定向。

页面加载只会通过 `POST /v1/admin/accounts/today-stats/batch` 读取数据库中的最近快照，不会请求上游。单个或批量“立即探测”会实时请求上游，但不会触发余额不足自动下线。

自动探测服务由后端在正常启动时自动运行，不需要打开 Web 页面。服务启动后立即执行一次，之后每分钟最多取 20 个到期账号，最多 4 个并发；同一账号的倍率和余额请求顺序执行。全局开关关闭时停止所有定时请求，但手动探测和表单测试仍可用。

## 11. 标准 JSON 查询模板

账号编辑页的 `generic`、`new_api` 和 `custom` 模式保存完整 JSON，后端严格解析，不执行 JavaScript。配置包含 `request.url/method/query/headers/body`、`variables` 和固定 `mapping`。映射字段固定为 `is_valid`、`invalid_message`、`plan_name`、`remaining`、`used`、`total`、`unit`、`extra`；数字支持 `multiplier`、`divisor`，推导仅支持 `remaining_plus_used` 和 `total_minus_used`。

内置变量为 `{{baseUrl}}`、`{{rootUrl}}`、`{{apiKey}}`、`{{accessToken}}`、`{{userId}}`；自定义变量名只能使用字母、数字和下划线。`secret: true` 的变量值会保存到敏感凭据区，不会出现在账号响应或审计日志。自定义 URL 必须与账号 Base URL 保持相同协议、主机和端口，GET/POST 请求超时 10 秒，响应最多 64KB。

表单测试接口为 `POST /v1/admin/accounts/upstream-usage-query/test`，只返回规范化结果和本次完整 JSON 响应，不持久化、不写日志。

## 12. 账号表单与列表展示规则

### 12.1 两个账号级开关

API Key 账号的新增、编辑表单包含两个互相独立的开关：

- **自动探测上游声明倍率**：只决定后台是否定时请求上游倍率接口。
- **自动探测余额/用量**：只决定后台是否定时请求余额、订阅或用量接口。

倍率同步依赖倍率探测，但余额探测不依赖倍率探测。账号只开启余额探测时，后台可以只查询余额；账号只开启倍率探测且明确关闭余额探测时，不会额外请求余额接口。

### 12.2 余额设置的显示条件

只有打开“自动探测余额/用量”后，表单才显示以下设置：

- 余额查询方式。
- 上游余额币种。
- New API 用户 PAT 和兼容旧版的用户 ID。
- 标准 JSON 模板编辑器。
- 变量与响应映射帮助。
- “测试用量查询”按钮和测试结果。

关闭该开关时，上述设置全部折叠隐藏，保存账号后该账号也不会进入新的定时余额查询队列。全局探测开关关闭时，所有账号的定时倍率和余额查询都停止；账号表单测试和管理员手动探测不受全局开关影响。

### 12.3 查询方式和默认模板

余额查询方式包括：

- `auto`：使用后端内置兼容顺序，不显示 JSON 编辑器。
- `generic`：自动填入通用 `/user/balance` 标准 JSON 模板，用户可以在保存前修改。
- `new_api`：自动填入 `/api/user/self` 标准 JSON 模板，默认把 `data.quota / 500000` 映射为 USD 余额。
- `custom`：自动填入完整的自定义标准 JSON 示例，不提供空白编辑器。

切换查询方式时，表单会载入对应模板。编辑已经保存的账号时，优先加载该账号保存的完整 JSON，不覆盖管理员之前的自定义内容。

自定义模板的初始结构包含：

```json
{
  "version": 1,
  "template_type": "custom",
  "result_type": "balance",
  "variables": {
    "tenantId": {
      "value": "",
      "secret": false
    }
  },
  "request": {
    "url": "{{rootUrl}}/user/balance",
    "method": "GET",
    "query": {},
    "headers": {
      "Authorization": "Bearer {{apiKey}}",
      "User-Agent": "sub2api/usage-probe"
    }
  },
  "mapping": {
    "is_valid": { "path": "is_active", "default": true },
    "invalid_message": { "path": "message" },
    "plan_name": { "path": "plan_name" },
    "remaining": { "path": "balance" },
    "used": { "path": "used" },
    "total": { "path": "total" },
    "unit": { "path": "currency", "default": "USD" },
    "extra": { "path": "data" }
  }
}
```

### 12.4 用户可配置范围

用户只负责定义两件事：

1. **如何请求上游数据**：配置 `request.url`、`request.method`、`request.query`、`request.headers` 和结构化 JSON `request.body`。
2. **如何把上游响应映射成本地字段**：在 `mapping` 中填写上游响应的 GJSON 路径、默认值和数值换算规则。

系统负责变量替换、发送请求、限制超时和响应大小、解析 JSON、生成统一快照以及在列表中展示。模板不允许 JavaScript、函数、动态表达式或任意代码执行。

### 12.5 可用变量

内置变量如下，JSON 中使用双花括号引用：

| 变量 | 含义 |
| --- | --- |
| `{{baseUrl}}` | 账号填写的完整 Base URL。 |
| `{{rootUrl}}` | 去掉末尾 `/v1` 后的站点根地址；例如 `https://example.com/v1` 变为 `https://example.com`。 |
| `{{apiKey}}` | 账号的模型 API Key。 |
| `{{accessToken}}` | New API 用户页面生成的 PAT/系统访问令牌。 |
| `{{userId}}` | 兼容旧版 New API 的 `New-Api-User` 用户 ID。 |

自定义变量写入 `variables`。变量名只能使用字母、数字和下划线，并且必须以字母开头。标记为 `secret: true` 的变量在保存时会从普通配置中移出并写入敏感凭据区；账号响应和审计日志不会返回原值。

### 12.6 本地标准映射字段

`mapping` 只能输出以下固定字段：

| 字段 | 用途 |
| --- | --- |
| `is_valid` | 本次上游响应是否有效。 |
| `invalid_message` | 无效时显示的错误原因。 |
| `plan_name` | 上游套餐或分组名称。 |
| `remaining` | 剩余余额或剩余用量。 |
| `used` | 已使用量。 |
| `total` | 总额度。 |
| `unit` | 显示单位或币种，例如 `USD`、`CNY`。 |
| `extra` | 需要保留的额外结构化数据。 |

数值映射使用 `(上游原始值 * multiplier) / divisor`。允许的推导只有：

- `remaining_plus_used`：使用 `remaining + used` 生成 `total`。
- `total_minus_used`：使用 `total - used` 生成 `remaining`。

### 12.7 表单测试规则

表单测试可以在账号尚未保存时执行，使用当前表单中的 Base URL、API Key、PAT、用户 ID 和 JSON 配置请求上游。测试结果包含：

- 规范化后的 `is_valid`、`plan_name`、`remaining`、`used`、`total`、`unit` 等字段。
- 上游本次返回的完整 JSON，最大 64KB。
- HTTP 状态码。

表单测试不会保存快照，不会触发余额不足自动下线，也不会要求全局定时探测处于开启状态。

### 12.8 账号列表显示

`/admin/accounts` 的列标题固定为“上游倍率/余额”。列表单元格在同一行组合显示倍率和计费信息：

- 倍率和余额都存在：`0.60x / 余额 ¥50.00`。
- 倍率和美元余额都存在：`0.60x / 余额 $50.00`。
- 只有余额：`- / 余额 ¥50.00`。
- 订阅账号：`0.60x / 订阅 #123`。
- 尚未成功探测：显示未探测、失败、不支持或已过期状态。

鼠标移动到该单元格后，详情提示继续显示计费类型（余额计费或订阅计费）、余额、订阅号、用量、声明倍率、高峰倍率、更新时间、账号探测状态、全局探测状态和余额不足自动下线状态。

## 13. 数据库备份存储

位置：`/admin/settings` 的“数据”区域。

数据库备份支持本地存储和云存储。手动备份与定时备份共用同一个存储目标；切换为本地存储时，页面隐藏 S3/COS 配置，创建备份不会再校验云存储配置。

### 13.1 本地存储

本地存储指后端服务器文件系统，不是管理员浏览器所在电脑。管理员只需选择“本地存储”并保存，不需要填写服务器路径。

默认根目录是后端进程当前工作目录下的：

```text
data/backups/database
```

文件按日期继续分目录：

```text
data/backups/database/YYYY/MM/DD/<数据库名>_YYYYMMDD_HHMMSS.sql.gz
```

例如在 `E:\dev_code_project\sub2api\backend` 启动后端时，实际根目录为：

```text
E:\dev_code_project\sub2api\backend\data\backups\database
```

保存本地存储配置时，后端自动创建并检查目录是否可写。上传本地文件时先写入临时文件，完成同步后再原子重命名，避免失败任务留下看似完整的备份文件。备份文件使用受限权限，不会作为公开静态文件暴露。

### 13.2 S3 兼容云存储

通用 S3 模式支持 AWS S3、Cloudflare R2、MinIO 及其他兼容实现，需要配置：

- Endpoint。
- Region。
- Bucket。
- Key 前缀，默认 `backups/`。
- Access Key ID。
- Secret Access Key。
- 是否强制使用路径风格。

云存储密钥会加密后保存。生产环境必须设置固定的 `TOTP_ENCRYPTION_KEY`；如果使用每次启动自动生成的临时密钥，系统拒绝持久化新的云存储 Secret，避免服务重启后无法解密。

### 13.3 腾讯云 COS

云存储类型选择“腾讯云 COS”后，管理员只需配置：

- `SecretID`。
- `SecretKEY`。
- `Bucket`，应使用包含 APPID 的完整存储桶名称。
- 所在区域，从页面下拉列表选择，例如 `ap-chengdu`、`ap-guangzhou` 或 `ap-shanghai`。

COS 不显示也不接受管理员自定义 Endpoint。后端始终根据所选区域生成：

```text
https://cos.<region>.myqcloud.com
```

即使请求中残留了其他 S3 Endpoint，COS 模式也会覆盖为区域对应地址，避免切换存储类型后误用旧地址。

当前测试凭据对应的存储桶区域为 `ap-chengdu`。已完成一次真实测试对象的上传、下载内容校验和删除清理，说明该存储桶的连接、读取、写入和删除权限可用。此测试不等同于完整数据库备份；完整备份还依赖 PostgreSQL 客户端工具。

### 13.4 PostgreSQL 远程连接与客户端兼容

数据库导出使用 `pg_dump`，恢复使用 `psql`。它们会直接使用 `database.host`、`database.port`、`database.user`、`database.password`、`database.dbname` 和 `database.sslmode` 连接本机、远程、WSL 或 Docker 中的 PostgreSQL。选择本地存储或 COS 只改变备份文件的目标位置，不会取消对 PostgreSQL 客户端的依赖。

备份数据流为 `pg_dump -> SQL -> gzip -> 本地/S3/COS`，不是复制 PostgreSQL 正在使用的数据文件。运行中的数据库不能通过直接复制数据目录获得可靠的一致性备份。

Windows、Linux 和 macOS 后端按以下顺序查找工具：

1. `PG_DUMP_PATH` 或 `PSQL_PATH` 指定的完整程序路径。
2. `database.client_bin`（或 `DATABASE_CLIENT_BIN`）指定的 PostgreSQL 根目录或 `bin` 目录。
3. `PG_BIN` 或 `POSTGRES_BIN` 指定的 PostgreSQL 根目录或 `bin` 目录。
4. `POSTGRES_HOME\bin`。
5. 项目或程序附近的 `tools/postgresql/<系统>-<架构>/bin` 和 `tools/postgresql/bin`。
6. 当前进程的 `PATH`。
7. Windows 的 `C:\Program Files\PostgreSQL\<版本>\bin` 等标准安装目录，以及 Linux/macOS 的常见安装目录。
8. 后端运行在 Windows 且前述位置均未找到工具时，自动尝试默认或 `PG_WSL_DISTRO` 指定的 WSL 发行版内的 `pg_dump` 或 `psql`。
9. WSL 主机也没有客户端时，自动进入 `sub2api-postgres` 或 `sub2api-postgres-dev` Docker 容器执行工具；原生 Linux 同样支持 Docker 容器回退。

项目内置客户端支持以下两种目录布局：

```text
tools/postgresql/bin/...
tools/postgresql/windows-amd64/bin/...
tools/postgresql/linux-amd64/bin/...
tools/postgresql/linux-arm64/bin/...
```

Windows 不能只放一个 `pg_dump.exe`，必须复制 PostgreSQL 完整的 `bin` 目录，其中包含 `psql.exe`、`libpq.dll`、OpenSSL DLL 等运行依赖。Linux 使用便携发行版时，应保留与 `bin` 相邻的 `lib` 目录，后端会为客户端进程自动设置库搜索路径。客户端二进制和 DLL 已被 Git 忽略，不会误提交到仓库。

也可以在 `config.yaml` 中显式配置：

```yaml
database:
  client_bin: "./tools/postgresql/windows-amd64/bin"
```

相对路径以启动后端时的工作目录为基准。修改 `config.yaml` 或相关环境变量后需要重启后端进程。

官方 Linux Docker 镜像会把与 PostgreSQL 镜像匹配的 `pg_dump` 和 `psql` 放入应用运行镜像；其他 Linux 安装方式仍需要安装发行版的 PostgreSQL 客户端包。

数据库运行在 WSL、后端运行在 Windows 时，可以直接使用 WSL 内已安装的 PostgreSQL 客户端。Ubuntu/Debian 发行版缺少客户端时，在 WSL 内安装：

```bash
sudo apt update
sudo apt install postgresql-client
```

数据库不在默认 WSL 发行版时，启动 Windows 后端前设置 `PG_WSL_DISTRO`，值为 `wsl -l -q` 显示的发行版名称：

```powershell
$env:PG_WSL_DISTRO = "Ubuntu-24.04"
```

数据库容器不是仓库默认名称时，同时设置容器名：

```powershell
$env:PG_DOCKER_CONTAINER = "你的PostgreSQL容器名"
```

Docker 回退使用 `docker exec` 在数据库容器内执行命令，数据库密码通过进程环境传入，不写入命令行或备份记录。仓库默认 Compose 容器名无需额外配置。

修改 WSL 或 Docker 相关环境变量后需要重启 Windows 后端进程。

自定义安装目录可以在启动前设置：

```powershell
$env:PG_BIN = "C:\Program Files\PostgreSQL\17\bin"
```

也可以分别指定：

```powershell
$env:PG_DUMP_PATH = "C:\Program Files\PostgreSQL\17\bin\pg_dump.exe"
$env:PSQL_PATH = "C:\Program Files\PostgreSQL\17\bin\psql.exe"
```

如果没有安装 PostgreSQL 客户端，系统会明确提示安装客户端或配置上述路径，不再只返回 Windows `%PATH%` 的底层执行错误。

### 13.5 备份、恢复与记录

备份过程为流式处理：`pg_dump` 输出经过 gzip 压缩后写入当前存储目标，避免先在内存中生成完整 SQL 文件。本地和云存储均支持：

- 手动创建备份。
- 定时创建备份。
- 按保留天数和最多份数清理旧备份。
- 恢复指定备份。
- 删除备份文件及记录。
- 下载已完成的备份。

备份记录包含 `storage_type` 和 `storage_provider`，页面可以区分本地、通用 S3 和腾讯云 COS。历史记录没有 `storage_type` 时按 S3 处理，兼容升级前已有数据。本地记录保存创建时的实际根目录，因此修改默认目录不会影响旧文件的下载、恢复或删除。

本地文件通过受管理员认证保护的下载接口传输，不生成公开 URL。创建备份、修改云存储目标、下载和恢复等高风险操作使用管理员 step-up 2FA 保护。

### 13.6 管理接口

主要接口如下：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/v1/admin/backups/storage-config` | 获取本地或云存储目标。 |
| `PUT` | `/v1/admin/backups/storage-config` | 保存存储目标。 |
| `GET` | `/v1/admin/backups/s3-config` | 获取脱敏后的 S3/COS 配置。 |
| `PUT` | `/v1/admin/backups/s3-config` | 保存 S3/COS 配置。 |
| `POST` | `/v1/admin/backups/s3-config/test` | 测试云存储连接。 |
| `GET/PUT` | `/v1/admin/backups/schedule` | 获取或保存定时备份配置。 |
| `POST` | `/v1/admin/backups` | 创建备份。 |
| `GET` | `/v1/admin/backups` | 获取备份记录。 |
| `GET` | `/v1/admin/backups/:id/download` | 下载备份文件。 |
| `POST` | `/v1/admin/backups/:id/restore` | 恢复备份。 |
| `DELETE` | `/v1/admin/backups/:id` | 删除备份。 |

### 13.7 验证覆盖

已覆盖以下验证：

- 本地目录自动解析、创建和写权限检查。
- 未配置 S3/COS 时创建和恢复本地备份。
- 本地文件上传、下载和删除。
- COS 区域校验及 Endpoint 自动生成。
- 腾讯云 COS 真实测试对象上传、下载校验和删除。
- Windows PostgreSQL 版本排序和工具路径解析。
- S3 备份创建及下载 URL。
- 前端 TypeScript 类型检查和生产构建。
