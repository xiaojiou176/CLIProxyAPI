# CLIProxyAPI Documentation Policy

## 目标

确保后端协议、配置、鉴权、路由变更与文档同步，避免 AI 与人类协作时出现行为误判。

## DoD（文档维度）

以下改动必须同步更新文档：

- `internal/**`、`sdk/**`、`cmd/**` 行为变更
- 管理 API 或配置语义变更
- 测试/发布命令变更
- workflow 触发条件或门禁策略变更

## Doc-Change Contract

| 代码改动 | 必须同步 |
|---|---|
| `internal/**`, `sdk/**`, `cmd/**` | `README.md` 或 `docs/**` 或 `AGENTS.md` |
| `.github/workflows/**` | 本文件 |

## 门禁

- 脚本：`./scripts/doc-ci-gate.sh`
- workflow：`.github/workflows/doc-governance.yml`
- 调试基线：`docs/debug-runbook.md`

## Active Branch Note (2026-02-17)

当前分支包含 `.github/workflows/*` 与运行时日志链路改动，提交前必须保持本文件同步更新。
