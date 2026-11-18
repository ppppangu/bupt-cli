# CLI 与 TUI 的信息结构

命令行和全屏界面共用五个主题。首页回答当前状态，主题页聚合做同一项决定所需的信息，对象页展示与所选资源相关的操作。主题定义在 `internal/navigation`。命令的选项、别名和完整索引从 Cobra 命令树生成。

- **实例**是计算资源的入口，节点、SSH、服务映射和使用期限跟随所选实例。实例的维护页只列与该目标有关的回执和操作。
- **资源与模板**用于选择要申请的资源。账号配额、学校可见的资源池、模板规格和用户预设在这里汇合。资源池按池 ID 归集模板。目录单独记录观察时间，配额保留缺失值与真实零值的区别。
- **作业与记录**区分用户程序、托管周期、平台操作回执。一个作业可以查看退出码和日志，并沿着 Run 中保存的 Task ID 定位最近的平台操作。
- **文件**先按存储位置选择访问方式。持久存储走学校文件接口，实例文件传输走所选节点的 SSH 路由。
- **账号与连接**呈现两层学校认证、当前控制器和上次验证的 SSH 路由，按需进入扫码或实例终端凭据输入。

`internal/tui` 使用 tcell 管理终端。页面栈保留父页的选择、筛选和滚动位置，刷新时按对象 ID 恢复选择。后台读取返回事件到单一 UI 循环，离开的页面取消请求，过期响应按请求代数丢弃。浏览期间按需加载目录和详情。显示活跃 Run 的详情时定期读取本地回执。交互操作复用同一 profile 的控制器和已有终端处理函数。

```mermaid
flowchart LR
    CLI[CLI] --> SDK[client + domain 请求/回执]
    TUI[TUI] --> SDK
    SDK --> RPC[Unix socket / 受限 SSH RPC]
    RPC --> Core[Controller]
    Core --> Platform[学校平台适配器]
    Core --> Connect[SSH / SFTP / 隧道]
    Core --> Store[持久状态 + 加密 Vault]
```

| 边界 | 负责 | 前端不能接管的工作 |
| --- | --- | --- |
| `internal/domain` | 类型化请求、Run/Task/Route/认证回执、稳定错误码 | 不依赖 Cobra、终端、浏览器或控件 |
| `internal/client` | 同一 RPC 通路的类型化调用、取消等待、字节流 | 不自动创建、接管、续期或重新登录 |
| `internal/control` | 认证状态机、权限、幂等意图、生命周期协调 | 前端不能直接改数据库来替代操作 |
| `internal/platform` / `internal/connect` | 学校协议与独立 SSH 通路 | 不把 provider 流程复制进 TUI |
| `internal/cli` / `internal/tui` | 输入、确认、配置选择、渲染、终端管理 | 不解析 CLI 文本输出来判断业务结果 |

每个 profile 的操作都发送到其选定控制器。多个前端共享这一份状态；不让本地和远程控制器同时托管同一实例。采用“本地控制器 + 远程中继”时，远程服务承担租约与转发，本地控制器仍是该 Run 的管理者。前端不设置 RPC `client_id`；远程网关根据注册 SSH 身份写入它。

请求类型见 [requests.go](../internal/domain/requests.go)，查询/认证接口见 [sdk.go](../internal/client/sdk.go)，变更接口见 [operations.go](../internal/client/operations.go)。`Client.Call` 仍可承载尚未增加类型化封装的 RPC；业务规则始终留在控制器。

| 用户动作 | SDK / RPC | 成功回执 |
| --- | --- | --- |
| 配额和按资源池归集的模板目录 | `resources.overview` | `ResourceOverview` |
| 总览（不自动登录）、恢复等待 | `Status` / `WaitRun` | `StatusReport` / `Run` |
| 校验资源预设（无分配） | `preset.resolve` | `Preset` |
| 登录、继续等待批准 | `StartLogin` / `PollLogin` | `AuthChallenge` |
| 创建、启动、恢复、明确接管、克隆、`Up` | 对应类型化方法 | `Run` |
| 提交作业、结束周期、取消作业 | `RunJob` / `Down` / `CancelJob` | `Run` |
| 修改现有周期策略 | `UpdateRunPolicy` → `lease.set` | 带新 `PolicyRevision` 的 `Run` |
| 原子停机、释放、删除、资源/快照/服务操作、申请续期 | 对应类型化方法 | `TaskRecord` |
| 预览上述变更 | `Preview(ctx, method, request)` | 原始 JSON，形状依操作而异 |
| 应用可信配置 | `ReloadConfiguration` → `agent.reload` | `ReloadResult` |

托管的 create/start/restore 返回 Run，不能当成一次平台 Task。预览也不能当成已执行的 Run/Task：有的返回拟议 Run，有的返回请求体或操作说明；预览不保证执行时状态、额度或权限仍相同。

前端状态应分开保存：当前选中 ID、表单、待提交动作、已收到的 Run/Task/Route、认证 challenge、最近错误。`Run.State`、`AuthState`、`RenewalState`、`CleanupState`、`ExitCode` 是独立事实。例如“学校认证待刷新、SSH 可用、释放待认证”可以同时成立；不合并成一个“离线”状态。完成操作后按返回 ID 重新读取状态，不凭按钮点击、RPC 返回或任务队列受理就显示“已释放”。

认证界面提供扫码和统一认证账号密码两项。选择后复用前台登录流程。该流程调用 `StartLogin`，扫码期间按 `PollAfterMS` 安排 `PollLogin`，密码表单等待人工输入后以表单 ID 调用 `SubmitLoginPassword`，展示 `Stage`、`State`、`RequiredAction`、URL 与有效期。`webvpn → university_sso → complete` 的切换由控制器完成；前端不自行拼接第二阶段登录。接管其他 WebVPN 登录等人工决定必须由用户触发相应动作。遇到未知 action/stage 保留提示和原始状态，不猜测或自动批准。

TUI 的事件循环只发起异步请求并接收结果；关闭页面取消请求/日志订阅的 context。**取消等待不会取消后台 Run**；停止作业必须明确调用 `CancelJob` 或 `Down`。RPC 超时与 `TASK_PENDING` 也不代表执行失败：保留业务回执和幂等键，按 `task_id` / Run ID 继续查询；不能换一个键盲目重建。界面按 `domain.Error.Code` 选择动作，`Message` 只用于展示。

创建按钮可以直接构造请求，无需模拟命令行：

```go
wait, renew := false, false
maxRuntime := "2h"
request := domain.ManagedRequest{
    OperationOptions: domain.OperationOptions{
        Wait: &wait, IdempotencyKey: operationID, // 本次用户意图的稳定键
    },
    PolicySelection: domain.PolicySelection{
        MaxRuntime: &maxRuntime,
        Policy: &domain.RunPolicyPatch{AutoRenew: &renew},
    },
    TemplateSelection: domain.TemplateSelection{SpecID: selectedSpecID},
    Name: instanceName,
}
run, err := api.Up(ctx, request)
// 将结果交回事件循环；成功后用 run.ID 调用 GetRun。
```

指针字段的 `nil` 表示“不改/沿用默认”，`&false` 表示明确关闭。CLI、SDK 和 TUI 共用 `auto_renew=false` 的内置默认；只有显式配置或操作才启用续期，并且仍须有有限截止时间。SDK 不自动补 `Adopt=true`；选中已有实例不等于同意接管。`MaxRuntime` 和 `Until` 每次只选一个，并位于请求顶层，使控制器清除旧的限时方式；`RunPolicyPatch` 只携带实际修改的其他字段。编辑已有 Run 时附上展示时的 `PolicyRevision`，收到 `POLICY_CONFLICT` 后重新读取，不覆盖其他窗口的修改。资源数量用精确字符串，平台 ID 用 `domain.ID`，避免浮点精度损失。

客户端配置与运行配置有不同生效时机：

1. 表单可以用共享 `config.Load` / `Config.Preset` 解析用户选择。选择本地或项目 preset 时同时发送 `Preset` 名称和已解析的 `PresetSnapshot`；里面只允许资源与策略，不包含可信端点或凭据。`ConfigSources` 只传递非秘密的来源说明，由控制器限量合并到诊断元数据，不赋予任何权限。新增界面需要的默认偏好应放进共享解析层，不能复制 CLI 的参数处理函数。
2. 控制器把选定策略和意图固定在该 Run 中。之后改 preset、全局默认值或执行 reload，不会悄悄改变已有 Run 的 deadline；改变它必须走 `UpdateRunPolicy`。同一意图重试也不重新开始计时。
3. 修改**控制器实际使用的可信全局配置文件**后，调用 `bupt agent reload` / `ReloadConfiguration`。远程控制器不会因此自动读取客户端的项目文件。修改连接 profile 时若仍有活跃 Run 或未完成 Task，控制器返回 `PROFILE_BUSY` 并保留旧运行配置。
4. 后续若加入配置编辑，可以用 `pendingProfileChanges` 表示已编辑但尚未应用的非秘密配置；只有 reload 成功才能清除此状态。`activeRuns`/待完成任务仍存在时显示阻止原因，不自行停止它们来强行应用配置。

认证 Cookie、Token、密码、私钥和含秘密的作业 env/argv 不进入可持久化的 UI model、普通状态文件或调试日志。表单中的秘密只短暂保留在内存并通过受保护 RPC 发送；学校会话及私有作业意图由控制器存入 Vault。登录 URL/challenge 也只按有效期暂存。SSH Route 独立于学校会话，前端启动 SSH 前不强制重新登录。终端、日志、代理与文件传输继续使用 SDK 的流接口，不能经过渲染文本回传。

TUI 的模拟终端测试覆盖按对象 ID 导航、预览与单次提交、异步请求取消、筛选和窄屏渲染。CLI 提供稳定的结构化错误和非交互输出。真实终端验收通过同一 Unix RPC 通路读取平台数据和预览。

`domain.Error` 另含 `RunID`、`TaskID` 和可选 `Hint`。界面按错误码给出认证、补终端凭据、等待或检查回执等动作；Run ID 应在提交后立即保存。`Run.Name` 为稳定的实例逻辑名，复用周期提交作业时用 `JobName` 保存作业标签，避免改变后续 `up`/SSH 的目标。已停止并释放的同名周期可以启动新的有限 Run，仍绑定原实例并重新核验所属账户，不隐式复制或重建磁盘。

## 平台和两个业务入口

平台登录由同一个持久流程管理 WebVPN 和 CAS。门户与算力适配器共享 WebVPN/CAS 会话，分别核验门户用户和算力用户。学校门户的 CAS 身份采用 `cas_学工号`，GPU 服务返回学工号并核对其独立 UID。临时账号保持独立命名空间。学校要求进一步确认时记录当前 profile 的待完成服务，前台统一由 `bupt login` 续接。

门户读取、变更、密钥导出使用独立 RPC。列表和导出分别授权，完整 Key 只通过显式导出交付。缓存和凭据按可信服务配置、profile 和已核验的校园身份划分。持久请求回执约束重复创建，学校返回 429 时同一客户端遵循 Retry-After。查询不发送模型推理请求。

CLI 与 TUI 共用对象和控制器动作。TUI 的模型表、Key 编辑、资源目录和实例生命周期为原生页面，其他命令表单从当前 Cobra 命令树生成，每个值单独作为 argv 传入。
