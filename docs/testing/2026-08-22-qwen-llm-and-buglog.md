# 2026-08-22 Qwen 未真正参与发送 + bug 日志

## 根因

平台 `model:config` 会落盘并热替换 `LLMClient`，但发送主路径几乎不调用它：

1. 只在选择器找不到发送键/输入框时才截图定位。
2. 开会话、未知页、搜索页完全不走模型。
3. 启用判断只看 `Model != ""`，不看 `Enabled()`（需要 base_url + model）。
4. 平台若下发 camelCase（`baseUrl`/`apiKey`）会解成空配置，表现为「下发了但没用上」。
5. 部分视觉模型把 content 回成数组，旧解析会当成空回复。

## 修复

- 配置同时接受 snake/camel；`llmAssist()` 用 `Enabled()`。
- 深链校验失败后调用 `DiagnoseScreen`，按 unknown/search/tap_back 恢复。
- 打开会话/输入发送/聊天列表失败写 `data/bugs/YYYY-MM-DD.jsonl`（无截图、无 api_key）。

## 命令

```bash
go test ./internal/wda/ ./internal/gateway/ -count=1
```

## 结果

```text
ok  wda-farm-gateway/internal/wda       9.304s
ok  wda-farm-gateway/internal/gateway   2.213s
```

未对真实 Qwen 接口做联网调用。
