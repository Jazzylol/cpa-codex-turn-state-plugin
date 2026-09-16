# Codex Turn State

CLIProxyAPI 动态库插件：在模型执行选定 Codex 凭据后，按凭据覆盖 `X-Codex-Turn-State` 请求头。每个凭据独立设置固定值和启停状态。

## 安装

面向支持原生动态库插件的 CPA v7.3.4 或更新版本。Release 提供 Linux、macOS、Windows 的 AMD64 / ARM64 安装包，Linux 需要 GLIBC 2.34 或更新版本。本机已验证 `jinshenganyuci/cli-proxy-api:codex-identity-v7.3.4.1`。`no-plugin` 版 CPA 无法加载。

### 通过 CPA 插件商店安装

在已有 `plugins` 配置中添加插件源，保留其他现有配置：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/jinshenganyuci/cpa-codex-turn-state-plugin/main/registry.json
```

保存后刷新管理后台的 **插件商店**，搜索 **Codex Turn State**，点击 **安装**。CPA 会自动下载对应系统和架构的 Release 安装包并验证 `checksums.txt`。无需手动上传 `.so`。

安装后打开插件资源菜单 **Codex Turn State**，输入 CPA 管理密钥，选择凭据并设置标头。所有凭据默认不覆盖。

### 手动安装

1. 将 `codex-turn-state.so` 放入 CPA 的 `plugins` 目录。
2. 把 `config.example.yaml` 中的配置合并到现有 `plugins` 配置。不要覆盖其他插件配置，也不要重复写顶层 `plugins`。
3. 加载插件后，打开管理后台的插件资源菜单 **Codex Turn State**。直接地址为 `/v0/resource/plugins/codex-turn-state/panel`。
4. 输入 CPA 管理密钥，选择凭据，粘贴完整标头值，勾选启用并保存。所有凭据最初均不覆盖。
5. 发起一个新请求，下载详细报文，在 `API REQUEST` 中核对凭据和发出的标头。

配置保存在 `state_file` 指定的 JSON 文件，权限为 0600；相对路径以 CPA 进程工作目录为基准。Docker 部署应持久化该文件所在目录。OAuth 凭据文件不会被插件改写。

## 行为

- 仅匹配当前实际选中的 Codex 凭据 ID 和 auth_index；兼容普通 Codex 请求及使用 OpenAI Responses 格式的压缩请求。
- 在凭据选择后运行，覆盖客户端携带的同名标头；值按原文发送，不对 `$` 开头内容做变量替换。
- 未配置、关闭规则或其他凭据保持原有行为；删除规则不会删除凭据。
- 修改规则立即作用于之后的执行尝试。HTTP 使用每次请求的标头；WebSocket 只在建立上游连接时发送 HTTP 握手标头，修改后必须重新连接客户端，已经建立的连接不会补发新标头。
- 规则影响模型执行请求，不接管 OAuth 登录、Token 刷新、额度查询或管理 API Call。
- 当前 CPA 的 `/v1/images/*` 图片接口会从原始客户端请求重建标头，忽略插件覆盖。为避免静默发送错误值，选中已启用规则的凭据时，插件会在出站前返回 HTTP 501 和 `turn_state_image_transport_unsupported`。关闭该凭据的规则后恢复图片调用。不声称支持这些图片端点。
- 上游是否接受固定回合状态由上游决定；插件不会生成有效状态，也不会把上游新返回的状态自动覆盖到手动配置中。

CPA 凭据原生 `headers`、模型级请求头规则以及后续执行的其他插件可能拥有更高优先级。管理页保存启用规则时会拒绝凭据已有同名原生 header 的冲突，避免无声失效；不要在其他位置同时配置此标头。外部后续修改仍需通过请求日志核对。禁用插件不会清除已经发往上游连接的握手状态。

## 管理接口

接口均要求 CPA 管理鉴权，静态资源页面不包含凭据和标头值。

- `GET /v0/management/plugins/codex-turn-state/credentials`：最小化的 Codex 文件凭据列表，不返回 token 或文件路径。
- `GET /v0/management/plugins/codex-turn-state/rules`：已保存规则。
- `PUT /v0/management/plugins/codex-turn-state/rule`：保存 `{ "auth_id": "example.json", "auth_index": "...", "enabled": true, "value": "..." }`。
- `DELETE /v0/management/plugins/codex-turn-state/rule`：删除 `{ "auth_id": "example.json" }`。

## 本地构建和测试

```sh
go mod download
gofmt -w .
go test -race ./internal/...
go vet ./...
make package
```

源码使用 Go C ABI 动态库，不依赖 CPA 内部 Go 类型，不要求与 CPA 使用同一 Go 编译器。目标系统仍须满足构建产物的 libc 版本和 CPU 架构要求。

项目与 Release：https://github.com/jinshenganyuci/cpa-codex-turn-state-plugin
