# bupt 使用手册

第一次安装请打开随包的「使用说明.txt」。中继示例中的 `./dist/bupt-linux-amd64` 是从源码构建后的路径。使用师生安装包时，选择解压目录下 `bin/` 中与服务器架构对应的程序。

## 从总览开始

`bupt` 打印命令索引并返回 shell。全屏界面使用 `bupt tui`，也可用 `bupt tui api` 和 `bupt tui compute` 直达业务。运行一次 `bupt login`，先通过 WebVPN，再完成学校统一身份认证。控制器处理门户与 GPU 服务各自的回调。学校要求再次确认时，仍用这一条登录命令继续。

| 我想做什么 | 命令 | TUI 入口 |
| --- | --- | --- |
| 查看账号、认证状态和服务摘要 | `bupt status`、`bupt account` | 帮助与反馈 → 账户及相关设置 |
| 查所有模型的调用 ID、能力、上下文、输入输出价格和健康状态 | `bupt api model`、`bupt api model show ID` | 模型 API → 模型与价格 |
| 查余额、最近调用、Token 趋势和按模型用量 | `bupt api usage --days 30` | 首页 → 余额与用量 |
| 查询、新建、停用和导出密钥 | `bupt api key` | 模型 API → API 密钥。模型详情可直接创建限定模型的 Key |
| 获取模型接入地址、调用示例或榜单 | `bupt api guide`、`bupt api leaderboard` | 模型 API → 在我的程序中调用或排行榜 |
| 选算力资源、比较规格和额度 | `bupt compute catalog`、`bupt compute quota` | 裸算力服务器 → 资源与价格 |
| 看哪台机器占着额度、何时到期 | `bupt compute instance`、`bupt compute instance show ID` | 裸算力服务器 → 我的实例 |
| 查调度、托管运行、存储和项目 | `bupt compute scheduler job`、`bupt compute job`、`bupt compute storage`、`bupt project` | 裸算力服务器 → 资源与价格 → 更多算力工具 |

首页用 `R` 进入模型 API，`S` 进入裸算力服务器，`U` 进入余额与用量，`?` 打开帮助与反馈。首次使用可在首页设置个人工作区并登录。方向键或 j/k 选择，Enter 激活，Tab / Shift+Tab 切换控件。`/` 筛选当前列表。在选择器内，Enter 应用并返回，Esc 取消。在资源列表按 Esc 返回上一级，退格清除已应用的筛选。密钥和实例列表可按 `n` 新建，`r` 刷新，资源页按 `?` 查看快捷键。脚本沿用 `--json`、`--jsonl`、`--output id` 和 `--non-interactive`。

### TUI 目录树

```text
校园算力平台
├─ R 模型 API
│  ├─ 1 在线调试
│  ├─ 2 模型与价格
│  ├─ 3 在我的程序中调用
│  ├─ 4 API 密钥
│  └─ 5 排行榜
├─ S 裸算力服务器
│  ├─ 1 我的实例
│  ├─ 2 创建实例
│  └─ 3 资源与价格
├─ U 余额与用量
│  ├─ 1 总览
│  ├─ 2 模型 API
│  ├─ 3 裸算力
│  ├─ 4 趋势
│  └─ 5 明细
└─ ? 帮助与反馈
   ├─ 快捷键说明
   ├─ API 使用文档
   └─ 账户及相关设置
```

首次设置或需要登录时，首页会给出对应操作。离线时「创建实例」显示使用条件。作业、存储和项目可从「资源与价格 → 更多算力工具」进入，在裸算力页按 `?` 也能找到。完整教程和 CLI 命令参考在「帮助与反馈 → 使用帮助 → 快捷键说明」内。

### 顶栏与账号

顶栏默认显示 WebVPN、算力平台、剩余积分和本人运行中的实例数。在「账户及相关设置」可以勾选姓名或学工号、调整可选字段、恢复默认。运行实例数按学校报告的本人运行状态统计。横线表示尚无可确认的数值，真实零值显示为 0。网络中断或权限核验失败时，以对应的状态文字提示。

另一个终端完成 `bupt login` 或 `bupt logout` 后，已打开的界面会接收同一控制器的状态更新。开启自动核验的窗口共享低频学校检查。关闭自动核验时仍能接收本机操作产生的通知。`bupt tui --cached` 用于离线浏览。

### 接入与统计

在「模型 API → 在我的程序中调用」选择模型和示例格式。从模型详情或在线调试进入时会带入当前模型，从调试进入还会带入输入、参数和密钥。选择或创建调用密钥后，在「环境配置与依赖」复制配置说明。关闭该项即可复制程序正文。`c` 复制当前预览，`s` 保存到新文件。Python 使用标准库。Python 和 curl 读取环境变量 BUPT_API_KEY，bupt 命令使用 Key ID。运行示例时按用量计费。

首页「余额与用量」默认显示「总览」。按数字 1–5 就地切换总览、模型 API、裸算力、趋势和明细。五个视图共用统计周期，可选近 7 天、30 天或最近 1–30 天。在「趋势」和「明细」选择数据来源。模型明细可按日期或模型分组，裸算力明细可按账单类型查看，用 `/` 筛选日期、名称或实例编号。结算日期未知的记录单列。

命令行用 `bupt usage --days 7 --output json` 读取同一学校时区的查询窗口。裸算力遍历账单分页并按学校记录 ID 去重。通过 `complete`、`code` 和 `next_page` 核对结果，必要时用 `compute usage --page N` 单独复查。金额未知、日期缺失或分页中断时，完整计量总额会缺省。门户余额、门户实扣积分和算力账单的已公布计量分别显示。部分账户的门户日期无法和算力窗口精确对齐，界面会说明。

修改时限、创建方式或模型的数值参数后，TUI 将这些表单设置保存到本机状态目录。相同 profile 和模板或模型重新打开时恢复参数。超过 14 天的草稿失效，表单可清除本机草稿。实例名称、调用正文、密钥及授权只属于当前编辑会话或本次提交。

### 从第一台实例到 SSH

在「裸算力服务器 → 资源与价格」移动到一个规格即可查询默认日价，已查过的报价保留在当前列表。

在「裸算力服务器」选「创建实例」，选定模板后填写实例名称和运行上限。申请表显示当前配置的学校日价。按需调整资源后会重新查询。项目模板可从本人可用项目中选择。核对资源和到期处理后，用 Enter 确认提交。提交后进入本次申请的实例详情。分配到实例编号后，原页显示实际状态。控制器在分配后启动并验证 SSH。实例详情显示排队、启动和验证进度。实例详情同时列出本次托管截止和已知的平台预留到期。就绪后可连接 SSH 或上传代码。上传到本次作业节点后，界面返回实例并选中「提交后台作业」。例如运行 `python train.py`，程序填 `python`，第一个参数填 `train.py`。提交后可在回执打开「查看这次作业的输出」。

「我的配额」可按资源填写申请上限和理由。「远程算力」的订单先显示所选规格、周期和学校报价，再确认下单。可在「运行与记录」查看实验进度、作业输出和操作历史。

### 模型资源和凭据

```sh
bupt api model --json | jq -r '.models[].id'
bupt api key create research --model MODEL_ID
bupt api key show KEY_ID
bupt api key export KEY_ID --file ./research.key
bupt api key disable KEY_ID
```

创建时学校只返回一次完整值。控制器确认已保管新密钥时，原工作台会出现「查看刚创建的密钥」，可主动查看并复制，关闭后该入口消失。所选控制器按账号加密保管，默认输出只包含 ID、状态、模型范围和保管标记。导出到文件要求新路径，权限为 0600。显式 `--file -` 可接管道。由网页创建、控制器并未保管完整值的 Key 也可管理。永久删除使用 `bupt api key delete KEY_ID --confirm KEY_ID`。

模型和健康页按真实 ID 合并。学校未公布的上下文、价格和未监测的状态保持未知。门户页面显示的「周期内剩余」与「算力余额」目前读取同一个余额字段。用量趋势带有统计区间。校园服务连接限流时按服务器等待时间退避，门户查询在一分钟内复用同账号的快照；`--refresh` 刷新当前栏目，`--cached` 可离线查看已保存的数据。

### 模型调用

在「模型 API → 在线调试」选任务和模型。只有一个兼容模型时会直接进入工作台。选择输入字段后按 Enter 编辑，Enter 换行，Esc 保留草稿。「填入示例」在覆盖已有输入前会询问。「发送请求」按次提交，回复、可获得的用量和网关编号显示在原页。可展开、复制或导出结果，也可改写输入后再次发送。若请求结果未知，先在工作台核查近期用量，核查本身只读取数据。

语义相似度使用学校的向量接口获取两段文本的向量，在本机计算余弦相似度。代码工作台中的请求 JSON 展示实际发送给网关的内容。

| 能力 | 自己输入的内容 | 返回内容 |
| --- | --- | --- |
| 对话 | 自由提问，可调输出 Token 上限 | 模型回复、耗时及网关返回的 Token 用量 |
| 向量 | 一段或多段文本 | 每段的维度和向量预览，恰好两段时展示余弦相似度，可展开完整向量 |
| 重排 | 问题、候选段落和可选的返回条数 | 排序、原始候选编号、原文和相关度分数 |

```sh
bupt api model example bge-m3 --text '今天去图书馆' --format python
bupt api model invoke qwen3.8-27b --key KEY_ID --text '请用一句话解释向量检索。'
bupt api model invoke bge-m3 --key KEY_ID --text '怎样借书？' --text 'How can I borrow a book?'
bupt api model invoke jina-reranker-m0 --key KEY_ID --query '去哪借书？' --document '食堂供应早餐。' --document '图书馆提供借阅服务。'
```

`KEY_ID` 是 `bupt api key` 列表中的标识，完整 Key 由控制器取用。单次调用按当前输入生成独立请求。`example` 可查看同一输入对应的 curl、Python 或 JSON，`invoke --sample` 显式发送内置示例，`invoke --dry-run` 仅预览。缓存模式可以编辑和预览，实际调用需要在线。响应在当前界面显示，程序不保存提示词和回复历史。请求结果不确定时先核对用量，再决定是否重新发送。

### 项目与可选管理功能

`bupt compute features` 查看学校发布的模块开关。`bupt project show GROUP` 展开已验证项目的角色、可见实例，以及学校已向该项目管理员发布的成员。`bupt admin` 单独核验学校后台会话。经学校授权的管理员可查询账户与集群限额，限额调整以预览返回的版本提交。学校权限与控制器权限分别校验。

`compute usage` 提供个人实例账单，`compute usage history` 查询个人账户流水。启用远程算力且学校提供该接口时，`remote` 提供资源、实例和订单管理。创建或续期订单先用 `--dry-run` 查看单价、总额与周期，再用返回的 `--quote` 确认。真实付款由操作人在学校支持的付款端完成。内嵌的模型计量模块以 `compute llm` 查看独立账目。

平台动态用 `bupt news`，服务协议用 `bupt agreement`。排行榜展示方式由本人通过 `bupt api leaderboard display` 管理。建设中的网页账单流水没有可供查询的真实记录。

## 日常使用

```sh
bupt login
bupt compute catalog
bupt compute instance create lab --spec SPEC_ID
bupt compute instance wait lab
bupt ssh lab
bupt cp ./code.py lab:code.py
bupt compute job run --instance lab -- python3 code.py
```

在终端创建时，按提示填写运行时长并核对学校日价。申请提交后会显示操作 ID。等待命令验证实例 SSH 连接，就绪后可进入终端。接管已有实例使用 `bupt compute instance adopt INSTANCE_ID --max-runtime 2h`。在现有 SSH 路由上执行一条命令使用 `bupt exec lab -- COMMAND`。

保存常用资源配置后，新建实例不必反复填写模板 ID：

```sh
bupt compute catalog ls
bupt compute preset set small --spec '模板名称@版本' --max-runtime 2h
# 也可以复制已有实例的资源选择：
bupt compute preset set small --from INSTANCE_ID --max-runtime 2h
bupt compute preset use small
bupt compute instance start another-name
bupt compute preset ls
```

`--spec` 接受 ID 或唯一的精确名称/版本；名称有歧义时拒绝猜测。显式 `--spec` 不继承另一模板的默认预设；`--preset ''` 临时禁用默认预设。`preset set/use/rm --project` 写入项目 `.bupt.toml`，全局与账号配置保留独立。预设仅影响后续请求，不改写正在运行的策略。

`up` 先返回可持久保存的 Run ID 再等待就绪，等待进度写入 stderr。关闭终端或等待超时后可继续：

```sh
bupt compute lease wait RUN_ID                    # 继续等待准备完成，不重复分配
bupt compute lease wait RUN_ID --for finished     # 等待清理结束
bupt status RUN_ID
bupt compute lease status RUN_ID --json
bupt compute lease end RUN_ID --cleanup release
```

需要补实例终端密码时执行 `bupt auth access`；学校登录失效时执行 `bupt login`，再 `bupt compute lease wait RUN_ID`。`status --cached` 完全使用本地记录；普通 `status` 检查当前会话但不会主动登录或接管网页登录。SSH 列是已保存路由状态，实际连通性用 `bupt exec gpu -- true` 验证。

需要 OpenSSH/VS Code 兼容入口时，在实例就绪后执行 `bupt ssh-config install gpu --alias bupt-gpu`，以后可用 `ssh bupt-gpu`。安装器保存备份，并将工具管理的 Include 放到现有 SSH 配置前。

控制器重启会断开由它转发的 SSH 窗口；实例上的 tmux 和后台作业仍可继续。交互工作用 `ssh --session work --reconnect` 恢复会话，普通 SSH shell 不承诺跨断线保持。

脚本使用 `--json`、`--jsonl` 或 `--output id`，以及 `--non-interactive`。`compute instance create --output id` 只输出操作 ID；诊断和错误写 stderr，SSH/文件/作业流不混入表格。

## 第一次使用：本地控制器

```sh
bupt init --dry-run
bupt init
bupt agent status
bupt login
bupt auth status
bupt compute catalog ls
bupt compute quota
```

`init` 默认安装并启动当前用户的服务：macOS 使用 LaunchAgent，Linux 使用 `systemd --user`。不需要服务器，也不修改 Clash、系统路由或防火墙。Linux 会话必须能访问用户级 systemd；也可以使用前台模式。

```sh
bupt init --foreground
# 或者，只初始化文件，再显式运行：
bupt init --install=false --start=false
bupt agent run
```

前台控制器会占用该终端；其他终端继续使用 `bupt`。`--install=false` 且未指定 `--start=false` 会启动独立后台进程。`bupt agent stop` 停止控制器并保留配置、凭据、状态及实例上的隧道设置。

实例在校内能直连时，可以使用直连 SSH。离开校内网络后，需要一个仍然可达的直连地址，或配置下面的 SSH 中继。当前没有实现自动 P2P 打洞；本地控制器也不会让原本不可达的校内 TCP 地址自动变得可达。

## 本地控制器 + 可选的 SSH 中继

中继是自己的、可被实例主动连接的 Linux SSH 服务器。控制器仍可留在笔记本，学校会话和主要运行状态也留在本机。以下命令会实际配置所选远程服务器，先用 `--dry-run` 检查同一组参数。

```sh
bupt init --mode local --relay-install \
  --relay-address relay.example.edu:22 \
  --relay-user root \
  --relay-identity-file ~/.ssh/relay-admin \
  --relay-host-key "$(cat ./verified-relay-host.pub)" \
  --instance-address relay.example.edu:22 \
  --agent-binary ./dist/bupt-linux-amd64
```

`verified-relay-host.pub` 是事先核验的服务器 SSH 主机公钥；主机指纹检查不会自动跳过。远程安装需要 root，或具备免交互 `sudo` 权限的管理账户，以及 Linux/systemd。选择与服务器架构匹配的 `--agent-binary`。

安装器创建专用 `bupt` 系统用户、受限 SSH 客户端身份、systemd 服务和私有状态目录。实例反向端口仅绑定中继的 `127.0.0.1`；不会为了 SSH 把实例端口公开监听到公网，也不会改写系统 `sshd_config` 或防火墙。已有中继用户、路径或不兼容的服务配置会被明确拒绝或要求先检查，不自动接管。

如果中继已经准备好，可以仅保存经过核验的中继配置，不加 `--relay-install`。此时 `--relay-user` / `--relay-identity-file` 应当是已经注册的受限客户端身份，而不是安装用管理员身份。

笔记本休眠时，实例上的作业和实例到中继的隧道保活可以继续；笔记本上的 SSH 窗口会断线，本地控制器也无法按时向学校提交续期或释放请求。恢复运行后才继续核对和处理。后台作业执行器会保留已下发的期限和进程状态；它不能代替学校的资源释放 API。需要持续管理时可把控制器放到服务器。

## 可选的远程控制器

```sh
bupt init --mode remote \
  --relay-address relay.example.edu:22 \
  --relay-user root \
  --relay-identity-file ~/.ssh/relay-admin \
  --relay-host-key "$(cat ./verified-relay-host.pub)" \
  --instance-address relay.example.edu:22 \
  --agent-binary ./dist/bupt-linux-amd64
bupt agent status
bupt login --device --wait
```

此模式由服务器保存学校凭据和运行状态，笔记本通过受限 SSH RPC 连接控制器。服务器需要能访问学校 WebVPN；扫码批准的是服务器发起的学校会话。切换登录来源仍可能触发学校重新认证。

本地与远程控制器各自拥有状态库。初始化远程模式不会自动迁移另一控制器的活跃作业、已保存凭据和路由。切换前先处理当前活跃运行，并保留原控制器状态；不要同时让两个控制器管理同一实例的生命周期。

## 登录与凭据

| 统一身份认证方式 | 命令 | 校外 WebVPN 过期时 |
| --- | --- | --- |
| 手机扫码 | `bupt login` | 先扫 WebVPN 码，再扫统一认证码 |
| 学工号和密码 | `bupt login --sso-password` | 先扫 WebVPN 码，再在终端输入统一认证账号和密码 |

学校会话仍有效时会跳过对应步骤。两种方式都以门户确认校园身份为登录成功。裸算力服务在首次使用时核对自己的会话。`bupt tui access` 的「学校登录」也可选择这两种方式。

Pi、Codex 和 Claude Code 使用 `bupt login --agent` 获取当前步骤，再用相同 profile 和控制器运行 `bupt auth poll --agent`。每次调用只取一步，默认 stdout 是单个 JSON。成功退出表示已取到步骤。认证完成看 `stage=complete` 和 `state=authenticated`。等待期间遵循 `poll_after_ms`，按 `required_action` 提示扫码或手机确认；无图片时继续轮询。有 `qr_file` 时展示实际图片或可打开的本机链接，并记住路径避免重复提示。路径属于运行 CLI 的机器。

`--agent --output table` 即使通过管道也能打印等宽字符码，可展开工具输出扫码。显式加 `--open` 会用本机查看器打开图片。查看器不可用时查看返回的路径。自动 PNG 保存在状态目录的 `login-qr/` 中，权限为 0600，有效期后的下一次登录或轮询会清理。自定义 `--qr-file` 使用下文的文件命名规则，由调用者保管。过期后用 `bupt login --agent` 获取新码；要求接管其他登录时先征得用户确认。

统一认证密码在终端隐藏输入。控制器只在本次向学校 CAS 提交时使用它，之后保存登录会话。可用 `--username` 预填学工号。出现图形验证码时按提示填写，遇到其他额外验证可改用 `bupt login` 扫码。脚本可以让密码管理器通过标准输入向 CLI 提供密码。

```sh
bupt login --sso-password --username YOUR_CAMPUS_ID
secret-provider read campus-password | bupt login --sso-password --username YOUR_CAMPUS_ID --password-stdin
bupt login --device --wait --non-interactive --qr-file ./bupt-login.png
bupt login --device --no-wait --qr-file ./bupt-pending.png --json
bupt auth poll --wait --json
bupt auth info --json
```

没有交互终端时，显式使用 `--device --wait` 等待人工扫码。`--no-wait` 返回当前 challenge，`auth poll` 查看并推进完整登录流程，`auth poll --wait` 继续等待。二维码过期或控制器要求刷新时会返回明确错误，需要重新运行 `login`；不会反复提交密码。

`--qr-file ./bupt-login.png` 生成 `bupt-login.webvpn.png`、`bupt-login.sso.png` 等 0600 文件。同阶段出现新 URL 时添加编号。已有文件不会被覆盖，重复启动请换一个文件名前缀。`auth device` / `login --gateway-only` 明确只完成 WebVPN，不能代替后面的统一认证。

只有学校明确返回 `AUTH_CONFIRM_REQUIRED` 时，才由你决定是否运行 `bupt auth confirm --take-over` 替换另一个 WebVPN 登录；工具不会自动确认。

实例终端首次安装 SSH 公钥时可能需要实例密码。它与扫码登录是两个用途：

```sh
bupt auth access
bupt compute instance prepare-ssh EXISTING_RUNNING_INSTANCE
bupt ssh EXISTING_RUNNING_INSTANCE
```

`auth access` 隐藏输入并加密保存**实例终端密码**，只用于 SSH 公钥初始化，不设置服务端密码，也不执行登录尝试。实例账户名来自控制器核验的节点/账户信息。`compute instance prepare-ssh` 只准备或修复已运行实例的 SSH，不创建实例、不申请资源；多节点加 `--node NODE_ID`。需要自动化输入时使用 `auth access --password-stdin`。

算力平台本地口令使用 `--local`。也可通过凭据 stdin 选择该接口。脚本传入统一认证凭据时须同时指定 `--sso-password`。`auth refresh` 可核验和刷新已有平台本地凭据；统一认证过期请重新执行默认 `login`。

```sh
bupt login --local --username LOCAL_PLATFORM_ACCOUNT

secret-provider read platform-password | \
  bupt login --username LOCAL_PLATFORM_ACCOUNT --password-stdin --non-interactive

secret-provider read platform-credentials-json | \
  bupt login --credentials-stdin --non-interactive
```

`secret-provider` 代表你使用的凭据管理器，不是本工具内置命令。JSON 输入可包含 `username`、`password`，以及本地接口确实要求时的 `token`、`captcha_uuid`、`captcha_code`。口令参数不接受明文 argv。不要把真实凭据放进命令历史、项目 TOML、仓库文件或 CI 日志。本地口令登录遇到图形验证码时保留本次已输入的密码，最多请求三张验证码，由你输入答案；Ghostty/kitty 可直接显示 PNG，其他环境保存私有图片供查看。

```sh
bupt auth captcha --file ./captcha.png --json
bupt auth setup
bupt logout
```

`auth setup` 是显式的设置/修改平台本地密码操作，由用户单独发起。`bupt logout` 清除所选配置的学校会话、已保存的平台本地口令和实例终端密码。若存在活跃托管运行，控制器会返回 `ACTIVE_RUNS`。

## 实例、费用与操作回执

```sh
bupt compute catalog show SPEC_ID --json
bupt compute catalog estimate --spec SPEC_ID
bupt compute instance create experiment --spec SPEC_ID --max-runtime 2h --dry-run --output json
bupt compute instance create experiment --spec SPEC_ID --max-runtime 2h --max-points-per-day RATE --output json
bupt compute instance wait experiment
bupt ssh experiment
```

将 `RATE` 换成核对后接受的日价上限（积分/天）。交互创建会展示规格、学校估价、计费提示与到期处理并要求确认。未配置运行上限时会询问时长。脚本通过参数、配置或预设提供运行上限，并显式授权估算日价。控制器提交前重新估价。学校未公布确切计费起点时，按预留建立或保留存储时就可能计费理解，结算以学校账单为准。`--allocate-only` 会申请实例及其所需的预留，到分配完成为止。准备使用时执行 `compute instance start INSTANCE`，这会沿用该分配的托管截止时间。批准和排队状态会出现在详情和 TUI 进度中。

| 命令 | 影响 |
| --- | --- |
| `compute instance start INSTANCE` | 启动已有停机实例并准备 SSH。如果算力已归还，先用 `restore` 重新分配。 |
| `compute instance restart INSTANCE` | 温和停机后启动同一实例，沿用当前托管截止。非托管实例需提供有限运行上限。先处理正在运行的托管作业。 |
| `compute instance stop INSTANCE` | 中断运行，保留实例磁盘及计算预留。预留和存储仍可能计费。 |
| `compute instance release INSTANCE` | 归还停机实例预留的 GPU、CPU 和内存，按学校回收策略保留实例磁盘，存储仍可能计费。 |
| `compute instance restore INSTANCE` | 为已有实例重新分配算力，保留原磁盘。确认学校日价后提交，分配完成后再启动。脚本以 `--max-points-per-day RATE` 授权日价。 |
| `compute instance delete INSTANCE --confirm INSTANCE_ID` | 永久删除已停机并释放的实例及实例磁盘。独立存储空间由 `compute storage` 管理。 |
| `compute instance cancel-queue INSTANCE` | 撤回排队的启动或预留申请。 |
| `compute lease end INSTANCE --cleanup release` | 主动结束托管周期，温和停机并按选定策略清理。 |

生命周期命令默认返回操作 ID 和当前状态，`--wait` 等待本次操作完成。用 `bupt compute instance wait --operation OPERATION_ID` 续等回执。用 `bupt compute instance wait INSTANCE` 等待通过实际 SSH 认证验证，必要时用 `--node NODE` 指定登录节点。查询平台运行状态用 `--state running`。中断等待时，控制器保留已经提交的操作。同一请求用相同的 `--idempotency-key` 重试。

TUI 的实例详情如果显示「算力未分配」，先选「重新分配算力」，核对价格和时限。分配完成后选「启动并准备 SSH」，就绪后选「连接 SSH」。

不同键重复停机或释放时，如果学校仍在处理同一请求，控制器返回当前回执。如果目标状态已达到，返回无副作用的确认回执。重叠的启停、重启、释放与恢复会返回正在处理的操作 ID。遇到 `SUBMISSION_UNKNOWN`，使用返回的请求键及原参数重试。脚本需保留原日价授权。

`bupt compute instance events INSTANCE --output json` 分别列出学校当前可见的任务回执与本地控制器审计。学校快照包含的历史有限，`--cached` 可离线查看上次成功观察。

## 有上限的运行与续期

新的托管实例、启动周期和作业需要有效的 `--max-runtime` 或 `--until`，也可从 preset / 配置继承。准备、排队、启动时间计入该周期；重复连接或更新配置不会从零计时。

```sh
bupt compute lease status experiment --json
bupt compute lease set experiment --max-runtime 6h
bupt compute lease set experiment --no-renew
bupt compute lease set experiment --cleanup stop
bupt compute lease renew experiment --duration 30m --reason "finish current experiment"
```

`lease set --max-runtime 6h` 把总上限改为从本周期开始计算的 6 小时，不是在当前时间上再加 6 小时。`--until` 接受带时区的 RFC3339 时间。上限已落在过去时，需要显式 `--stop-now` 才允许触发立即清理。两个上限选项不能同时设置。

新建托管运行默认关闭自动续期。可以在可信配置或启动选项中显式启用。续期由学校审批，控制器分别记录待批申请、已获批的到期时间和本次运行的总上限。

默认清理宽限为 60 秒。自动强制停机默认关闭，可以为当前操作或周期明确授予 `--force-stop`。需要立即强制停机时，显式执行 `compute instance stop INSTANCE --force`。认证过期、平台拒绝申请、网络不可达或拒绝强制停止时，工具会保留 `auth_required` / `cleanup_pending` 等状态，而不是声称资源已释放。运行上限是调度约束，不是学校平台在指定秒钟必定完成释放的承诺。

## 后台作业、长连接与文件

```sh
bupt compute job run --preset gpu-small --max-runtime 4h --dry-run -- python3 train.py
bupt compute job run --preset gpu-small --max-runtime 4h --max-points-per-day RATE -- python3 train.py
bupt compute job run --instance experiment --max-runtime 1h --detach -- make test
bupt compute job ls
bupt compute job show JOB_ID --json
bupt compute job logs JOB_ID --follow
bupt compute job attach JOB_ID
bupt compute job wait JOB_ID
bupt compute job update JOB_ID --max-runtime 5h
bupt compute job cancel JOB_ID
```

前台 `run` 返回作业退出码。文本模式跟随日志，JSON/JSONL 模式只输出作业结果或结构化诊断，实时日志用 `bupt compute job logs JOB_ID --follow` 查看。提交后等待超时可读取错误的 `run_id` 并用 `bupt compute job wait RUN_ID --json` 续等。提交响应丢失时用返回的 `idempotency_key` 和原参数重试。终端中断会请求取消并执行既定清理。`--detach` 只返回作业身份，控制器继续管理。`job wait` / `job logs -f` 被中断只停止等待或查看，不默认取消作业。`job attach` 是跟随持久输出直到结束，后台作业的 stdin 是 `/dev/null`，不是交互式 PTY。

真正需要交互式长会话时，使用实例已有的 tmux：

```sh
bupt ssh experiment --session work --reconnect
bupt exec experiment -- printf '%s\n' 'argument with spaces'
bupt forward experiment 8888 --local 127.0.0.1:18888
bupt ssh-config export experiment
bupt ssh-config install experiment
```

`ssh` 使用原生 Go SSH 传输和 `xterm-256color`，不执行 Ghostty 的 SSH shell 包装函数。`--reconnect` 重新连接同一 tmux 会话，不重跑命令；镜像需要已安装 tmux。SSH 主机指纹变化会失败，不能自动信任。多节点实例请明确加 `--node NODE_ID`。`ssh-config install` 是可选的 OpenSSH 兼容 include，写入前备份已有内容。

```sh
bupt cp ./train.py experiment:work/train.py
bupt cp --recursive ./src experiment:work/src
bupt cp experiment:work/result.bin ./result.bin
bupt cp - experiment:work/input.bin --size BYTE_COUNT < ./input.bin
bupt sftp experiment

bupt compute storage stores
bupt compute storage ls --store STORE
bupt compute storage push ./dataset.tar datasets/dataset.tar --store STORE
bupt compute storage pull results/model.bin ./model.bin --store STORE
bupt compute storage pull results/model.bin - --store STORE > model.bin
```

`cp` / `sftp` 走已缓存的 SSH 路由；`data` 走学校持久存储 API，需要学校会话。平台上传支持按上传记录和文件 SHA-256 续传，也可用 `--upload-id` 指定已有记录；stdin 上传必须给出 `--size`，不自动续传。下载先写同目录临时文件，成功后才替换目标；覆盖已有本地文件需要 `--overwrite`。目录复制不跟随符号链接。

模板支持时还可执行 `resize`、`snapshot`、`clone`、`service`。例如 `resize experiment --size-gb 100` 表示 QEMU 磁盘扩到总共 100 GB，不是增加 100 GB。资源改配通常要求停机并释放，快照要求支持相应节点类型；不支持的操作会返回明确错误。

## 配置与 CI

配置优先级从低到高是内置默认、全局配置、所选 profile 的默认、项目 `.bupt.toml`、环境变量、命令行。preset 的继承和显式布尔值均保留来源；运行时策略变更需要 `lease set` / `job update`，修改配置不会悄悄改动既有作业。

```sh
bupt config explain --json
bupt config get policy.cleanup
bupt config set policy.max_runtime 2h
bupt profile ls
bupt profile use bupt
```

项目文件可以提交 profile 选择、资源 preset 和有限运行策略，不可改写可信服务器地址、身份文件或 Secret：

```toml
# .bupt.toml
version = 1
profile = "bupt"

[policy]
max_runtime = "2h"
auto_renew = true
cleanup = "release"
stop_grace = "60s"
force_stop = false

[presets.gpu-small]
spec_id = "SPEC_ID"
```

机器输出使用 `--json`（单个值）、`--jsonl`（逐条记录）或 `--output id`（逐行 ID），各格式选项互斥。诊断和提示在 stderr。`ssh`、`exec`、`proxy`、文件下载到 `-` 等原始字节命令不接受格式化选项。远端命令退出码会保留；本地一般错误为 1，用法错误为 2，中断为 130。

为 CI 使用独立 SSH 公钥，由控制器所有者注册到指定 profile，不授予所有者权限：

```sh
bupt client register ci-train --public-key-file ./bupt-ci.pub --allow-profile bupt --json
bupt client register ci-monitor --public-key-file ./bupt-monitor.pub --allow-profile bupt --read-only
bupt client ls
bupt client revoke CLIENT_ID
```

`client` 管理控制器 RPC 身份，`client key` 管理实例登录公钥，`api key` 管理模型调用密钥。当前授权粒度是 profile 范围及只读或管理访问权限。只读身份只能查询，普通 CI 身份需由所有者注册并维护权限。

在 CI 的 Secret 文件挂载和环境变量中提供控制器专用私钥路径及已核验的主机公钥：

```sh
export BUPT_CONTROLLER='ssh://bupt@relay.example.edu:22'
export BUPT_CONTROLLER_IDENTITY_FILE="$CI_KEY_FILE"
export BUPT_CONTROLLER_HOST_KEY="$VERIFIED_RELAY_HOST_KEY"

bupt --non-interactive --idempotency-key "$CI_PIPELINE_ID:train" \
  compute job run --name ci-train --preset gpu-small --max-runtime 2h \
  --max-points-per-day "$AUTHORIZED_POINTS_PER_DAY" --cleanup release \
  -- python3 /workspace/train.py
```

CI 不存学校密码，也不自动绕过刷脸。学校会话失效时由所有者重新登录；已经运行的实例 SSH 通路仍独立存在。不要在 CI 启用输出 Secret 的 shell trace。

## 状态、维护与排错

| 内容 | 本地默认路径 |
| --- | --- |
| 非 Secret 配置 | `~/.bupt/config.toml` |
| 状态库、RPC socket | `~/.bupt/state/state.db`、`agent.sock` |
| 加密凭据、主密钥、专用身份 | `~/.bupt/data/` |
| 主密钥 | `~/.bupt/data/master.key` |
| 本机界面偏好 | `~/.bupt/tui.toml` |
| 本机草稿参数 | `~/.bupt/state/tui-drafts/`，文件权限 0600 |
| 默认 profile 的可读账号状态 | `~/.bupt/state/bupt/status.json` |

显式设置的 `XDG_CONFIG_HOME`、`XDG_STATE_HOME`、`XDG_DATA_HOME` 分别覆盖对应目录。远程安装使用 `/etc/bupt/` 和 `/var/lib/bupt/`，系统服务为 `bupt-agent.service`。Unix socket 与持久化目录仅供所属账户访问；学校密码、Cookie 和 Token 保存在加密 vault 中，SSH 私钥是单独的受保护文件。

备份应包含配置、完整状态目录、数据目录和匹配的主密钥；丢失主密钥无法解密已有凭据。复制 bbolt 状态库前先停止该控制器，避免不一致副本。不要只备份密文却遗失主密钥，也不要提交真实状态文件到仓库。升级时替换二进制、保留这些目录并重启对应服务；安装器替换重要配置时记录备份位置。

```sh
bupt doctor --offline
bupt agent status
bupt auth status --json
bupt compute lease status experiment --json
bupt compute task ls --json
bupt compute audit ls --limit 50 --jsonl
```

若 `ssh` 提示尚无路由，已有运行实例可在学校认证有效时执行 `compute instance prepare-ssh INSTANCE`，新实例使用 `compute instance create`。不要把 SSH 端口连接失败等同于密码过期。若平台操作返回未知结果，查看 task/run 回执再决定重试。`console` 使用学校终端通道，和独立 SSH 的认证要求不同。

实现与验证范围见 [协议说明](protocol.md)。仓库的协议模拟、状态恢复和命令行测试不等价于所有真实模板的线上创建、续期、释放和删除验证；线上验收应记录具体实例、模板及操作结果。

CLI 与 TUI 共用的分层与类型化接口见 [架构说明](architecture.md)。CLI、SDK 和控制器共享配置默认值，自动续期默认关闭，需要 `--renew` 或显式配置；任何托管运行仍需有限时长。

更换网络出口后，WebVPN 可能要求重新认证。运行 `bupt login` 可在当前网络恢复学校会话。`bupt auth probe --json` 可使用不写回主会话的副本检查当前状态；探测本身仍可能算作服务端活动，不能拿连续探测结果冒充空闲寿命测试。


## 脚本契约与命令迁移

`bupt help --all --json` 返回完整命令路径列表 `commands`。`bupt help COMMAND --json` 返回命令的用法、直接子命令和本地及继承的选项。未知子命令以用法错误退出。

`--output table|json|jsonl|id` 选择结果格式。保留 `--json` 和 `--jsonl`。支持 ID 的列表用 `--output id` 逐行输出，同一行一个 ID。模型账户余额用 `bupt api usage --field balance`。余额缺失时返回明确错误，学校报告的零值输出 `0`。文件目的地用 `--file`，并在支持的文件操作中继续使用 `-` 表示标准流。

加载 `bupt completion bash`、`bupt completion zsh` 或 `bupt completion fish` 生成的脚本后，实例参数和规格参数可以补全。候选来自当前 profile 的控制器缓存。重名时优先用 ID，控制器不可用时结束查询。

模型调用超时后可用 `bupt api usage` 核对。控制器会保留原请求编号并拒绝以新键自动重发同一请求。明确核对并再次授权后，使用 `api model invoke ... --resend-after REQUEST_ID` 发起新的调用。TUI 在核查用量页提供同样的确认。

诊断写 stderr。JSON 和 JSONL 模式的失败输出一行 `{"error":{"code":"...","message":"..."}}`。普通诊断以 `BUPT[CODE]` 开头。用法错误退出 2，本地操作失败和 RPC 超时退出 1，前台等待中断退出 130，尚无程序退出状态的作业到期退出 124。`exec` 透传远端的 0–255 和原始 stderr，远端程序若主动写入相同诊断前缀，两类诊断在这个流中不可区分。默认的 `bupt version` 与 `bupt --version` 输出相同，结构化版本使用 `bupt version --output json`。

| 原写法 | 现行写法 |
| --- | --- |
| `up NAME --spec SPEC` | `compute instance create NAME --spec SPEC` |
| `up NAME --adopt ID` | `compute instance adopt ID`，停机实例再运行 `compute instance start ID` |
| `down INSTANCE` | 只停机用 `compute instance stop INSTANCE`，明确清理策略用 `compute lease end INSTANCE --cleanup release` |
| `instance`、`ls`、`inspect` | `compute instance ls`、`compute instance show` |
| `spec`、`resources`、`estimate` | `compute catalog ls/show/estimate` |
| `quota`、`compute admin quota` | `compute quota`、`admin quota show/set` |
| `run`、`job`、`compute run` | `compute job` |
| 旧版学校调度 `compute job` | `compute scheduler job/request/template` |
| `data`、`compute ssh/exec/cp/forward` | `compute storage`、根级 `ssh/exec/cp/forward` |
| `api model try`、`-q` | `api model invoke`、`--output id` 或 `--field balance` |
| 顶层 `key`、`ssh ensure` | `client key`、`compute instance prepare-ssh` |
| `compute admin quota set --field NAME` | `admin quota set --quota-field NAME` |

若 `compute job run` 会新建实例，或使用 `compute instance clone`，先用 `--dry-run` 核对学校日价，再指定 `--max-points-per-day RATE`。仅在已有实例上提交作业可用 `--instance`。

升级时使用同一构建中的 CLI 与控制器，保留配置、状态、历史回执与凭据。升级前核对已下发到远端执行器的旧作业清理策略。

## 联系作者与反馈

TUI 首页选「帮助与反馈」，或按 `?` 在本页帮助中进入。普通反馈先选问题类型，再预览和复制邮件。在 SSH、模型调用和其他出错页面可选「反馈此问题」，原页面会保留。

```sh
bupt feedback
bupt feedback --template > feedback.txt
bupt doctor --report
bupt doctor --report --offline
```

`bupt feedback --template --diagnostics` 可附加脱敏摘要。诊断只检查程序、配置和控制器连接，报错时仅提取经过校验的错误码和回执。资源名称、地址、日志等由用户审阅后自行补充脱敏片段。学校账号、权限和结算由学校管理部门处理。请将审阅过的正文粘贴到自己的邮件客户端发送。
