# bupt 使用手册

第一次安装请打开随包的「使用说明.txt」。中继示例中的 `./dist/bupt-linux-amd64` 是从源码构建后的路径。使用师生安装包时，选择解压目录下 `bin/` 中与服务器架构对应的程序。

## 从总览开始

`bupt` 打印命令索引并返回 shell。全屏界面使用 `bupt tui`，也可用 `bupt tui api` 和 `bupt tui compute` 直达业务。运行一次 `bupt login`，先通过 WebVPN，再完成学校统一身份认证。控制器处理门户与 GPU 服务各自的回调。学校要求再次确认时，仍用这一条登录命令继续。

| 我想做什么 | 命令 | TUI 入口 |
| --- | --- | --- |
| 查看账号、认证状态和服务摘要 | `bupt status`、`bupt account` | 平台总览 → 账号与平台 |
| 查所有模型的调用 ID、能力、上下文、输入输出价格和健康状态 | `bupt api model`、`bupt api model show ID` | 大模型 API 服务 → 模型 |
| 查余额、最近调用、Token 趋势和按模型用量 | `bupt api usage --days 30` | 大模型 API 服务 → 余额与用量 |
| 查询、新建、停用和导出密钥 | `bupt api key` | 大模型 API 服务 → API Key；模型详情可直接创建限定模型的 Key |
| 获取 SDK 地址、调用示例或榜单 | `bupt api guide`、`bupt api leaderboard` | 大模型 API 服务 → 接入指南或排行榜 |
| 选算力资源、比较规格和额度 | `bupt compute catalog`、`bupt compute quota` | 裸算力服务 → 资源目录与配额 |
| 看哪台机器占着额度、何时到期 | `bupt compute instance`、`bupt compute instance show ID` | 裸算力服务 → 我的实例 → 选中实例 |
| 查调度、托管运行、存储和项目 | `bupt compute job`、`bupt compute run`、`bupt compute storage`、`bupt compute project` | 裸算力服务 → 作业与记录、存储与文件或项目 |

方向键选择，Enter 打开，Esc 返回，`/` 筛选，`r` 刷新当前页，`q` 退出。底部给出选中动作的等效 CLI。每个业务的「全部管理操作」直接使用 CLI 的命令、参数和校验规则，可在表单中逐项填写。脚本使用 `--json`、`--jsonl`、`-q` 和 `--non-interactive`。已有 `up`、`ssh`、`down` 等短命令仍可使用。

### 模型资源和凭据

```sh
bupt api model --json | jq -r '.models[].id'
bupt api key create research --model MODEL_ID
bupt api key show KEY_ID
bupt api key export KEY_ID --output ./research.key
bupt api key disable KEY_ID
```

创建时学校只返回一次完整值。所选控制器按账号加密保管，默认输出只包含 ID、状态、模型范围和保管标记。导出到文件要求新路径，权限为 0600。显式 `--output -` 可接管道。由网页创建、控制器并未保管完整值的 Key 也可管理。永久删除使用 `bupt api key delete KEY_ID --confirm KEY_ID`。

模型和健康页按真实 ID 合并。学校未公布的上下文、价格和未监测的状态保持未知。门户页面显示的「周期内剩余」与「算力余额」目前读取同一个余额字段。用量趋势带有统计区间。校园服务连接限流时按服务器等待时间退避，门户查询在一分钟内复用同账号的快照；`--refresh` 刷新当前栏目，`--cached` 可离线查看已保存的数据。

### 模型试用

在大模型 API 首页选择「试用模型」和模型 ID，或从单个模型详情进入。选中输入项按 Enter 打开文本编辑器，支持中文、多行粘贴和方向键移动。Ctrl+S 保存，Ctrl+D 用当前 Key 发送一次。也可以回到表单选择「发送一次」。新建 Key 时可从这里返回同一份草稿。

| 能力 | 自己输入的内容 | 返回内容 |
| --- | --- | --- |
| 对话 | 自由提问，可调输出 Token 上限 | 模型回复、耗时及网关返回的 Token 用量 |
| 向量 | 一段或多段文本 | 每段的维度和向量预览，恰好两段时展示余弦相似度，可展开完整向量 |
| 重排 | 问题、候选段落和可选的返回条数 | 排序、原始候选编号、原文和相关度分数 |

```sh
bupt api model example bge-m3 --text '今天去图书馆' --format python
bupt api model try qwen3.8-27b --key KEY_ID --text '请用一句话解释向量检索。'
bupt api model try bge-m3 --key KEY_ID --text '怎样借书？' --text 'How can I borrow a book?'
bupt api model try jina-reranker-m0 --key KEY_ID --query '去哪借书？' --document '食堂供应早餐。' --document '图书馆提供借阅服务。'
```

`KEY_ID` 是 `bupt api key` 列表中的标识，完整 Key 由控制器取用。单次试用按当前输入生成独立请求。`example` 可查看同一输入对应的 curl、Python 或 JSON，`try --sample` 显式发送内置示例，`try --dry-run` 仅预览。缓存模式可以编辑和预览，实际调用需要在线。响应在当前界面显示，程序不保存提示词和回复历史。请求结果不确定时先核对用量，再决定是否重新发送。

### 项目与可选管理功能

`bupt compute features` 查看学校发布的模块开关。`bupt compute project show GROUP` 展开已验证项目的角色、可见实例，以及学校已向该项目管理员发布的成员。`bupt compute admin` 单独核验学校后台会话。经学校授权的管理员可查询账户与集群限额，限额调整以预览返回的版本提交。学校权限与控制器权限分别校验。

`compute usage` 提供个人实例账单，`compute usage history` 查询个人账户流水。启用远程算力且学校提供该接口时，`compute remote` 提供资源、实例和订单管理。创建或续期订单先用 `--dry-run` 查看单价、总额与周期，再用返回的 `--quote` 确认。真实付款由操作人在学校支持的付款端完成。内嵌的模型计量模块以 `compute llm` 查看独立账目。

平台动态用 `bupt news`，服务协议用 `bupt agreement`。排行榜展示方式由本人通过 `bupt api leaderboard display` 管理。建设中的网页账单流水没有可供查询的真实记录。

## 日常使用

```sh
bupt login                         # 只在学校认证失效时扫码
bupt status                        # 登录、实例、SSH 缓存与托管任务概览
bupt up gpu --adopt INSTANCE_ID --max-runtime 2h
bupt ssh gpu                       # 使用已准备好的 SSH，无需每次扫码
bupt ssh gpu --session work --reconnect
bupt cp ./code.py gpu:code.py
bupt run --instance gpu --max-runtime 30m -- python3 code.py
bupt down gpu                      # 停止并释放资源，保留实例磁盘
```

首次接管网页创建的实例用 `up --adopt`；之后可用逻辑名 `gpu`。`run --instance` 默认继承已有周期的期限和清理策略；显式运行策略参数或 `lease set` 可更改策略，但不会把已消耗的时间清零。`run` 完成后会按该周期的策略清理资源。希望在已有 SSH 会话上执行命令并保持实例运行，使用 `bupt exec gpu -- COMMAND`。

保存常用资源配置后，新建实例不必反复填写模板 ID：

```sh
bupt spec ls
bupt preset set small --spec '模板名称@版本' --max-runtime 2h
# 也可以复制已有实例的资源选择：
bupt preset set small --from INSTANCE_ID --max-runtime 2h
bupt preset use small
bupt up another-name
bupt preset ls
```

`--spec` 接受 ID 或唯一的精确名称/版本；名称有歧义时拒绝猜测。显式 `--spec` 不继承另一模板的默认预设；`--preset ''` 临时禁用默认预设。`preset set/use/rm --project` 写入项目 `.bupt.toml`，全局与账号配置保留独立。预设仅影响后续请求，不改写正在运行的策略。

`up` 先返回可持久保存的 Run ID 再等待就绪，等待进度写入 stderr。关闭终端或等待超时后可继续：

```sh
bupt wait RUN_ID                    # 继续等待准备完成，不重复分配
bupt wait RUN_ID --for finished     # 等待清理结束
bupt status RUN_ID
bupt job inspect RUN_ID --json
bupt down RUN_ID --cleanup release
```

需要补实例终端密码时执行 `bupt auth access`；学校登录失效时执行 `bupt login`，再 `bupt wait RUN_ID`。`status --cached` 完全使用本地记录；普通 `status` 检查当前会话但不会主动登录或接管网页登录。SSH 列是已保存路由状态，实际连通性用 `bupt exec gpu -- true` 验证。

需要 OpenSSH/VS Code 兼容入口时，在实例就绪后执行 `bupt ssh-config install gpu --alias bupt-gpu`，以后可用 `ssh bupt-gpu`。安装器保存备份，并将工具管理的 Include 放到现有 SSH 配置前。

控制器重启会断开由它转发的 SSH 窗口；实例上的 tmux 和后台作业仍可继续。交互工作用 `ssh --session work --reconnect` 恢复会话，普通 SSH shell 不承诺跨断线保持。

脚本使用 `--json` / `--jsonl` 或 `-q`，以及 `--non-interactive`。`up --no-wait -q` 只输出 Run ID；诊断和错误写 stderr，SSH/文件/作业流不混入表格。

## 第一次使用：本地控制器

```sh
bupt init --dry-run
bupt init
bupt agent status
bupt auth login
bupt auth status
bupt spec ls
bupt quota
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
bupt auth login --device --wait
```

此模式由服务器保存学校凭据和运行状态，笔记本通过受限 SSH RPC 连接控制器。服务器需要能访问学校 WebVPN；扫码批准的是服务器发起的学校会话。切换登录来源仍可能触发学校重新认证。

本地与远程控制器各自拥有状态库。初始化远程模式不会自动迁移另一控制器的活跃作业、已保存凭据和路由。切换前先处理当前活跃运行，并保留原控制器状态；不要同时让两个控制器管理同一实例的生命周期。

## 登录与凭据

| 统一身份认证方式 | 命令 | 校外 WebVPN 过期时 |
| --- | --- | --- |
| 手机扫码 | `bupt login` | 先扫 WebVPN 码，再扫统一认证码 |
| 学工号和密码 | `bupt login --sso-password` | 先扫 WebVPN 码，再在终端输入统一认证账号和密码 |

`bupt auth login` 和 `bupt login` 接受相同选项。学校会话仍有效时会跳过对应步骤。两种方式都以门户确认校园身份为登录成功。裸算力服务在首次使用时核对自己的会话。`bupt tui access` 的「学校登录」也可选择这两种方式。

统一认证密码在终端隐藏输入。控制器只在本次向学校 CAS 提交时使用它，之后保存登录会话。可用 `--username` 预填学工号。出现图形验证码时按提示填写，遇到其他额外验证可改用 `bupt login` 扫码。脚本可以让密码管理器通过标准输入向 CLI 提供密码。

```sh
bupt login --sso-password --username YOUR_CAMPUS_ID
secret-provider read campus-password | bupt login --sso-password --username YOUR_CAMPUS_ID --password-stdin
bupt auth login --device --wait --non-interactive --qr-file ./bupt-login.png
bupt auth login --device --no-wait --qr-file ./bupt-pending.png --json
bupt auth poll --wait --json
bupt auth info --json
```

没有交互终端时，显式使用 `--device --wait` 等待人工扫码。`--no-wait` 返回当前 challenge，`auth poll` 查看并推进完整登录流程，`auth poll --wait` 继续等待。二维码过期或控制器要求刷新时会返回明确错误，需要重新运行 `auth login`；不会反复提交密码。

`--qr-file ./bupt-login.png` 生成 `bupt-login.webvpn.png`、`bupt-login.sso.png` 等 0600 文件。同阶段出现新 URL 时添加编号。已有文件不会被覆盖，重复启动请换一个文件名前缀。`auth device` / `auth login --gateway-only` 明确只完成 WebVPN，不能代替后面的统一认证。

只有学校明确返回 `AUTH_CONFIRM_REQUIRED` 时，才由你决定是否运行 `bupt auth confirm --take-over` 替换另一个 WebVPN 登录；工具不会自动确认。

实例终端首次安装 SSH 公钥时可能需要实例密码。它与扫码登录是两个用途：

```sh
bupt auth access
bupt ssh ensure EXISTING_RUNNING_INSTANCE
bupt ssh EXISTING_RUNNING_INSTANCE
```

`auth access` 隐藏输入并加密保存**实例终端密码**，只用于 SSH 公钥初始化，不设置服务端密码，也不执行登录尝试。实例账户名来自控制器核验的节点/账户信息。`ssh ensure` 只准备或修复已运行实例的 SSH，不创建实例、不申请资源；多节点加 `--node NODE_ID`。需要自动化输入时使用 `auth access --password-stdin`。

算力平台本地口令使用 `--local`。为兼容旧脚本，单独使用凭据 stdin 也选择该接口。脚本传入统一认证凭据时须同时指定 `--sso-password`。`auth refresh` 是已有平台本地凭据的兼容入口；统一认证过期请重新执行默认 `auth login`。

```sh
bupt auth login --local --username LOCAL_PLATFORM_ACCOUNT

secret-provider read platform-password | \
  bupt auth login --username LOCAL_PLATFORM_ACCOUNT --password-stdin --non-interactive

secret-provider read platform-credentials-json | \
  bupt auth login --credentials-stdin --non-interactive
```

`secret-provider` 代表你使用的凭据管理器，不是本工具内置命令。JSON 输入可包含 `username`、`password`，以及本地接口确实要求时的 `token`、`captcha_uuid`、`captcha_code`。口令参数不接受明文 argv。不要把真实凭据放进命令历史、项目 TOML、仓库文件或 CI 日志。本地口令登录遇到图形验证码时保留本次已输入的密码，最多请求三张验证码，由你输入答案；Ghostty/kitty 可直接显示 PNG，其他环境保存私有图片供查看。

```sh
bupt auth captcha --output ./captcha.png --json
bupt auth setup
bupt logout
```

`auth setup` 是显式的设置/修改平台本地密码操作，不会由默认统一认证或普通 SSH 自动触发。`bupt logout` 和 `bupt auth logout` 执行同一操作。退出会清除所选配置的学校会话、已保存的平台本地口令和实例终端密码。若存在活跃托管运行，控制器会返回 `ACTIVE_RUNS`。

## 实例的日常生命周期

先查看实际模板，再保存常用规格。`SPEC_ID`、资源约束和权限由当前学校平台决定，不预设某块 GPU 一定可用。

```sh
bupt spec ls
bupt spec show SPEC_ID --json
bupt config set presets.gpu-small.spec_id SPEC_ID
bupt estimate --preset gpu-small

bupt up experiment --preset gpu-small --max-runtime 2h
bupt ssh experiment
bupt exec experiment -- nvidia-smi
bupt down experiment
```

`up` 负责找到或创建逻辑实例、启动、准备并验证 SSH 路由，然后纳入有上限的生命周期。对已有同一托管周期重复执行 `up` 不应重置计时。对别人或手工创建的资源，需要明确 `instance adopt` / `up --adopt INSTANCE_ID`，不会仅凭相同名称静默接管。

```sh
bupt instance ls --jsonl
bupt instance inspect experiment --json
bupt instance adopt INSTANCE_ID --name experiment --max-runtime 2h
bupt instance start experiment --max-runtime 2h
bupt instance stop experiment
bupt instance release experiment
bupt instance restore experiment --max-runtime 2h
bupt instance cancel-queue experiment
bupt instance delete experiment
```

`stop` 是停机；`release` 是停机后解除资源占用、进入平台回收站；`delete` 是永久删除所选实例及其实例数据。默认 `down` 使用 `cleanup=release`，先停机再释放，不默认永久删除。平台回收站的保留期和外部存储规则仍以学校当前设置为准，重要结果应先导出。

`--no-wait` 返回操作回执；之后用 `task show TASK_ID` / `task wait TASK_ID` 查看。命令超时只表示本次等待结束，不能据此认定远端操作失败，更不能立即用新请求重复创建。保留 `task_id`，重试同一意图时使用同一个 `--idempotency-key`。

## 有上限的运行与续期

新的托管实例、启动周期和作业需要有效的 `--max-runtime` 或 `--until`，也可从 preset / 配置继承。准备、排队、启动时间计入该周期；重复连接或更新配置不会从零计时。

```sh
bupt lease status experiment --json
bupt lease set experiment --max-runtime 6h
bupt lease set experiment --no-renew
bupt lease set experiment --cleanup stop
bupt lease renew experiment --duration 30m --reason "finish current experiment"
```

`lease set --max-runtime 6h` 把总上限改为从本周期开始计算的 6 小时，不是在当前时间上再加 6 小时。`--until` 接受带时区的 RFC3339 时间。上限已落在过去时，需要显式 `--stop-now` 才允许触发立即清理。两个上限选项不能同时设置。

普通托管运行在没有任何配置层明确选择时启用有限续期；`--no-renew` 或配置中的 `auto_renew=false` 会保留明确的禁用选择。续期是向学校提交普通用户申请，批准后的实际到期时间、正在等待批准的请求和本工具的最大上限分别记录；提交成功不代表批准成功，也不会越过明确的运行上限。

默认清理宽限为 60 秒，之后按 `force_stop` 决定是否强制停止；可用 `--stop-grace` 和 `--force-stop=false` 调整。认证过期、平台拒绝申请、网络不可达或拒绝强制停止时，工具会保留 `auth_required` / `cleanup_pending` 等状态，而不是声称资源已释放。运行上限是调度约束，不是学校平台在指定秒钟必定完成释放的承诺。

## 后台作业、长连接与文件

```sh
bupt run --preset gpu-small --max-runtime 4h -- python3 train.py
bupt job run --instance experiment --max-runtime 1h --detach -- make test
bupt job ls
bupt job inspect JOB_ID --json
bupt job logs JOB_ID --follow
bupt job attach JOB_ID
bupt job wait JOB_ID
bupt job update JOB_ID --max-runtime 5h
bupt job cancel JOB_ID
```

`run` 默认跟随输出并返回作业退出码；终端中断会请求取消并执行既定清理。`--detach` 只返回作业身份，控制器继续管理。`job wait` / `job logs -f` 被中断只停止等待或查看，不默认取消作业。`job attach` 是跟随持久输出直到结束，后台作业的 stdin 是 `/dev/null`，不是交互式 PTY。

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
bupt sftp experiment

bupt data stores
bupt data ls --store STORE
bupt data push ./dataset.tar datasets/dataset.tar --store STORE
bupt data pull results/model.bin ./model.bin --store STORE
bupt data pull results/model.bin - --store STORE > model.bin
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
force_stop = true

[presets.gpu-small]
spec_id = "SPEC_ID"
```

机器输出使用 `--json`（单个值）、`--jsonl`（逐条记录）或 `--quiet`（ID/标量），三个选项互斥。诊断和提示在 stderr。`ssh`、`exec`、`proxy`、文件下载到 `-` 等原始字节命令不接受格式化选项。远端命令退出码会保留；本地一般错误为 1，用法错误为 2，中断为 130。

为 CI 使用独立 SSH 公钥，由控制器所有者注册到指定 profile，不授予所有者权限：

```sh
bupt client register ci-train --public-key-file ./bupt-ci.pub --allow-profile bupt --json
bupt client register ci-monitor --public-key-file ./bupt-monitor.pub --allow-profile bupt --read-only
bupt client ls
bupt client revoke CLIENT_ID
```

`client` 是控制器 RPC 身份；`key` 是实例登录公钥，两者用途不同。当前授权粒度是 profile 范围及只读/管理两类，不是按单个实例或单个 RPC 自定义权限。只读身份不能 SSH，也不能修改资源。普通 CI 身份不能注册其他客户端、替换学校登录凭据或关闭控制器。

在 CI 的 Secret 文件挂载和环境变量中提供控制器专用私钥路径及已核验的主机公钥：

```sh
export BUPT_CONTROLLER='ssh://bupt@relay.example.edu:22'
export BUPT_CONTROLLER_IDENTITY_FILE="$CI_KEY_FILE"
export BUPT_CONTROLLER_HOST_KEY="$VERIFIED_RELAY_HOST_KEY"

bupt --non-interactive --idempotency-key "$CI_PIPELINE_ID:train" \
  run --name ci-train --preset gpu-small --max-runtime 2h --cleanup release \
  -- python3 /workspace/train.py
```

CI 不存学校密码，也不自动绕过刷脸。学校会话失效时由所有者重新登录；已经运行的实例 SSH 通路仍独立存在。不要在 CI 启用输出 Secret 的 shell trace。

## 状态、维护与排错

| 内容 | 本地默认路径 |
| --- | --- |
| 非 Secret 配置 | `~/.config/bupt/config.toml` |
| 状态库、RPC socket | `~/.local/state/bupt/state.db`、`agent.sock` |
| 加密凭据、主密钥、专用身份 | `~/.local/share/bupt/` |
| 主密钥 | `~/.local/share/bupt/master.key` |

遵循 `XDG_CONFIG_HOME`、`XDG_STATE_HOME`、`XDG_DATA_HOME`。远程安装使用 `/etc/bupt/` 和 `/var/lib/bupt/`，系统服务为 `bupt-agent.service`。Unix socket 与持久化目录仅供所属账户访问；学校密码、Cookie 和 Token 保存在加密 vault 中，SSH 私钥是单独的受保护文件。

备份应包含配置、完整状态目录、数据目录和匹配的主密钥；丢失主密钥无法解密已有凭据。复制 bbolt 状态库前先停止该控制器，避免不一致副本。不要只备份密文却遗失主密钥，也不要提交真实状态文件到仓库。升级时替换二进制、保留这些目录并重启对应服务；安装器替换重要配置时记录备份位置。

```sh
bupt doctor --offline
bupt agent status
bupt auth status --json
bupt lease status experiment --json
bupt task ls --json
bupt audit ls --limit 50 --jsonl
```

若 `ssh` 提示尚无路由，已有运行实例可在学校认证有效时执行 `ssh ensure INSTANCE`，完整创建/启动周期使用 `up`。不要把 SSH 端口连接失败等同于密码过期。若平台操作返回未知结果，查看 task/run 回执再决定重试。`console` 使用学校终端通道，和独立 SSH 的认证要求不同。

实现与验证范围见 [协议说明](protocol.md)。仓库的协议模拟、状态恢复和命令行测试不等价于所有真实模板的线上创建、续期、释放和删除验证；线上验收应记录具体实例、模板及操作结果。

CLI 与 TUI 共用的分层与类型化接口见 [架构说明](architecture.md)。CLI、SDK 和控制器共享配置默认值，自动续期默认关闭，需要 `--renew` 或显式配置；任何托管运行仍需有限时长。

更换网络出口后，WebVPN 可能要求重新认证。运行 `bupt login` 可在当前网络恢复学校会话。`bupt auth probe --json` 可使用不写回主会话的副本检查当前状态；探测本身仍可能算作服务端活动，不能拿连续探测结果冒充空闲寿命测试。
