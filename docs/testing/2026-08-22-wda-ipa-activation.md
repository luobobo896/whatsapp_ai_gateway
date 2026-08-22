# 2026-08-22 WDA IPA 优先激活

## 环境

- 机器：本机 macOS，仓库 `/Users/hanson/work/个人文档/whatsapp_ai_gateway`
- 本机没有 `ios` / `tidevice`，未做真机 install + xctest
- 已有签过名的 `WebDriverAgentRunner-Runner.app`（`~/Library/Application Support/WDAFarmGateway/derived`）

## 命令

```bash
cd /Users/hanson/work/个人文档/whatsapp_ai_gateway
go test ./internal/gateway/ -count=1
SKIP_BUILD=1 DERIVED="$HOME/Library/Application Support/WDAFarmGateway/derived" \
  OUT=/tmp/wda-ipa-verify/wda.ipa sh scripts/package-wda-ipa.sh
go build -o /tmp/gateway-ipa-check ./cmd/gateway
/tmp/gateway-ipa-check -h
```

## 结果

- `go test ./internal/gateway/ -count=1`：通过
- IPA：`Payload/WebDriverAgentRunner-Runner.app`，含 `PlugIns/WebDriverAgentRunner.xctest`，bundle `com.wda.WebRunner.xctrunner`
- 网关新增 `-ipa`，默认 `<state>/wda.ipa`

## 未覆盖

- Windows / Mac 真机：`install` IPA 后 `runwda`/`xctest`，`/status` ready
- 本机未装 `ios`/`tidevice`，auto 仍回退 xcodebuild
