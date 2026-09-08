# Codex Fast Tier Bridge

CLIProxyAPI 插件，用于兼容 Codex Fast mode 的服务层级和动态路由提示。

## 行为

| CPA 收到的请求 | 发给 Codex 的 `service_tier` | Routing Hint |
| --- | --- | --- |
| 未设置 | 未设置 | 不发送 |
| `priority` | `priority` | `model=<实际模型>;tier=priority` |
| `fast` | `priority` | `model=<实际模型>;tier=priority` |

`fast` 会规范化为 `priority`，与 OpenAI Codex 客户端的请求值一致。这也避免 ChatGPT/Codex OAuth 后端返回 `Unsupported service_tier: fast`。

插件不会强制普通请求进入 Fast mode。最终是否采用 Fast mode 由上游决定：上游响应 `service_tier=priority` 表示实际使用 Fast mode，`service_tier=default` 表示按 Standard 处理或被降级。

OpenAI 官方说明：[Fast mode](https://developers.openai.com/api/docs/guides/fast-mode)。

## 兼容性

当前版本针对 CLIProxyAPI `v7.2.153`（Commit `934fb792`）构建并测试。升级 CLIProxyAPI 后应重新构建插件。

需要 Go 1.26 和 CGO。

## 构建

```bash
go mod tidy
make test
make build
```

Linux amd64 产物：

```text
dist/linux/amd64/codex-fast-tier-bridge-v1.4.0.so
```

## 安装

将动态库放入 CPA 的插件目录：

```text
plugins/linux/amd64/codex-fast-tier-bridge-v1.4.0.so
```

在 `config.yaml` 中启用：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codex-fast-tier-bridge:
      enabled: true
      priority: 100
      version: "1.4.0"
      audit: false
```

每个 Codex OAuth 凭证还需要允许 CPA 复制插件生成的动态请求头：

```json
{
  "X-Codex-Routing-Hint": "$X-Codex-Routing-Hint"
}
```

可在 CPA Manager Plus 的“凭证管理 → 配置 → 自定义请求头”中设置。

## 审计

临时将 `audit` 设置为 `true` 后，插件仅记录以下非敏感字段：

```text
service-tier-audit requested=priority outbound=priority upstream=default outcome=succeeded
```

- `requested`：CPA 收到的服务层级。
- `outbound`：转换后交给 Codex 执行器的服务层级。
- `upstream`：上游响应报告的实际处理层级。
- `outcome`：请求最终状态。

`audit: false` 时不会注册响应及生命周期审计能力，也不会新增 `service-tier-audit` 日志或保留审计记录。关闭审计不会删除 Docker 已保存的历史日志。

## 验证边界

测试必须同时检查普通与快速请求：

- 普通请求应为 HTTP 200，且响应层级为 `default`。
- 快速请求应为 HTTP 200；只有响应层级为 `priority` 才能确认上游实际采用 Fast mode。
- 请求成功但响应为 `default`，只能证明请求被上游接受，不能声称 Fast mode 已生效。
