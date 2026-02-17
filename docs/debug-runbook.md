# CLIProxyAPI Debug Runbook

## 1. 快速定位顺序

1. 先确认配置与启动参数是否正确
2. 再确认请求是否到达管理路由/代理路由
3. 最后看上游调用与错误映射

## 2. 最小验证命令

```bash
go build ./cmd/server
go test ./...
bash test/verify_hard_constraints.sh
```

## 3. 日志与请求审计

- 请求日志开关：`/v0/management/request-log`
- 文件日志开关：`/v0/management/logging-to-file`
- 错误日志保留数：`/v0/management/error-logs-max-files`
- 单条请求日志：`/v0/management/request-log-by-id/:id`
- 错误日志列表：`/v0/management/request-error-logs`

默认日志目录：

- `logs/`
- 如配置 `WRITABLE_PATH`：`<WRITABLE_PATH>/logs`

## 4. 常见故障 -> 首查文件

- 鉴权或账号选择异常：`sdk/cliproxy/auth/*`
- 请求日志字段异常：`internal/logging/*`、`internal/api/middleware/request_logging.go`
- 事件流/usage 异常：`internal/usage/*`
- iFlow 执行异常：`internal/runtime/executor/iflow_executor.go`

## 5. 交付要求

调试类改动必须附带：

1. 复现步骤
2. 修改文件
3. build/test 证据
4. 风险边界
