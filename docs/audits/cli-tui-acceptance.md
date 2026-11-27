# bupt CLI 与 TUI 实现验收

2026 年 11 月 19 日 ｜ Go 1.26.5 ｜ macOS arm64 ｜ 工作树版本 0.2.0

## 结论

**PARTIAL。** 规范命令全部注册并可发现。CLI 与 TUI 查询了真实学校模型、规格和实例。模拟环境完成了登录状态恢复、一次性模型调用、密钥托管，以及实例从计费授权、创建、启动到 SSH 就绪。学校审批、最终出账、真实写入及单个 TUI 会话内完整的实例操作旅程仍有验证缺口。大部分 Unix 契约通过实际子进程验证。动态对象补全、中英文帮助的一致性、跨资源并发和若干脚本边界仍待完善。

CLI、TUI 的交互已分开。控制器集中维护权限、计费授权、状态推进、凭据和操作回执。前端仍直接拼装不少 RPC 参数，类型化 SDK 的覆盖与生产入口的实际使用范围不同。新页面和命令通常能局部变更，关键 RPC 字段改动仍可能影响两个前端。

审计从 HEAD `b98369d` 及任务开始时已有的未提交变更出发。用 `git archive HEAD` 在临时目录构建旧 CLI，用原工作树独立构建新版。旧版和新版 `help --all` 分别有 275、202 条不同的可发现路径，包括组和父入口。数量用于发现迁移能力。校网仅执行读取，写入验证用模拟学校和独立凭据。

### 证据索引

| 代号 | 验证环境 | 可观察结果 |
| --- | --- | --- |
| H | 构建后执行 `bupt help`、`bupt help --all`、每条 `bupt help COMMAND` 和 `__complete` | 验证实际注册、隐藏范围与补全 |
| C | `python3 scripts/check_cli_acceptance.py PATH_TO_BUPT`。真实子进程、临时 HOME/配置、模拟 Unix RPC | 校验 RPC 方法、操作域、结构化输出、原始字节、退出码与错误。此级测试证明客户端所发请求。 [脚本](../../scripts/check_cli_acceptance.py) |
| I | `go test ./internal/control -count=1` | 真实控制器、数据库与学校适配器替身。生命周期测试包含编译后的 CLI、PTY、Unix socket 与真实本机 SSH。 [生命周期测试](../../internal/control/instance_lifecycle_test.go) |
| P | `go test ./internal/platform -count=1` | 通过本地 HTTP/TLS、WebSocket、TUS 测试端点验证学校协议及业务响应。 [平台测试](../../internal/platform/platform_test.go) |
| T | `go test ./internal/tui -count=1` | tcell 模拟终端实际按键、焦点、页栈与回执，后端使用替身。 [工作台测试](../../internal/tui/workspace_test.go) |
| L | 当前安装的 0.2.0 控制器与本次构建的 CLI/TUI，只读校园网 | 验证真实模型、模板及实例可读 |

PASS 表示该行限定的范围内已执行对应验证。PARTIAL 表示部分达成或组合测试不完整。FAIL 表示确认不符合要求。BLOCKED 表示受真实费用、资源修改或环境限制。NOT APPLICABLE 表示明确超出适用范围。

## 规范树与迁移

| 规范分支 | 实际树 |
| --- | --- |
| `login/logout/tui` | 根级直接入口。TUI 支持区域参数 |
| `api` | `model ls/show/invoke/example`、`key ls/show/create/enable/disable/delete`、`usage`、`leaderboard`。`guide` 和 `key export` 在完整帮助中 |
| `compute` | `catalog ls/show/estimate`、`instance ls/show/create/start/stop/restart/release/restore/delete/wait/events/cancel-queue`、`storage`、`job ls/run/show/cancel/logs`、`scheduler job/request/template`、`quota`、`usage` |
| `ssh/exec/cp/forward/usage/doctor/config` | 根级入口。`config` 内含 `get/set/explain/validate` |
| `agent/client/profile/remote/project/cluster/admin` | 根级高级组，可从 `help --all` 查找。`agent gateway` 为 SSH 强制命令使用的内部入口 |
| 其他高级能力 | `auth/init/status/console/proxy/sftp/ssh-config/news/account/agreement` 与 `compute preset/lease/task/audit/snapshot/service/dataset/notice/llm/request/features` |

`api model/key`、`compute catalog/instance/job/storage` 和 `auth` 的默认子命令均只读。`api`、`compute` 根组返回各自概况。`config` 和 `compute scheduler` 默认显示帮助。C 核实默认分组指向。 [注册](../../internal/cli/root.go#L166) · [计算分组](../../internal/cli/compute.go#L35) · [API 分组](../../internal/cli/portal.go#L56) · [帮助](../../internal/cli/discovery.go)

| 旧入口 | 迁移与保留 |
| --- | --- |
| `auth login/logout` | `login/logout`。`auth` 保留会话诊断、续登及实例控制台凭据 |
| `ls/inspect`、`instance inspect` | `compute instance ls/show` |
| `up` | 新资源用 `compute instance create`，已有实例用 `start`，只接管用 `adopt`。原 `up --ssh` 拆为 `wait` 与 `ssh` |
| `down/wait/lease` | 停机用 `compute instance stop`。清理策略及周期用 `compute lease end/status/wait`。续等变更回执用 `compute instance wait --operation ID` |
| `spec/resources/estimate` | `compute catalog ls/show/estimate`。目录 JSON 按池分组，逐模板处理用 `--output jsonl` |
| 旧 `compute job`、`compute run`、顶层 `run/job` | 学校调度进入 `compute scheduler`。托管作业进入 `compute job`，`inspect` 改为 `show`。自动新建作业所用实例需要授权日价 |
| `compute ssh/exec/cp/forward/sftp/console`、`ssh ensure` | 对应根级命令。SSH 准备迁至 `compute instance prepare-ssh` |
| `data/clone/resize/snapshot/service/task/audit/preset` | `compute storage`、`compute instance clone/resize`、对应 `compute` 高级组。克隆需要日价授权 |
| `quota`、`compute admin quota`、`api admin` | 个人 `compute quota`，管理 `admin quota show/set`。变更使用 `--quota-field`。门户后台身份 `admin portal` |
| 顶层 `key`、`api key` | `client key` 管理实例登录公钥，`client` 管理控制器身份，`api key` 管理模型调用密钥。RPC 分别为 `key.*`、`client.*`、`portal.*` |
| `api model try`、`-q/--quiet` | `api model invoke`、`--output id` 或 `api usage --field balance` |

本次允许移除旧路径，`TestRemovedPathsAndUnapprovedCreateDoNotSubmit` 验证了典型入口。完整帮助中的保留能力分层验到了查询、控制器、适配器和 TUI 模拟环节。学校未开放的高级能力仍可能返回权限或能力错误。 [迁移与示例](../usage.md#脚本契约与命令迁移)

## CLI 追踪矩阵

| 规范入口 | 实现 → 底层 | 可观察证据 | 状态 |
| --- | --- | --- | --- |
| `login` | `auth.start/poll/sso.submit` → 持久挑战 → WebVPN/CAS 与学校身份核验 | C `--device --no-wait` 返回挑战。I `TestDefaultLoginFollowsBothQRStagesWithoutPassword` 在 CLI 层覆盖两阶段，控制器用 `TestCampusPasswordFlowSurvivesGatewayAndRestartAndConsumesOnce` 验证恢复 | PASS |
| `logout` | `auth.logout` → 校验活跃任务、注销和凭据处置 | C，`TestLogoutCommandPaths`、`TestAccountWatchCrossTerminalLoginLogoutAndRevocation` | PASS |
| `tui` | `openTUI` → 独立 tcell 与同 profile RPC | C 拒绝非 TTY。TUI 连续任务见专表 | PARTIAL |
| `api` | `portal.read overview` → 门户概况 | C 结构化。P `TestPortalPagesMergeNativeIDsAndRespectCookiesAndPermissions` | PASS |
| `api model ls` | `portal.read model` → 模型目录能力与价格 | C JSONL。L `source=live`、6 项。`TestPortalCommandsExposeCatalogMeasurementsAndProtectKeyExport` | PASS |
| `api model show` | 同目录，按模型 ID 查询 | C ID 输出。`TestModelExamplesAndExplicitCLITrial`；模型和 Key 的查找范围已分离 | PASS |
| `api model invoke` | `TrialRequest` → `portal.trial` → 校验账号、Key、模型权限和防重 → 网关 POST | C 请求 ID；`TestTrialChecksOwnerCurrentKeyAndScopeAndDoesNotReplay`、`TestModelGatewayPostsOnlyTheEditedRequestAndParsesEachType`、`TestModelRateLimitDoesNotResend`。真实账单见 BLOCKED 行 | PASS |
| `api model example` | 目录和 guide 读取 → `ExampleCode`；生成 curl/Python/JSON/bupt | C Python 含所选模型，未发送调用。`TestTrialTemplatesAndCustomPayloads`、`TestModelExamplesAndExplicitCLITrial` | PASS |
| `api key ls` | `portal.read key` → 模型 Key 元数据 | C；`TestPortalKeyCustodyReceiptsCacheAndAuthorization` 验证隐藏完整值 | PASS |
| `api key show` | 同目录按 Key ID 查找 | C；`TestPortalCommandsExposeCatalogMeasurementsAndProtectKeyExport` | PASS |
| `api key create` | `portal.change key.create` → 模型范围 → 学校创建与 Vault | C 操作域；`TestPortalKeyCustodyReceiptsCacheAndAuthorization` 验证防重与保管 | PASS |
| `api key enable` | `portal.change key.enable` → 学校写入 | C 核对动作，P 门户写入协议；生效传播缺实网验证 | PARTIAL |
| `api key disable` | `portal.change key.disable` → 学校写入 | C 核对动作，I 验证停用状态阻止调用；生效传播缺实网验证 | PARTIAL |
| `api key delete` | 同写通路，CLI 与控制器校验目标 ID | C 确认参数；`TestPortalKeyCustodyReceiptsCacheAndAuthorization` | PASS |
| `api key export` | `portal.export` → 所有者授权 → Vault → 私有文件或显式标准流 | `TestPortalCommandsExposeCatalogMeasurementsAndProtectKeyExport`、`TestKeyExportIsAnExplicitTerminalOperation` | PASS |
| `api usage` | `portal.read usage` → 余额、用量及模型汇总 | C 余额标量。`TestBalanceFieldAndPartialUsage` 包含余额为零。缺单次请求对账 | PARTIAL |
| `api leaderboard` | `portal.read leaderboard` → 门户榜单和隐私状态 | C，`TestLeaderboardReadableTablesPrivacyAndRankOrdering` | PASS |
| `compute` | `status.get` → 学校身份与快照、托管状态 | C；`TestStatusUsesCacheWhenLoginExpiresAndNeverSyncs` | PASS |
| `compute catalog ls` | `resources.overview` → 按资源池索引模板 | C 默认组同路由。I 可选模板。L 1 个池、3 个模板 | PASS |
| `compute catalog show` | `spec.show` → ID/唯一名称/版本 → `Spec` | C；`TestTemplateNameAmbiguityRequiresExplicitVersion` | PASS |
| `compute catalog estimate` | `spec.estimate` → 规格约束 → `Estimate` | C；P `TestCatalogEstimateAndEncodedGatewayPrefix` | PASS |
| `compute instance ls` | `instance.list` → live 或本地快照 → 合并 Run/Route | C 和 L。`TestCachedInstancesDoNotSynchronizeWithSchool` | PASS |
| `compute instance show` | `instance.inspect` → Resolve，未分配时可返回已有 Run | C 正确路由，I 创建后关联同一实例 | PASS |
| `compute instance create` | `instance.create` → 精确日价 → 持久 Run → Create/Reserve/Start/SSH | C 校验回执与无授权拒绝。I 按任务顺序、中止确认和涨价阻止实质请求 | PASS |
| `compute instance start` | `instance.start` → 验证停机和预留 → 启动同一实例 | C；`TestAllocatedInstanceStartReturnsSameReceiptAfterRunning` | PASS |
| `compute instance stop` | `instance.stop` → StopAppTask → 平台状态 | C 与 I，温和停止且保留预留。人工停机未确认时保留原截止 | PASS |
| `compute instance restart` | `instance.restart` → 持久停启两步 → SSH | C 与 I 验证同实例、原截止和任务次数。交叠手动操作组合测试不足 | PARTIAL |
| `compute instance release` | `instance.release` → AppUnreserveTask，要求停机 | C 与 I 核对真实模拟任务和预留 | PASS |
| `compute instance restore` | `instance.restore` → AppReserveTask → 原实例分配完成 | C 与 I，同一 ID 且未启动 | PASS |
| `compute instance delete` | `instance.delete` → 确认且已停机释放 → RemoveAppTask | C 与 I 错 ID 零删除、正确 ID 一次 | PASS |
| `compute instance wait` | `instance.wait` → 平台状态或回执，默认实际 SSH 认证 | C 状态参数、SIGINT。I `ssh_state=ready` 与真实本机 SSH | PASS |
| `compute instance events` | `instance.events` → 本地控制器审计记录 | C 返回请求；来源标注 `controller_audit`，缺学校全量事件时间线 | PARTIAL |
| `compute instance cancel-queue` | `instance.cancel-queue` → 按队列选学校任务 → 等离开队列 | C；`TestCancelQueueWaitReturnsAfterSchoolStateChanges` | PASS |
| `compute storage` | `stores/ls/mkdir/rename/rm/push/pull` → `data.*` → 文件与流适配 | C 各操作、上传和下载。`TestStorageTUSResumeStreamingAndTraversal`、`TestStorageEditsStayInsideSelectedDirectoryAndRequireDeleteConfirmation` | PASS |
| `compute job ls` | `job.list` → 仅托管 Job | C；I 作业状态独立于实例别名 | PASS |
| `compute job run` | `job.run` → 实例、加密意图、启动与归档 | C 参数。`TestJobIntentPrivacyLaunchRecoveryAndEncryptedArchive`、`TestRunForegroundKeepsJSONSeparateAndReturnsRemoteStatus` | PASS |
| `compute job show` | `job.get` → 专用 Job | C；`TestJobWaitReturnsForExpiredAndExternallyStoppedRuns` 使用同一读路径 | PASS |
| `compute job cancel` | `job.cancel` → Job 的取消和策略清理 | C 路由，I 任务终止清理。真实调度取消和断链交错未覆盖 | PARTIAL |
| `compute job logs` | `job.logs` → 当前 SSH 或加密归档 → 字节帧 | C 原始流。`TestJobLogsUseEncryptedArchiveAfterCleanup`、`TestJobAttachReadsLogsWithoutRelaunch` | PASS |
| `compute scheduler job` | `job/ls/show/logs` → `compute.read job/job.show/job.log` | C 查询域与 cluster 参数。真实学校调度状态和大日志未查 | PARTIAL |
| `compute scheduler request` | `request/show` → `compute.read job.request[.show]` | C 均路由正确，实网记录未查 | PARTIAL |
| `compute scheduler template` | `template/show` → `compute.read job.template[.show]` | C 均路由正确，实网模板未查 | PARTIAL |
| `compute quota` | `quota.get` → 本人账户 → `CapacityFromQuota`，申请走单独方法 | C 数组输出。`TestQuotaRequestBindsActualOwnerAndConvertsUnits` | PASS |
| `compute usage` | `compute.read billing` 及算力账单分页 | C 正确查询。T `TestUnifiedLedgerPeriodFilterAndExactPoints`。未和真实结算核对 | PARTIAL |
| `ssh` | 名称或 ID → 缓存 Route → SSH 认证 | C 与 I。本地 SSH 在学校认证过期替身下返回远端状态 37 | PASS |
| `exec` | `--` 后的 argv → 无 PTY SSH → 分离字节、退出状态 | C 二进制和退出 124。`TestNativeExecPreservesArgumentsAndExitWithoutSchoolAuthentication`、`TestExecSeparatesRemoteStatusFromTransportFailure` | PASS |
| `cp` | `INSTANCE:PATH` → SFTP → 原字节与原子本地写入 | C 管道输入输出。`TestSFTPAtomicUploadAndDownload`、`TestFailedDownloadKeepsExistingFileAndRemovesPartial` | PASS |
| `forward` | 默认 127.0.0.1 listener → `connect.forward` → SSH 目的端口 | C 实际 listener、字节及 SIGINT。`TestFramedForwardAndBound` | PASS |
| `usage` | `client.ReadUsage` 读取 API 用量和算力账单 | C 两域查询。`TestBalanceFieldAndPartialUsage` 覆盖单域失败。算力汇总只含当前页 | PARTIAL |
| `doctor` | 本地配置检查或 `doctor` RPC | C 路由，配置及缓存路由测试。真实中继故障未注入 | PASS |
| `config` | `get/set/explain/validate` → 分层配置与文件 | C 验证。`TestLayerPrecedenceAndProvenance`、`TestSetFileIsAtomicPrivateAndRejectsSecret` | PASS |
| `agent` | 本地服务管理与独立 RPC | C status。`TestSocketDryRunDoesNotStopAgentOrOpenStream` 同验预览与实际停止 | PASS |
| `client` | `client.*` 与 `key.*` 保持分离 | C 路由，`TestCIClientRegistrationUsesOnlyPublicKeyAndSelectedScope`、`TestConcurrentEnrollmentPreservesAllKeys` | PASS |
| `profile` | 本地可信配置及账号选择 | C `profile ls`，`TestSelectionAndControllerOverrides` | PASS |
| `remote` | 目录/订单 → `compute.remote.*` 与原生报价 | C 读取、P `TestRemoteCheckoutReadsQuoteAndReturnsOnlyOrderReceipt`，实网订单未知 | PARTIAL |
| `project` | `compute.read project[.show]` → 验证成员及角色 | C；`TestProjectDetailPrefersEffectiveMembership` | PASS |
| `cluster` | `compute.read cluster/partition` → 学校调度适配 | C 读取，未实测服务端角色和全部模块 | PARTIAL |
| `admin` | `compute.read admin.*`、`compute.admin.*` → owner、学校角色及版本 | C `OWNER_REQUIRED`；P `TestAdminLimitRequiresRealRoleAndReviewedRevision`。实网写入 BLOCKED | PASS |

命令和下游链可交叉查阅 [控制器路由](../../internal/control/router.go) · [实例控制](../../internal/control/instance_lifecycle.go) · [模型调用](../../internal/control/model_trial.go) · [Portal 写入](../../internal/control/portal.go#L195) · [算力页面](../../internal/control/compute_pages.go) · [转发与文件流](../../internal/control/streams.go#L104)。

## 生命周期、费用与危险边界

| 规范状态或边界 | 验证证据和行为 | 状态 |
| --- | --- | --- |
| 创建 → 审批或排队 → 启动 → 运行 | I 按创建、预留、启动、SSH ready 的顺序推进。学校长时间审批、异常和多次状态交错缺完整组合试验。 [状态机](../../internal/control/runs.go#L837) | PARTIAL |
| 创建预览或拒绝确认 | PTY 输入 `n` 退出 130，未返回回执。非交互缺少、无效或略低日价均零创建 | PASS |
| 通过审核之后日价变化 | `TestAllocationRechecksAuthorizedRateBeforeCreating` 报价略高，状态 failed，无 CreateAppTask。授权规则也应用于会分配新实例的作业和克隆 | PASS |
| 仅分配然后手动启动 | `TestCreateCostAuthorizationAndAllocateOnly` 观测 CreateAppTask=1、AppReserveTask=1、StartAppTask=0。显式 start 后状态 ready | PASS |
| 相同键的重复创建与启动 | 原操作回执复用。`TestAllocatedInstanceStartReturnsSameReceiptAfterRunning` 改参数返回 `IDEMPOTENCY_CONFLICT` | PASS |
| 同键并发与重复名称 | `TestConcurrentDuplicateTaskSubmitsOnce`；待处理同名 Run 拒绝新意图。不同键、学校外部变更及跨控制器并发未穷尽 | PARTIAL |
| 提交成功但丢响应 | `TestCreateRetainsRecoverableIDWithoutDuplicateSubmission`、`TestDuplicateTaskReceiptReconcilesOriginalResult` 和 `TestTaskRetryReusesExactUUIDAndPayload`。本地去重及同 UUID 恢复通过，学校真实去重期限未验证 | PARTIAL |
| 排队取消 | `TestCancelQueueWaitReturnsAfterSchoolStateChanges`。预留排队变为停止，仅一次取消，无随后启动 | PASS |
| 停止中与已停机 | I 验证普通停机保留预留。`TestNormalStopWaitsAtLeastSixtySecondsBeforeForce` 确认先等待，新的周期默认关强停。`TestUnconfirmedManualStopKeepsOriginalDeadline` 确认原期限无丢失 | PASS |
| 重复停机、重复释放 | 同键命中已持久回执。新键可发重复请求，缺专门的已满足状态回执。学校拒绝时由任务状态提示 | PARTIAL |
| 释放、恢复和永久删除 | I 检查同实例 ID、停机和预留分别变化，删除核对目标 ID。创建完成、释放完成与生命周期状态跨时序出现仍可能需要重查 | PARTIAL |
| 超时、取消等待和失败重查 | C 超时与 SIGINT。I `TestWaitActionErrorRetainsDurableRun`。P `TestTaskStaleCodeAndAmbiguousTimeout`。会返回待确认状态与回执供查询 | PASS |
| 校园会话过期且 SSH 路由有效 | I 认证过期替身下真实本地 SSH 返回远端退出 37。`wait` 仍会尝试读取学校状态，直接 `ssh` 使用缓存路由 | PASS |
| 模型请求超时、费用结果未知 | 网关单次 POST，无重定向和模拟自动重发。控制器持久请求标识，CLI 错误或 TUI 页面引导核查用量。实网逐笔费用无法独立验证 | PARTIAL |
| 校园平台真实写入、计费起点和结算 | 实例、Key、权限、订单、模型推理和账单归因需要另行授权 | BLOCKED |

## TUI 追踪矩阵

| 用例或契约 | 入口、实现和验证 | 状态 |
| --- | --- | --- |
| 登录 → 回到受保护页面 | `onboarding` 和工作台 → `TerminalAction` → 共用登录通路。`TestLoginAtBlockedReadReturnsToSamePage`、`TestLoginRefreshesMetadataOnExamplesAndTrialPages` | PASS |
| 选模型 → 编辑 → 调用 → 看结果 → 改后再调 | `model_trial.go` 与 `trial_editor.go` → `portal.trial`。`TestTypeAndSendOwnTextInTUI`、`TestTrialPurposesSwitchModelAndKeepDraft` 覆盖输入和结果；实网仅看目录 | PASS |
| 模型 → 创建或选 Key → 生成与复制代码 | `examples.go/services.go` → 校验范围 → `ExampleCode` → 剪贴板或文件。`TestIntegrationExamplesSelectEditCopySaveAndTry`、`TestExampleKeyPreparationReturnsRequestedScopeAndPublicID`；真实剪贴板未验证 | PASS |
| 目录 → 创建 → 等待 → 连接 → 查看 → 停止 | `instance_form.go/actions.go/work.go` → 创建、状态、Route、停机任务。`TestFirstInstanceFromKeyboardKeepsTemplateAndPreviewsBeforeCreating`、`TestRunProgressUsesTheMatchingNodeRouteAndExistingBound`、`TestAllocationWaitsForSnapshotThenTracksStopOnSameInstance`。各段已测，完整旅程未在同一 TUI 进程跑通 | PARTIAL |
| 余额 → 模型用量 → 算力账单 → 明细 | `usage_workspace.go/portal_metrics.go` → 两域读取。`TestUsageSeriesMissingDaysAndPeriodSwitch`、`TestUnifiedLedgerPeriodFilterAndExactPoints`；真实账单未对账 | PASS |
| Esc、Enter、Tab、Shift+Tab、选择器回跳 | `keyboard.go`、`workspace.back`。`TestRightBrowsesOnlyAndStaticMouseTargetsAreInert`、`TestReaderScrollThenTailActionAndReturn`、`TestKeyManagementPreservesScopeAndReturnsToSelection` | PASS |
| `?` 上下文帮助 | `TestContextHelpDiscoverableAndScrollableOnSmallTerminal` | PASS |
| 深层返回保留焦点、滚动、选择及草稿 | 页面栈与按 ID 恢复。`TestWorkbenchRoundTripPreservesCodeContextAndModalCancellation`、`TestAccountWatchReconnectsWithoutLosingDraftOrFocus` | PASS |
| 有编辑内容退出 | 模型编辑器 Esc/Ctrl+S/Ctrl+C 保存到当前草稿。通用字段 Enter 接受、Esc 取消当前编辑，部分字段取消时没有明确反馈。退出进程后不能恢复草稿 | PARTIAL |
| 后台进度和可恢复错误 | `TestInterruptedWaitAndRejectedKeyHaveDistinctRetryBehavior`、`TestChatSampleAndUncertainCallLinkToUsage`、`TestRemoteOrderRecoverFromQuoteChangeAndUnknownReceipt`。响应未知可核查，单次扣费无法确定 | PASS |
| 高风险确认 | 创建 preview 和日价绑定，删除核对目标 ID。模型按钮明确标「按用量计费」，点击动作即提交，缺独立二次确认 | PARTIAL |
| 80×24 与较窄终端 | `TestProgressiveScreenFitsSmallTerminalAndIgnoresPastedCommands`、`TestNarrowNamesFieldsAndSnippetHints`、`TestMixedStatsOnSmallTerminalRemainReadable`。最小 28×10 以下会显示尺寸提示 | PASS |
| 真实学校登录及实网写入旅程 | 本次使用既有会话，实际收费、改 Key、停机和删除未获授权 | BLOCKED |

TUI 输入、表单和提交入口见 [键盘](../../internal/tui/keyboard.go) · [模型](../../internal/tui/model_trial.go#L522) · [实例](../../internal/tui/instance_form.go) · [通用表单](../../internal/tui/form_operations.go#L78)。

## Unix 契约逐项验证

| 契约 | 实际证据 | 状态 |
| --- | --- | --- |
| 1. stdout 数据与 stderr 诊断 | C 验证 API JSON、角色错误和原始 SSH stdout/stderr 分离。创建下一步只写交互 stderr | PASS |
| 2. JSON/JSONL | C 实际解析。`TestJSONErrorsStayOnStderr`、`TestJSONLAndQuietListsAreComposable`。部分可选高级列表的空结果契约仍未统一 | PARTIAL |
| 3. 输出和字段 | C `--output id/json/jsonl` 与 `--field balance`。`--output table` 为表格，ID 只在支持稳定标识的资源上启用 | PASS |
| 4. `-q` | C 退出 2，按迁移表选输出或余额字段 | PASS |
| 5. `--` | C argv 中保留 `--json -q`，缺分隔退出 2。`TestNativeExecPreservesArgumentsAndExitWithoutSchoolAuthentication` 检查实际 argv | PASS |
| 6. `-` 标准流 | C 比对 `cp` 和 `compute storage` 两方向。stdin 上传要求准确的 `--size` | PASS |
| 7. 原始字节 | C 比对 NUL、ESC、无效 UTF-8 和换行。程序 stderr 独立。远端可以自行发 ANSI 字节 | PASS |
| 8. 非 TTY、管道和重定向 | C 把 `y` 重定向给未授权创建，退出 2 且无 RPC。TUI 拒绝非 TTY。普通登录重定向时进入非交互，显式等待仍可用 | PASS |
| 9. `--non-interactive` | 抑制提示和隐式 PTY。明示 `--tty/--wait` 或受保护 stdin 按请求执行。`TestNoninteractiveCaptchaNeverPromptsOrOpensViewer` | PASS |
| 10. SIGINT、超时和退出 | C 等待和转发 SIGINT=130，RPC 超时=1 + `TIMEOUT`，传输断开 `CONTROLLER_IO`。SSH 控制信号向远端传递 | PASS |
| 11. 本地与远端退出码 | C 远端退出 124，原 stderr。传输错误带 `BUPT[SSH_TRANSPORT]`。同退出码可来自本地或远端，远端也可伪造相同前缀。 [协议](../protocol.md) | PASS |
| 12. `--dry-run` | I 验证停止预览保持 Agent 存活、写入流被拦截、创建零提交。身份与规格核验允许读取学校和更新本地缓存 | PASS |
| 13. 幂等键 | C 透传，I 核对持久回执、重复和冲突；文件写入、未知模型结果和不同动作组合没有全局原子保证 | PARTIAL |
| 14. 颜色、补全和 man | `TestInlineCaptchaNeedsRecognizedInteractiveStderrTerminal` 检查 NO_COLOR。TUI 可在 NO_COLOR 下实机使用。Bash、Zsh 脚本语法通过，Fish 仅验证生成。对象 ID 动态补全缺失 | PARTIAL |
| 15. 版本 | C 两个人类入口完全同字节，`version --json` 输出对象 | PASS |
| 16. 配置优先级 | C 用临时 HOME 与明确配置/socket，`TestLayerPrecedenceAndProvenance`、`TestProjectCannotReplaceTrustedEndpointsOrCredentials` | PASS |
| 17. 认证隐私 | `--sso-password` 为布尔开关，口令走隐藏输入或 stdin。Key 存 Vault 并显式导出。`TestPasswordStdinIsNeverRendered`、`TestTrialChecksOwnerCurrentKeyAndScopeAndDoesNotReplay` | PASS |
| 18. 帮助和文案 | H 根索引突出常用组，202 条逐条帮助可阅读，完整索引可用文本工具过滤。连接、维护和部分错误仍使用英文 | PARTIAL |

## 分层和可控变更

| 层 | 状态归属与依赖 | 变更影响 |
| --- | --- | --- |
| CLI、TUI 与 Shell | CLI 管参数、输出和退出状态，TUI 管当前页面、草稿、选择、滚动及刷新。Shell 传 argv 和字节 | 新增纯 CLI 命令通常只改 CLI，改 TUI 普通表单不改变 CLI 输出。前台认证、连接和复制仍借单个 Cobra `RunE`，共同修改时应回归 |
| 应用编排 | 控制器管理实例、作业、登录和门户用例，两个前端分别组织审阅和反馈。`client.ReadUsage` 目前由 CLI 使用 | 假 Platform、Store 和 RPC 足以单测用例。多个前端直接拼 RPC，修改方法名和字段有双端影响 |
| 领域规则 | `domain` 提供状态和模型输入、费用等类型。控制器决定本地生命周期/回执/授权。平台是资源、配额、身份与结算的权威 | 实例主要状态机集中在控制器。新增模型类型通常改 `domain.TrialRequest/ExampleCode`、平台解析及 TUI 控件，CLI 只在需要新参数时调整 |
| 控制器与通信 | `Handle` 分派，`client/wire` 走私有 Unix 或受限 SSH。Store 保存 Run/Task/Route，Vault 保存凭据和作业意图 | 层间能单独替换或测试。`router.go`、`runs.go` 较大且承担多种职责。`config_sources` 等带客户端说明的字段用于诊断 |
| 平台适配 | `platform` 负责校园 HTTP、SSO、事件、TUS 和网关。`connect` 负责 SSH/SFTP | 原生请求通常能由适配器局部改动。部分报价、账单、存储仍传原生键或 adapter DTO，改字段会影响渲染层 |

`go list ./...` 验证包间可构建且无循环。`internal/cli/tui_commands.go` 已删除。表单和面向任务的导航独立。多数前端入口直接构造 RPC 方法和参数。类型化 SDK 有独立测试。目前新增资源要改真正使用该资源的领域、控制器/适配器和前端，不能保证协议变更只改一处。此次 TUI QEMU 扩容字段与控制器不一致已经验证了这类耦合。 [表单映射](../../internal/tui/form_operations.go#L25) · [TUI 查询](../../internal/tui/workspace.go#L468) · [CLI RPC](../../internal/cli/root.go#L333) · [TUI 提交](../../internal/tui/actions.go#L149) · [前台交接](../../internal/cli/tui_operations.go#L14) · [类型化 SDK](../../internal/client/operations.go)

## 三类用户验收

| 用户 | 完成的任务 | 判断 |
| --- | --- | --- |
| 普通学生 | 空 HOME 从帮助找 `login/tui/compute`。T 验证缺控制器恢复、登录回页、挑模型编辑发送、创建 Key 后回到代码。I 实际终端走目录、审价、拒绝或创建、等待和 SSH | 主路径无需理解 RPC 或 Lease。缺控制器、多节点、缺实例控制台凭据等恢复场景仍出现工程术语 |
| 熟练开发者 | H 搜高级路径和补全枚举。C 通过 `exec` 精确传参、`cp` 双向字节和 `forward`，可核对返回值 | 命令域清楚。学校调度详情需要知道 cluster ID，常见资源仍无动态补全 |
| 自动化脚本用户 | 无终端以 JSONL 查目录、取标量余额、按授权与幂等键提交。C 注入网络断开、权限拒绝、超时和中断 | 可组合并可查回执。对网关未知结果与学校最终扣费需要人工核查，跨操作竞争仍有风险 |

## 已修复与剩余风险

| 优先级 | 基线缺陷 | 修复与验证 |
| --- | --- | --- |
| P0 | RPC dry-run 停止真实隔离 Agent | 原独立进程返回预览后退出 0。新版 `TestSocketDryRunDoesNotStopAgentOrOpenStream` 验证预览存活、真停止和直接流预览防护 |
| P0 | 通过托管作业、克隆、SDK Up 分配无当次日价授权，或异步阶段使用旧报价 | 共用底层精确比较，后台构建任务再核对。`TestAllocationRechecksAuthorizedRateBeforeCreating`。原入口需新授权参数 |
| P0 | 未确认的人为停止使周期一直绕过原有到期策略 | 保留成功停机的原语义，无法确认则在原期限执行预定清理。`TestUnconfirmedManualStopKeepsOriginalDeadline` |
| P1 | 仅分配回执早，随即启动失败，原启动键重试无恢复记录 | 等待预留和去重收据。`TestCreateCostAuthorizationAndAllocateOnly`、`TestAllocatedInstanceStartReturnsSameReceiptAfterRunning` |
| P1 | CLI 管道上传尝试 seek，cp 无 stdin 长度参数 | 增加 `--size`，按实际偏移定位。C 检查原始字节 |
| P1 | TUI 磁盘扩容错误字段 | 接到实际 `size_gb`。控制器资源测试与 `TestDiskExpansionFormBindsControllerSizeInGB` |
| P1 | 缓存可能同步学校，RPC 超时给底层错误，等待可能丢回执 | `TestCachedInstancesDoNotSynchronizeWithSchool`，C 结构化网络错误，I 恢复回执 |
| P1 | 并发公钥登记初始化锁偶发 ENOENT | 基线完整非缓存测试和 35 次复跑均复现。排他创建和有界重试后复跑 70 次成功。 [实现](../../internal/connect/enrollment.go#L125) |
| P2/P3 | 版本、空列表、输出提示和相关说明不一致 | C 对版本、ID/JSONL、字段、帮助和退出进行实测。更新 man、迁移指南与分层说明 |

剩余主要问题为不同操作并发、重复停机的新键语义、真实收费和审批、TUI 真机完整变更旅程，以及 RPC 类型约束和动态对象补全。没有发现已被验证的旧能力回归。高级能力逐条业务执行和校方可用性尚无充分证据。

真实校网只读返回了 6 个模型、1 个资源池、3 个模板和 1 台停机实例。未发送计费模型请求，未修改 Key、权限、实例、订单或账单，也未重启已安装控制器。最终控制器修复在离线隔离环境验证。

实网使用 `api model ls --refresh --output json`、`compute catalog ls --output json` 和 `compute instance ls --output json`，三条命令均退出 0。前两条返回 `source=live`。用同一临时二进制在 80×24 PTY 打开 `tui api`，按 2 后匹配全部 6 个模型 ID。打开 `tui compute`，按 1 匹配唯一实例名称，按 3 进入资源目录并确认其中两个模板在终端输出出现。第三个模板只在 CLI 响应中逐项确认。三个 TUI 会话均按 `q` 退出，状态 0。TUI 实网记录只验证打开页面和读取，另外的 TUI 操作依赖模拟测试结果。

## 回归命令与结果

~~~sh
audit_dir="$(mktemp -d)"
go test -count=1 ./...
go vet ./...
go test -race -count=1 ./...
go build -ldflags "-X main.version=$(cat VERSION)" -o "$audit_dir/bupt" ./cmd/bupt
"$audit_dir/bupt" help --all
python3 scripts/check_cli_acceptance.py "$audit_dir/bupt"
go test ./internal/connect -run '^TestConcurrentEnrollmentPreservesAllKeys$' -count=70
MANPAGER=cat man ./docs/bupt.1
mandoc -Tlint docs/bupt.1
~~~

| 验证 | 结果 |
| --- | --- |
| 基线 `go test ./...`、`go vet ./...` | 两者退出 0，测试命中缓存。首次执行非缓存全量时并发公钥登记暴露偶发错误 |
| 最终 `go test -count=1 ./...`、`go vet ./...`、`go test -race -count=1 ./...` | 均退出 0 |
| 构建、完整帮助、CLI 子进程 | 三者退出 0，共 287/287 项，含 202 个帮助路径，实际原始流、掉线、权限和 SIGINT |
| 公钥并发 70 次、Bash/Zsh 校验、Fish 生成 | 均退出 0。工作机无 Fish，运行检验 BLOCKED |
| man、本次源码和文档 diff | `man` 渲染与 `mandoc -Tlint` 均退出 0。源码及文档 `git diff --check` 通过。原工作树的两份 ANSI 演示帧有固定列宽末尾空格 |

[使用说明](../usage.md) · [架构](../architecture.md) · [学校协议](../protocol.md)

## 2026 年 11 月 20 日最终收敛与复验

本节追加于原验收记录。核心 CLI 与 TUI 闭环通过离线端到端验证，校园网只读复验通过。生产 LaunchAgent 原地换版与校园网带副作用的验收仍有外部阻塞，整体交付结论为 **PARTIAL**。

### 原 PARTIAL 项目处置

| 事项 | 完成内容及证据 | 状态与环境 |
| --- | --- | --- |
| 停机/释放重复、不同操作交叠 | 以学校实例 ID 识别资源。在已有持久未决操作时合并重复停止或释放，返回原任务 ID。已经停机/释放时返回已核对的无操作回执。存在冲突时返回 `INSTANCE_OPERATION_PENDING`。并发用例对停机、释放、恢复、启动、重启分别卡住学校提交，并行交叉提交其他动作并验证收据。`TestLifecycleConcurrentInterleaving`。 [资源协调](../../internal/control/lifecycle_coordination.go) | PASS · 模拟平台端到端 |
| 丢失响应、断线、重启 | 提交前把授权请求及重建参数一并写入持久库。重开状态库后使用同一原任务。期间新键停止合并、反向启动被拦截。出现学校收据后确认状态，再允许启动。`TestLifecycleLostReplyReconcilesAcrossControllerRestart`。 [收据处理](../../internal/control/tasks.go#L531) | PASS · 隔离控制器 |
| 学校传输重试及重复事件 | 不在掉线、超时或快照遗漏后重发 TASK。明确拒绝 1165 才重新核验并以新 ID 尝试一次。真实本地 WebSocket 验证等到原重发时限之后也只有一帧。老回调不能回退已确定的终态。`TestUnacknowledgedTaskIsNotSentAgain`、`TestDuplicateTaskReceiptReconcilesOriginalResult`。 [事件适配](../../internal/platform/socket.go) | PASS · 协议端点 |
| 创建、恢复预留的价格授权 | 创建、按规格新建作业及克隆延续预览与限价保护。显式恢复也须授权核对过的积分日价，并绑定原实例规格、节点资源及归属，提交学校任务前再估价。复验 `TestAllocationRechecksAuthorizedRateBeforeCreating`、`TestCreateCostAuthorizationAndAllocateOnly` 和包含恢复动作的生命周期用例。 [费用校验](../../internal/control/instance_lifecycle.go) | PASS · 模拟平台端到端 |
| 平台事件与本地审计混淆 | `instance events` 分开输出当前学校快照中的可关联回执与本地审计。学校历史标记不完整。联网失败可使用缓存并标明来源或错误。`TestInstanceEventsSeparatesSchoolReceiptsAndLocalHistory`。 [事件查询](../../internal/control/instance_lifecycle.go#L131) | PASS · 隔离控制器。学校仅提供有限历史 |
| 模型超时及费用未知 | 同一键保持原请求标识。结果未知时，对同样的模型、Key 和输入，即使使用新键也提示先核查，保护跨控制器重启有效。只有明确核对后的 `--resend-after` 或 TUI 确认允许新调用。TLS 请求测试验证余额拒绝与已收到 POST 但丢失响应，前者明确报余额不足，后者无重放。`TestModelUnknownEffectRequiresReconciliationAcrossControllerRestart`、`TestModelGatewayDistinguishesBalanceRejectionAndLostResponse`。 [模型调用](../../internal/control/model_trial.go) | PASS · 模拟网关与控制器 |
| TUI 一次会话闭环 | 同一真实 TUI 子进程执行四条任务，下节有可观察结果。修复前台 SSH 返回时键盘通道关闭和旧读者残留，同时验证正常退出。 [前台切换](../../internal/tui/workspace.go#L491) · [终端输入](../../internal/cli/terminal_input.go) | PASS · PTY、模拟学校及真实本机 SSH |
| 导航、取消及草稿 | Esc 取消字段、返回核对页及等待回执时有不同反馈。早先筛选、输入和 Key 在代码/调用页切换后恢复。持久草稿保存有效时限、分配方式和模型数值参数，支持恢复和清除。普通文本、完整 Key、口令、授权和返回内容只在对应工作流处理，不进入草稿格式。`TestDraftRecoveryVersionAndPermissions` 和终端重启验证。 [草稿存储](../../internal/tuiprefs/drafts.go) | PASS · 单元与 PTY |
| CLI 动态补全和输出 | 实例名称、实例 ID、模板和 `--spec` 使用选择的 profile 及控制器缓存。完成请求 RPC 预算 350 ms。Bash、Zsh、Fish 均实际运行生成脚本。空集合以 JSON 数组表示，JSONL 零记录为空流，结构化权限和传输错误保留在 stderr。 [补全](../../internal/cli/completion_data.go) · [Shell 验收](../../scripts/check_shell_completion.py) | PASS · 实际 Shell 和 Unix RPC 替身 |
| 完整账单与范围 | CLI `usage --days N` 与 TUI 共用学校时区和算力读取规则。读取学校全部可用页、去重、检查总数和记录漂移。分页中断保留已有记录和续查页。日期/金额缺失禁止完整合计，预估、未结算和学校已公布计量分开。`TestComputeLedgerPaginatesAndAccountsForUncertainty`、CLI 子进程 JSON 和 TUI 两页明细。 [分页和精确积分](../../internal/client/usage.go#L101) | PASS · 模拟平台端到端 |
| 架构契约 | 跨端模型调用类型含模型、授权 Key 元数据、输入及请求键。实际字段调整将扩容 Go 字段改为 `TargetCapacityGB`，两端直接绑定同一结构。RPC `size_gb` 到学校 `newSizeInGb` 的转换集中在控制器。扩容前旧路径曾在 CLI/TUI 分别写字符串键。 [领域请求](../../internal/domain/requests.go#L135) · [学校任务转换](../../internal/control/tasks.go) · [表单](../../internal/tui/form_operations.go) | PASS · 类型契约与平台请求测试 |
| 安装、启动、回滚 | 实际 `make install` 到私有暂存前缀。新版本、旧版本、重新上线新版本依次正常启动，对原影子数据库和配置执行健康检查并正常停机。 [部署记录](#部署与真实平台) | PASS · 本机影子部署 |
| 本机生产控制器原地换版 | 0.2.0 LaunchAgent 正在服务。虽无活动托管作业及待办平台任务，Unix Socket 仍有与本机终端客户端的连接，不能证明重启无中断。保留服务，由影子验证替代原地切换。 | BLOCKED · 本机生产 |
| 学校模型、实例、规格、额度和账单 | 在线会话恢复后，真实 CLI 经当前安装的控制器完成只读复验，覆盖目录、详情、估价和历史账单。跨账目精确对齐受校方字段限制。 | PASS · 真实学校只读 |
| 学校真实 SSH、写入、计费和管理员操作 | 需要获准的实例、额度和账号。真实学校创建、启动、计费调用、提交/取消审批、更改 Key、权限、释放或删除均未执行。普通与授权读写控制、模拟作业收据和平台 HTTP 各自有离线验证。 | BLOCKED · 真实学校 |

### 生命周期边界

| 观察状态 | 可以提交或返回的结果 |
| --- | --- |
| 运行中且没有其他未决动作 | 温和停止或重启。请求启动时确认已在运行，沿用已有期限 |
| 停机、已获批且有可用预留 | 启动。重发停止得到已核对状态。请求恢复预留时确认已有预留 |
| 停机且预留已释放 | 可按新核对的日价恢复。重复释放为无操作确认；已停机且释放后才可明确确认删除 |
| 学校排队中 | 可按学校队列类型申请取消。仍有未决平台动作时按回执核查冲突 |
| 已发请求且没有学校定论 | 后续冲突动作被挂起或拒绝。查原任务 ID。学校或其他客户端触发的状态变化不能单凭达到了期望状态证明原未决请求已终结 |
| 有终态回执但目标状态尚未出现 | 继续读学校快照。状态确认和收据持久化后解除本资源的约束 |

规则覆盖由同一控制器管理的资源。学校客户端、其他控制器和学校非原子状态转换不会获得分布式锁。若校方快照已经丢弃原回执，控制器保留未决状态，避免把未知变成第二次执行。有明确拒绝时才允许重新核验后再发。学校真实去重窗口以及保留历史时长仍无实网证据。

### 真实 TUI 单会话

`go test ./internal/control -run '^TestTUISingleSessionPTY$' -count=1 -v` 构建 `cmd/bupt`，打开真实 PTY，由键盘和窗口尺寸变化驱动。一个进程经同一个私有 Unix Socket 连续运行 A–D。之后新开终端验证草稿跨进程恢复。 [测试外壳](../../internal/control/tui_session_test.go#L116) · [终端驱动](../../scripts/check_tui_session.py)

| 流程 | 可观察结果 |
| --- | --- |
| A 裸算力 | 选中模板，输入实例名和 21 分钟时限，看到 1.25 积分/天，提交后一台实例运行并显示 SSH。远端测试 SSH 收到 `terminal-check` 与 `exit`，原实例页和终端模式恢复。停止后平台观测为 stopped，预留仍在。创建、启动及停止各一条，零强停及零释放。 |
| B 模型 | 选择模型和 Key，空输入不能触发调用。编辑和再次发送显示不同回复、6 Token 及单笔 1.25 积分。模拟响应未知按核查页面处理，另外覆盖失效 Key 和余额不足。两个成功及一笔结果未知合计记账 2.75 积分。 |
| C 程序接入 | 同一个会话把第二次调用的模型、输入与 Key 传给 Python 工作台。保存到新文件，验证文件含指定模型和正文。返回调用页时保留原筛选、Key 和草稿。 |
| D 费用 | 从总览切换模型用量到 30 天，再到算力，学校两页存在重复记录。合并为 12 条，显示 3.00 积分。打开第二页看到 `bill-01`，再回总览读取 27.25 积分余额。 |
| 重启及尺寸 | 80×24 发起任务，70×22 操作导航，110×34 完成模型和账单。在正常退出及 SIGKILL 后恢复已保存的参数，并通过界面清除。终端模型正文和实例自由文本未持久化。 |

### 部署与真实平台

在临时目录以 `make install PREFIX=... VERSION=0.2.0-acceptance.20261120` 构建并安装。对单独的配置、数据库、主密钥及 Socket 使用 `init --mode=local --install=false --start=false`，然后在前台运行 `agent run`。`config validate`、`version --json`、`agent status --json` 均通过。候选控制器协议为 1，当前版本、活动 Run、待办任务、其他连接可分别查询。检查影子数据库和主密钥权限 0600，配置项跨原子二进制替换保持一致。停止每个影子进程后确认 Socket 清理。回滚使用安装中旧二进制的副本，顺序为候选版本 → 0.2.0 → 候选版本。

最终影子启动 PID 为 47470、47490、47496，健康检查均退出 0。候选二进制 SHA-256 为 `c8d1552c1514dd437ef6ba59203b0a4a1994f1d03f812103d21ef035291d36bd`。回滚副本 SHA-256 为 `b98035f2f82fc701f85c4d021ac20702307a8f95132c27ba3d2296a1cc0e3b68`。

生产环境仍是 `~/.local/bin/bupt` 及 `cn.edu.bupt.cli.bupt-agent`。生产 PID 2348。已有 10 条历史周期、1 条路由，当前 0 个活动周期、0 个待办任务。`lsof -nP -a -p 2348,41758 -U` 看到控制器和本机终端客户端 PID 41758 配对的 Unix Socket，生产换版的无中断条件未满足。生产文件、进程和状态库未切换。

第一次在线检查显示模型 `GATEWAY_AUTH_EXPIRED`、算力 `HTTP_302` 和统一账单 `USAGE_UNAVAILABLE`。会话恢复后，经同一生产控制器再次只读验证。`api model ls --refresh` 返回 6 项且来源为 live，详情 ID 与列表匹配。实例有 1 台停机，目录来源为 live，含 3 个规格。首个规格详情和估价均成功，估价为 12 积分/天。个人配额返回 6 项，学校账单页返回 10 条。

真实门户发布了 7 天汇总，响应缺少逐日日期。该范围标记 `API_WINDOW_UNCONFIRMED`。算力页读完整，8 条落在查询窗口，`cost` 已发布，单位字段缺失，标记 `BILLING_UNIT_UNKNOWN`。CLI 和 TUI 保留原值并标「单位未公布」，积分合计缺省。`TestSchoolCostWithoutCurrencyRetainsValueWithoutInferringPoints` 验证这一真实字段形状。实网副作用验证需要另外指定资源、范围和费用授权。

候选 TUI 通过 80×24 的真实 PTY 和生产控制器复验。`tui api` 的模型列表匹配 6/6 项，`tui compute` 的实例和资源目录分别匹配 1/1、3/3 项。所有会话正常退出。

### 复验命令

| 范围 | 实际命令 |
| --- | --- |
| 全量、竞态与静态 | `go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` |
| 生命周期、计费和收据 | `go test ./internal/control -run 'Test(Lifecycle\|InstanceEvents\|Create\|AllocatedInstance\|AllocationRechecks\|CancelQueue\|ModelUnknown\|SocketDry)' -count=1` |
| 单会话与退出草稿 | `go test ./internal/control -run '^TestTUISingleSessionPTY$' -count=1 -v` |
| 账单数值与作用域 | `go test ./internal/client ./internal/tui -run '^Test(ComputeLedger\|UnifiedLedger)' -count=1 -v` |
| WebSocket、模型 TLS | `go test ./internal/platform -count=1`，包含未决任务零重发、重复回调、模型余额拒绝与不确定响应 |
| 真实 CLI 与 Unix Socket | `python3 scripts/check_cli_acceptance.py PATH_TO_BUPT`，包含两个新增多页和部分失败 JSON 用例，共 289 项。 |
| Shell | `PYTHONDONTWRITEBYTECODE=1 python3 scripts/check_shell_completion.py PATH_TO_BUPT --fish PATH_TO_FISH`。用 Bash 原生变量、Zsh PTY 的 TAB 和 Fish 的 `complete -C`。Fish 4.9.3 位于暂存目录，修正其包内 Homebrew 依赖地址后执行。 |
| 既有公钥竞争回归 | `go test ./internal/connect -run '^TestConcurrentEnrollmentPreservesAllKeys$' -count=70` |

最终版本的全量、竞态、静态检查、候选构建、影子回滚、289/289 项 CLI 检查、三种 Shell 补全、70 次公钥并发和只读实网检查均成功。工件构建使用同一份 `cmd/bupt`，包含 CLI、TUI 和 Controller。

仍有真实校园边界。学校审批、计费起点、逐笔对账、外部客户端并发、无可见回执情况下的确认和完整事件历史，不能从模拟数据或本机健康检查推出。通用低风险表单仍有独立 RPC 映射，以上类型化变更集中于首次验收已出错的合同字段和计费入口。

## 用户授权后的实网验收

在用户授权真实操作并确认可以让 TUI 短暂断线重连后，继续校园网与生产控制器验收。算力测试仅针对账号中已有的唯一停机实例，先核对日价，再设定 5 分钟托管上限、关闭续期并选定释放清理策略。模型只发送一次极短的向量请求，Key 只绑定该模型。

| 项目 | 实际观察 | 结论 |
| --- | --- | --- |
| 生产控制器换版 | 预检确认无运行作业、待办任务和中继。原二进制已备份至 `~/.local/bin/.bupt-pre-live-acceptance-nbg0yy5m`，原子替换后 LaunchAgent PID 从 2348 变为 28801。RPC 返回候选版 `0.2.0-acceptance.20261120` 和健康状态。 | PASS |
| TUI 重连 | 换版后未观察到原终端客户端原地重连。重新打开的真实 80×24 TUI 从新控制器读到 6/6 个模型、1/1 台实例及 3/3 个规格，随后正常退出。 | 新会话 PASS，原会话重连 BLOCKED |
| 临时模型 Key | 创建仅含 `bge-m3` 的临时 Key，核对控制器已保管凭据，停用与启用状态均经刷新验证，删除后再次刷新确认不存在。首次检查误用了字段名，已清理首把临时 Key 并用正确字段重新完成验收。 | PASS |
| 真实模型调用 | `bge-m3` 目录报价为 1 积分/百万 Token。单次请求返回 1024 维向量和 4 个输入 Token，网关未返回请求标识或扣费数值，门户本期用量也没有逐笔账目可供归因。 | 调用 PASS，单笔扣费 BLOCKED |
| 恢复预留、启动与 SSH | 候选控制器预览原实例恢复报价为 `2.4000000000000004` 积分/天，原样用作上限。托管周期回执前缀 `9ba57cd4`，学校任务回执前缀 `f9175e71`。等待超时后结束本次托管。超过原 5 分钟截止时间复查，实例仍显示停机且未预留，任务仍待核对。未启动、未 SSH、未再次提交。 | BLOCKED |
| 新建、审批与排队取消 | 实例配额 1 台且已全部占用，模板均自动审批。没有单独的可用测试名额或排队审批记录。 | BLOCKED |

现场另外发现，已请求结束的托管周期可能在旧快照仍显示未预留时提前记为已释放，即使上游恢复任务尚在等待。源码现已在既有实例清理分支检查未决资源操作。等到任务结果明确后才重新读快照并决定是否释放。新增回归模拟迟到的预留成功，确认清理在等待期间保持未决，学校最终生效后提交一次释放。 [清理状态机](../../internal/control/runs.go#L986) · [迟到预留回归](../../internal/control/lifecycle_coordination_test.go#L232)

`go test -count=1 ./...`、`go test -race -count=1 ./...` 和 `go vet ./...` 通过。清理修订版单独构建验证。为继续核查现场待确认的学校回执，生产控制器仍运行本次先行部署的候选版。

### 2026 年 11 月 20 日算力与 SSH 实网复验

独立的只读学校事件连接读到了旧预留 `f9175e71` 和启动 `0a07397c` 的 `complete` 回执，已有控制器连接此时还显示停机、启动待确认。两个旧任务均只从学校视图回收原回执。旧周期的停机 `cdbc5627` 和释放 `746b3371` 随后均完成，实例回到停机且未预留。旧周期曾经过本地截止时间才确认清理。学校排队或通信延迟时，控制器会持续跟踪清理，学校的实际停机及释放时间由回执确定。

平台适配器发出 `READY_RECEIVE_EVENT` 后才允许写入任务帧。未决任务触发的全量学校视图补查按连接限制为每 10 秒最多一次。控制器重启后也能用全量视图核对旧 UUID，等待实例状态变化的托管周期和手工回执沿用这一机制。只有读快照的请求会因漏回执而自动重复。CLI、TUI 和持久化数据的请求格式沿用现有契约。 [事件传输与补查](../../internal/platform/socket.go#L451) · [控制器任务对账](../../internal/control/tasks.go#L531) · [托管状态核对](../../internal/control/runs.go#L955)

| 项目 | 实际观察 | 结论 |
| --- | --- | --- |
| 恢复预留 | 唯一已有实例 在试前为停机、未预留。预览积分日价 `2.4000000000000004`，原样作为授权上限。周期 `905e34a4` 限时 5 分钟，关闭续期和强停。学校恢复回执 `f300ac0f` 为 `complete`，实例停机且已预留。 | 实网 PASS |
| 启动和等待 | `compute instance start` 沿用同一托管周期及原截止时间。学校回执 `02642afe` 完成。首次短等待超时，下一次 `compute instance wait` 核实实际 SSH 认证，返回 `running`、`ssh_state=ready`。期间仅继续等待。 | 实网 PASS |
| SSH | `bupt ssh -T INSTANCE_ID -- printf %s bupt-ssh-live-ok` 退出 0，stdout 精确匹配 `bupt-ssh-live-ok`。 | 实网 PASS |
| 停机和释放 | `compute lease end RUN_ID --cleanup release` 触发普通停机 `17825ffc` 和释放 `45fd5971`。本周期恢复、启动、停机、释放各一份回执，均为 `complete`。最后实例为停机、未预留，活动周期和未确认任务均为 0，SSH 路由为停机。 | 实网 PASS |
| 新建与排队审批 | `compute quota` 显示账户实例限额 1、用量 1，GPU 用量为 0。实网目录的 3 个模板均标记自动审批，因此现有账户缺少可用新建名额和人工审批案例。新建、排队与取消继续以隔离测试为证。 | 实网 BLOCKED |
| 单笔积分对账 | 学校有估价及账单读取接口，仍缺少能与上述任务 UUID 匹配的逐笔扣费数据。 | 实网 BLOCKED |

在真实停机回执到达、路由清理尚未结束的间隔里，实例列表曾显示缓存的 SSH 就绪。列表、详情和状态现在依据已观测的停机状态报告 SSH 停机，保留路由元数据供认证过期时的连接逻辑使用。 [状态投影](../../internal/control/routes.go#L19) · [回归测试](../../internal/control/usability_test.go#L64)

`TestPendingTaskRecoversFromFullViewWithoutResending` 在真实测试 WebSocket 上屏蔽任务回执和增量事件，模拟学校仍执行启动。测试核对任务帧字段及发送顺序、一次全量读取后恢复原 UUID 与运行状态，并验证学校只收到一次启动任务。`TestCleanupWaitsForLateRestoreBeforeReleasing` 验证取消后只能从全量快照找回的旧预留，清理保持待确认直至学校终态，随后只释放一次。 [WebSocket 回归](../../internal/platform/event_recovery_test.go#L28) · [控制器清理回归](../../internal/control/lifecycle_coordination_test.go#L238)

最终本机控制器运行 `0.2.0-acceptance.20261120-receipts.1`，协议 1，进程从 37906 换为 39950。候选版由 `go build -trimpath -buildvcs=false -ldflags '-s -w -X main.version=0.2.0-acceptance.20261120-receipts.1' -o PATH_TO_BUPT ./cmd/bupt` 构建。私有配置及 Socket 下的影子实例健康，Socket 权限为 0600，退出后清理完成。生产切换前活动周期、任务及连接均为 0，原子替换保留了回滚副本 `~/.local/bin/.bupt-pre-receipts-bxi5q2ly`。生产二进制 SHA-256 为 `a72028986e44f6b8b91ce5429948e4d2d5a7f70734af832b45d97ab929309085`。

最终只读复验中，`config validate --json` 返回 `valid=true`，`instance events` 从学校快照查到上述新旧恢复和启动回执，且 `history_complete=false`。CLI 实例详情返回 `stopped`、`reserved=false`、`ssh_state=stopped`。再次等待 SSH 的退出码为 1，stdout 空，stderr 返回 `INSTANCE_NOT_STARTED`。80×24 的真实 PTY 经生产 Socket 打开 `tui compute`，显示该实例已停止，退出码为 0。实网 SSH 命令由 CLI 完成，TUI 的 SSH 终端交接和返回原页面由隔离会话测试覆盖。

最终回归中，`go test ./...`、`go test -race -count=1 ./...`、`go vet ./...` 均退出 0。对候选制品运行 `python3 scripts/check_cli_acceptance.py PATH_TO_BUPT` 得到 289/289。单独运行 `go test ./internal/control -run '^TestTUISingleSessionPTY$' -count=1 -v` 耗时 43.37 秒，通过同一会话的算力与 SSH、模型调用、程序接入与费用明细流程，以及退出后的草稿恢复。

### 2026-11-27 · Agent 登录的会话内扫码

在 macOS arm64 的独立伪终端中启动 Pi 1.5.2、Codex CLI 0.172.0 和 Claude Code 2.1.239。每个客户端都通过自己的 shell 命令入口运行编译后的 CLI，连接临时 Unix socket 上的模拟控制器。

三个会话中，`login --agent` 的 WebVPN 步骤都显示了本地 PNG 路径。`auth poll --agent --output table` 的统一认证步骤都显示了完整字符码，Pi 可用 Ctrl+O 展开工具输出。将三个终端屏幕上的字符矩阵分别还原为图片，macOS Vision 均识别出预期的合成统一认证地址；第一步生成的 PNG 也解出了预期的 WebVPN 地址。三个会话随后均读到 `complete/authenticated`。本次验证范围为 CLI 命令、宿主显示与二维码解码，认证状态由模拟控制器推进。
