# 运维运行时配置

管理员接口由现有管理员认证、审计及管理权限中间件保护：

- `GET /api/v1/admin/ops/runtime/alert`：读取告警运行时配置；未初始化时返回默认配置。
- `PUT /api/v1/admin/ops/runtime/alert`：保存告警运行时配置，`evaluation_interval_seconds` 范围为 1–86400；其他字段沿用现有告警配置结构。

`timezone=Asia%2FShanghai` 查询参数不会影响路由匹配，告警配置本身不按时区分开存储。

关闭 `ops_monitoring_enabled` 或配置文件中的 Ops 开关后，管理员仍可读取和保存告警、邮件、高级设置、指标阈值和运行时日志配置。保存告警配置不会开启监控；采集、告警评估及监控数据接口仍遵守原监控开关。修复前这些设置接口会返回 `404 OPS_DISABLED`，导致设置弹窗加载失败。
