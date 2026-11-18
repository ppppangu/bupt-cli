# 协议、证据与边界

本文说明当前适配器实际依赖的学校协议、控制器连接方式和验证边界。它不是学校承诺稳定的公开 API 文档。学校前端或认证部署变化后，应先补充实际证据与回归测试，再调整适配器，不能把未识别的响应当作成功。

## 四层边界

1. `internal/cli` 负责参数、配置合并、stdin/stdout 和终端，不在 SSH 前自动执行学校登录。
2. `internal/client` / `internal/wire` 通过本地 Unix socket 或受限 SSH 命令连接控制器。
3. `internal/control` 保存运行周期、操作回执、SSH 路由、中继端口租约和审计，执行生命周期协调。
4. `internal/platform` 适配学校 HTTP、WebVPN、Socket.IO、控制台及持久存储；`internal/connect` 提供独立 SSH、SFTP 和隧道准备。

学校会话在 vault 中；SSH 路由、实例/节点身份、主机公钥、端口租约在状态库中。学校会话失效只阻断依赖该会话的管理通路，不应使已有 SSH 路由失效。重新准备一条缺失的 SSH 通路仍可能需要有效学校认证。

## 学校适配器的证据与测试

当前实现依据本次对学校已发布前端及运行时交互的检查，并将关键格式落实为已知向量或本地协议服务测试。仓库不保存真实账号口令、会话 Cookie、Token、带授权参数的下载 URL 或生产响应原文。

| 领域 | 当前实现 | 可复核的源码与测试证据 |
| --- | --- | --- |
| WebVPN 地址 | Wengine 的主机名编码、协议/端口路径及 HTTP→WebSocket 对应关系；避免重复代理 | [proxy.go](../internal/platform/proxy.go)，`TestWebVPNProxyKnownHostnameAndWebSocketProtocol` |
| 平台本地口令接口 | `/auth/user`、显式 `/auth/login` / `/auth/reset-user-password`；兼容前端密码编码；不是默认登录路径 | [auth.go](../internal/platform/auth.go)，`TestPasswordBrowserCodecKnownVector`、`TestAuthCookiesPersistenceIdentityAndPasswordSetup` |
| WebVPN 人工批准 | 原生 QR 开始/轮询、受限重定向、完成后确认会话 | [auth.go](../internal/platform/auth.go)，`TestGatewayQRSessionAndRedirectAllowlist`、`TestGatewayExpiryIsDistinctFromPlatformExpiry` |
| 验证码 | `/auth/captcha`，登录附带明确提供的 challenge/response | `TestCaptchaOptionsAndSavedCredentialRetry` |
| 状态与任务 | Engine.IO 3 WebSocket + Socket.IO 文本事件，索引同步、实体关系和 UUID 任务关联 | [socket.go](../internal/platform/socket.go)，`TestSocketEIO3GraphIndexGapAndTaskCorrelation` |
| 重试与不确定结果 | 相同 UUID/请求体重试；陈旧状态码重新核对后建立关联的后续尝试；超时保留未知状态 | `TestTaskStaleCodeAndAmbiguousTimeout`、`TestTaskRetryReusesExactUUIDAndPayload`；[tasks.go](../internal/control/tasks.go) |
| 终端 | `/proxy/service/<十六进制服务 ID>/term`；二进制 stdin、resize、heartbeat 帧 | [console.go](../internal/platform/console.go)，`TestConsoleBinaryFramesAndFragmentedReads` |
| 持久存储 | 从实际 storage 配置选择空间，TUS 偏移续传与流式下载，校验路径/来源 | [storage.go](../internal/platform/storage.go)，`TestStorageTUSResumeStreamingAndTraversal` |

上述 `Test...` 均位于 [platform_test.go](../internal/platform/platform_test.go)。这些测试复现协议，而不连接真实学校账户。源码中的浏览器兼容密码编码不是凭据静态存储加密；静态 Secret 加密由 [vault.go](../internal/state/vault.go) 单独负责。

默认登录现在由 [login.go](../internal/control/login.go) 统一协调 WebVPN、北邮 SSO 二维码和平台回调；原生 SSO 适配位于 [sso.go](../internal/platform/sso.go)。控制器状态迁移测试见 [login_test.go](../internal/control/login_test.go)。SSO 回调之后仍要核验平台身份，不能把扫码成功或某一次重定向当作平台登录成功。

## 统一认证前端契约

CLI 默认只调用 `auth.start` 和 `auth.poll`。控制器返回同一种 `AuthChallenge`：`stage` 为 `webvpn`、`university_sso`、`complete`；`state` 描述等待、完成、过期等状态；`required_action` 给出人工操作，`poll_after_ms` 指定后续轮询间隔，`url` 是当前阶段二维码内容。

账号密码模式在 `auth.start` 中传入 `sso_method=password`。WebVPN 完成后，控制器返回 `kind=bupt_sso_password`、`required_action=enter_sso_password`、表单 ID 和有效期。学校要求图形验证码时还返回 `captcha_image`。同一 profile 的所有者可通过 `auth.sso.submit` 提交 `challenge_id`、`username`、`password` 和可选的 `captcha_code`。控制器在提交前消费表单 ID，持久化中只保留会话和续接所需的状态。平台 `/auth/user` 核验成功后才报告完成。公开表单和本地 HTTPS 测试见 [sso_password.go](../internal/platform/sso_password.go) 和 [sso_password_test.go](../internal/platform/sso_password_test.go)。

统一认证 WebSocket 先按 `event` 分派，再解析 `ticket` / `confirm` 的对象数据；其他通知允许标量、数组或空 payload。收到确认码后连接保持到平台身份验证完成。真实 `/auth/user` 在 HTTP 200 时可能携带 `code:1`；该字段不能套用变更接口的 `retCode` 规则，仍必须验证有效 UID 与配置的身份绑定。

CLI 的 [auth_flow.go](../internal/cli/auth_flow.go) 处理阶段标签、二维码展示、人工凭据输入和等待。它不拼接 CAS URL、不交换票据、不操作认证 Cookie，也不自行决定跳过哪个认证阶段。TUI 显式登录时交还终端给这个流程，结束后恢复原页面。

WebVPN 认证完成后由控制器立即返回下一阶段 challenge；只有平台身份验证完成才返回最终 `authenticated`。前端必须用每次完整的新响应替换旧 challenge，避免 JSON 省略的旧 `url` 残留。相同阶段同一 URL 只显示一次，新 URL 或新阶段重新显示。`refresh_required` 要结束当前等待并提示重新登录，`confirmation_required` 要等待明确的 `auth confirm --take-over`，不能自动接管其他登录。

`--local` 选择平台本地口令。仅指定凭据 stdin 时沿用该入口，同时指定 `--sso-password` 时进入学校 CAS。另一个独立命令 `auth access` 只保存实例终端密码，供后续 SSH 公钥初始化；它不参与统一认证，也不设置或试验服务端密码。`ssh ensure INSTANCE` 对已运行资源请求 `route.get` 的显式 repair 操作，不分配资源。

## 门户与算力会话

同一 profile 的 WebVPN/CAS 会话可按需获得相应服务的登录票据。门户用户由 `/api/me` 核验，裸算力由 `/auth/user` 核验。当前账号权限不足、模块关闭、网关登录过期分别有独立的错误码。门户读接口按网页的模型 ID 汇总 `/api/models` 与 `/api/status`。用量来自 `/api/usage`，接入地址来自 `/api/config/gateway`。

`portal.read` 仅接受已知栏目。`portal.change` 处理具名的学校操作。Key 创建接口返回的一次性凭据在控制器加密保存，前台只收到不透明 ID。启停按目标状态核对学校实际结果；创建超时由请求回执与同名未决记录防止无条件重发。`portal.export` 独立授权。经 WebVPN 的写请求保留与真实门户页相同的访问来源；遇到跨站重定向不会重发写入。

`compute.read` 汇集项目、账单、平台申请和已启用的可选模块。管理员限额调整先经明确的学校后台身份检查，再以当前限额预览版本提交。控制器所有者、学校平台管理员和项目成员分别依各自来源校验。

### 多终端账号状态

本地控制器在 `~/.bupt/state/<profile>/status.json` 发布快照，目录权限为 0700。写入使用权限为 0600 的同目录临时文件和原子替换，适合用 `jq` 读取。对于和状态文件重名、或配置中仅大小写不同的 profile，目录名采用 `@` 加 profile 的小写无填充 Base32 编码。

快照包含控制器生成的 `epoch`、该 epoch 内单调递增的 `revision`、两层认证状态，以及有证据时的姓名、学工号、余额与本人运行实例数。过了 `valid_until`，读取者应将已登录视为待核验。实例数另有独立的 `running_valid_until`。新的控制器会生成新的 epoch。

`account.watch` 的首帧和后续每次变化都是完整快照。TUI 通过本地 Unix socket 或已授权的远程 SSH 控制器订阅，重连后从首帧恢复。CLI 的登录和退出经同一控制器发布。TUI 偏好保存在使用者本机的 `tui.toml`，多个窗口通过目录事件重读。

显式重新登录成功后，控制器在对应 epoch 内更新 `login_revision`。TUI 据此重读刚才因认证被拒的查询，已提交的模型请求和实例操作继续使用各自的回执。学校账号需要确认当前版本服务协议时，已核验的门户快照携带 `consent_required`，由用户阅读并确认。

自动核验开启时，同一 profile 的在线窗口共享周期检查。控制器只读取 WebVPN 的 `/user/portal/collections` 和门户的 `/api/me`，使用隔离的 Cookie 副本，遵守学校限流。只有操作系统可见的地址变化能在本地立即提示网络发生变化，公网出口、学校端注销和静默失效要等服务端证据。缓存模式订阅本地已知变化，按需查询算力资源时才会取得实例计数。

## CLI 与控制器 RPC

本地控制器默认监听私有 Unix socket，不开放一个新的公网 HTTP 管理端口。远程客户端使用 SSH forced command `rpc`；网关根据 SSH 注册公钥固定 `client_id`，覆盖请求自行声明的身份，不能通过传入另一个身份越权。

普通请求为一行 JSON，一次连接完成一个请求：

```json
{"id":"request-uuid","method":"instance.list","profile":"bupt","params":{}}
```

普通响应包含相同的 `id`，以及 `result` 或 `error`。错误携带稳定的 `code`、可读 `message`，必要时附 `retryable` 与 `task_id`：

```json
{"id":"request-uuid","error":{"code":"AUTH_REQUIRED","message":"school approval required","retryable":true}}
```

JSON 消息和单帧负载上限均为 16 MiB。大文件按流分块，不整体装入一条 JSON。流操作先发送普通 JSON 确认，再使用 `1 byte kind + 4 byte 大端长度 + payload` 帧：

| kind | 值 | 用途 |
| --- | --- | --- |
| Input / Output / Stderr | 0 / 1 / 2 | 输入、输出、独立 stderr |
| Resize | 3 | 终端尺寸 JSON |
| Exit | 4 | `{"code":0}` 等退出状态 |
| Signal | 5 | 远端信号名称 |
| End | 6 | stdin 结束 |
| Progress | 7 | 上传偏移、传输元数据等 |
| Failure | 8 | 结构化错误 |

`connect.proxy` 在 JSON 确认后转成纯字节流，供 OpenSSH `ProxyCommand` 使用；该确认由内置客户端消费，不写入 stdout。`proxy`、`ssh`、`exec` 不能混入表格、进度条或 JSON 结果。上传确认可以返回 `offset`，客户端将文件定位到该偏移；不可定位的 stdin 不自动续传。

协议实现见 [wire.go](../internal/wire/wire.go)、[client.go](../internal/client/client.go)、[server.go](../internal/control/server.go)。当前协议与状态 schema 为版本 1；仍是本项目内部协议，不承诺不同版本二进制任意混用。远程安装应使用与本地 CLI 同次构建的 agent。

## SSH、中继与身份

首次准备通过受认证的平台控制台检查目标账户、主目录、SSH 服务与主机公钥，安装工具专用公钥，并验证真实 SSH 登录。随后连接使用缓存信息和严格主机公钥检查，不静默接受主机密钥变化。

不能直连时，实例主动向已配置中继建立反向 SSH。控制器登记的端口租约以客户端、profile、实例和节点共同区分；实例反向监听仅在中继 loopback 上。隧道公钥被限定为相应端口转发，控制器客户端公钥被限定为 forced command。不同于管理员安装身份，这些运行身份没有通用远程管理 shell。

中继服务不会把任意客户端提供的 `host:port` 直接当作可代理目标；代理通过已注册的不透明租约 ID 查找目标。撤销租约后不会马上把仍在占用的端口分配给另一实例。SSH 连接能力与 `client` 的 profile 权限分别核验。

当前 CI 授权支持 profile 白名单、所有者标志和只读标志。非所有者不能管理客户端身份、改写学校登录凭据或停止控制器；只读身份不允许 SSH。它尚不提供单实例白名单、按 RPC 自定义角色、组织 SSO 或独立多租户服务。

实现及测试见 [connect](../internal/connect/)、[relay.go](../internal/control/relay.go)、[routes.go](../internal/control/routes.go)、[install](../internal/install/)。

## 生命周期的一致性语义

运行记录同时保留周期开始时间、明确截止时间、策略版本、认证状态、续期状态、作业退出状态和清理状态，不能用一个“运行/失败”字段替代这些互相独立的事实。

* `--max-runtime` 从该周期开始计算；准备和排队也计时。重复 `up`、重连 SSH 和重启控制器不能重新开始计时。
* `lease set` / `job update` 只更新明确给出的策略字段。缩短到已过期的期限需要显式确认立即停止的意图。
* 续期请求完成、学校批准续期、平台实际到期时间变化是三个不同阶段。默认仍是学校普通用户请求，不模拟管理员批准。
* 停机、释放和永久删除是不同操作。默认清理停机后释放；删除必须来自明确选择的策略或命令。
* RPC 等待超时不代表平台没有执行。任务 UUID、请求指纹及父子尝试关系用于核对；结果不明时保留回执，不无条件重新创建。
* 认证失效时保留后续清理责任。`cleanup_pending` 表示还有未完成的工作，不输出“已释放”的假成功。

后台作业通过实例里的 Python 3 执行器运行：argv 通过受保护通道输入，不经本地 shell 拼接；进程组、启动标记、deadline 和状态文件用于确认或停止。执行器的 stdout/stderr 日志位于实例用户的 `~/.local/state/bupt/runs/<run UUID>/`。应用自行打印的 Secret 仍可能进入这些应用日志，工具不会假装能识别任意业务输出。

作业使用 `/dev/null` stdin。`job attach` 只是跟随输出和等待；交互工作应使用 `ssh --session NAME` 与镜像中的 tmux。日志的 stdout/stderr 分别保存，combined 显示不能保证跨流逐字节的全局顺序。

## 当前明确限制

* 当前适配特定学校 WebVPN 和 GPU 平台的已观察协议；没有一个通用的“所有高校 API”。Engine.IO 当前路径使用 EIO 3，未实现所有 polling fallback、所有 Socket.IO namespace 或任意协议升级。
* 没有自动 P2P/NAT 穿透。校外访问需要仍可达的直连路径或实例能主动访问的中继。中继运行在既有 SSH 端口，管理员仍须保证该端口可达。
* 学校的人脸、二维码、验证码和 IP 绑定规则仍生效。SSH 独立并不等于获得无限续期或绕过学校管理权限。
* 本地控制器休眠/离线时，管理轮询、续期请求和平台释放会延迟。实例上的隧道与已启动作业可继续运行；平台本身到期、实例关机或网络变化仍会中断它们。常驻服务器控制器是可选解决方式。
* 控制台连接目前传递初始终端尺寸；原生 SSH 支持窗口变化。持续 console resize 的交互通路尚未与 CLI 信号完全连接。
* 远程安装面向 Linux/systemd 的 amd64、arm64，要求 root 或无交互 sudo；本地用户服务支持 macOS LaunchAgent 与 Linux systemd。Windows 二进制、其他 init 系统、离线包管理不在本次范围。
* 实例镜像必须具备相关依赖与权限：SSH、隧道所需的客户端和出站网络；后台作业需要 Python 3；持久交互会话需要 tmux。工具不会假定所有学校模板都相同。
* 快照/磁盘扩容仅在对应 QEMU 节点能力存在时工作；资源改配、克隆、服务映射同样受模板与普通用户权限限制。拒绝、排队和审核是正常结果，不能通过 CLI 包装成成功。
* `data` 只暴露本次已适配的持久存储配置、目录、单文件上传/下载。SSH 目录复制拒绝跟随符号链接；不是完整 rsync、任意 TUS 服务器或全部 OpenSSH sftp 子命令的替代品。
* 本地/远程控制器之间不自动迁移活跃状态，也没有控制器集群、分布式选主或跨控制器锁。不要让多个控制器同时接管同一实例。
* 静态加密使用受保护的主密钥文件，也支持实现中的 systemd credential / `BUPT_MASTER_KEY` 输入；当前没有宣称 macOS Keychain 后端。丢失主密钥不能恢复密文。迁移应同时保留配置、密文、状态和对应身份。
* `doctor`、操作回执和审计提供诊断，不替代真实 SSH 成功、学校任务结果和释放后的资源状态核验。

## 验证与上线记录

`make test`、`make race`、`make vet` 覆盖本地状态、协议服务、SSH/SFTP 测试端和 CLI 行为；`make release` 验证四个交叉编译目标。关键 CLI 用例包括认证过期时直接进入缓存 SSH、精确保留远端退出码、创建前拒绝缺少运行上限、显式 false 配置、单 JSON 输出、原始字节流、失败下载不破坏旧文件、上传内容哈希及节点选择。

这些测试不意味着已在每个真实模板上完成创建、排队、启动、反向隧道、续期审批、停止、释放、恢复、快照或删除。线上验收需要逐项记录实际 profile、模板、实例、操作回执和最终平台状态；本文件不把尚未执行的线上动作写成已验证。生产变更记录应留在所属服务器的 `shared/changes/`，不在示例中保存真实 Secret。

## 平台任务状态

任务创建、状态事件和作业清理各有独立的回执。客户端使用任务 UUID 与平台快照核对结果。事件连接恢复后按版本补齐缺失的索引。实例详情中的运行状态、磁盘状态与资源预留可分别查询。
