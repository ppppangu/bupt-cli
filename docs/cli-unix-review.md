# bupt CLI 体系图（Unix 风格审阅用）

> 本文档只描述 CLI 层现状，供 Unix 风格审阅。实现为 Go + cobra，入口 `cmd/bupt/main.go`，命令定义全部在 `internal/cli/`。
> 配套材料：`docs/usage.md`（用户手册）、`docs/protocol.md`（CLI↔控制器 RPC 契约）、`docs/bupt.1`（man page）。

## 1. 总体架构（三层）

```
bupt CLI (internal/cli, cobra)
   │  JSON-RPC over unix socket / ssh://  (internal/client)
   ▼
本地控制器 (bupt agent，可选远端)
   │  平台 HTTP/WebSocket 协议适配  (internal/platform)
   ▼
学校平台（gpuhub 算力 / webvpn / 模型 API 门户）
```

- 默认控制器是**本机**的；`--controller ssh://…` 可选远端。
- 学校登录过期不影响已建立的 SSH 路由（设计原则之一，错误提示反复强调）。
- `bupt tui` 是同一套命令对象的全屏界面（`internal/cli/tui_commands.go` 把 cobra 命令树转成 TUI 表单）。

## 2. 完整命令树（49 个顶层命令，约 130 个叶子命令）

命令树按 `bupt help --all` 的输出整理。⚠️ 标记 = 供审阅重点。

### 2.1 模型 API（`bupt api`）

```
api                     大模型 API 服务总览（= portal overview）
  model                 模型列表（可查询/别名 models）
    ls | show ID        ⚠️ 与父命令同义，show 按 ID 过滤
    example MODEL_ID    生成 curl/python/json/bupt 示例
    try MODEL_ID        用 Key 实际发一次请求（计费）
  key                   管理 API Key（别名 keys）
    ls | show ID | create NAME | enable ID | disable ID | delete ID
    export ID --output FILE|-   导出到 0600 新文件或 stdout
  usage                 余额与用量（--days 1..30）
  guide                 Base URL + curl/python 接入示例
  leaderboard           排行榜
    display             set MODE (anonymous|partial|real)
  admin                 后台权限视图
```

### 2.2 裸算力（`bupt compute`，最大的域）

```
compute                 总览（status 变体，显示登录/实例/配额）
  catalog               资源池、模板、配额
    ls | show SPEC      模板列表/详情（= 顶层 spec）
    estimate            估价（= 顶层 estimate）
  instance              实例生命周期（别名 instances）
    ls | inspect | create NAME | adopt | start | stop | release
    | restore | delete | wait | events | cancel-queue
  up NAME / down INSTANCE      ⚠️ 顶层亦有同名（同一对象）
  ssh / exec / console / forward / cp / sftp / resize / clone
  storage               持久存储（别名 data）
    stores | ls | push | pull | mkdir | rename | rm
  run                   CLI 托管作业（= 顶层 job，子命令同 job）
    ls | run | inspect | cancel | wait | logs | attach | update
  job                   学校调度作业（只读查询）
    show ID | logs ID | request | template
  admin                 ⚠️ 管理员命令混在普通命令树里
    user | quota | cluster | usage | request | approve | reject
    quota set CLUSTER ACCOUNT   需 --dry-run 预览 + --revision 提交
  quota                 个人与存储额度 ⚠️ 与顶层 quota 不是一回事
    request QUOTA_ID    申请改额度
  project | cluster | dataset | notice | usage | billing…
  remote                远程算力商城
    order create/cancel/payment-code/renew   需 --quote 报价 ID
  llm / features / preset / lease / task / audit / snapshot / service / wait
```

### 2.3 顶层快捷命令（根命令同时存在）

⚠️ 这些命令**同时挂在根和 `compute` 下**，同一 cobra 对象：

```
ls | inspect | up | down | ssh | exec | console | proxy | forward
cp | sftp | resize | clone | instance | lease | snapshot | service
job | run | wait | task | audit | data | spec | estimate | preset | quota
```

### 2.4 文件与连接

```
cp SOURCE DEST         INSTANCE:PATH 远程路径，-r 递归，- 指 stdin/stdout
sftp INSTANCE          内置小 shell：ls pwd cd get put mkdir rename rm quit
forward INSTANCE PORT  --local host:port --remote-host …
ssh INSTANCE [-- CMD]  -t/-T、--session tmux、--reconnect
  ssh ensure           修 SSH 路由
proxy INSTANCE         OpenSSH ProxyCommand 字节流
console INSTANCE       WebSocket 原生终端
```

### 2.5 认证与账户

```
login / logout         WebVPN 扫码 → 统一认证扫码/密码（--sso-password）
auth                   status | info | probe | refresh | device | poll
                       | confirm | captcha | access | setup
account | news | agreement
```

### 2.6 控制器与配置（管理员/自托管）

```
init                   local|remote 控制器部署（--foreground 等）
agent                  run | status | reload | install | stop | gateway(隐藏)
key                    generate | import | ls | revoke   专用客户端密钥
client                 register | ls | revoke    CI 客户端
profile                add | ls | show | use
config                 explain | validate | get | set
ssh-config             export | install    OpenSSH include
preset                 ls | show | set | use | rm   资源+期限预设
completion SHELL | version | doctor [AREA] | help [TOPIC]
```

## 3. 全局选项（16 个，`bupt help --all` 尾部）

```
--config FILE            --project-config FILE   -p/--profile NAME
--controller local|ssh://  --socket PATH
--state-dir DIR          --data-dir DIR
--json | --jsonl | -q/--quiet       三者互斥
--no-color               --non-interactive
--dry-run                --no-project
--timeout DUR (默认10m)   --idempotency-key KEY
```

## 4. 已采用的 Unix 约定

| 约定 | 现状 |
|---|---|
| stdout/stderr 分离 | 数据（表格/JSON/字节流）→ stdout；诊断、警告、`Next:` 提示 → stderr |
| 管道友好 | `--json`/`--jsonl`/`-q`；原始字节命令（ssh/exec/proxy/console/sftp、`pull -`）**拒绝**格式化选项 |
| `-` 约定 | `cp`/`pull`/`push`/`key export` 支持 stdin/stdout |
| 退出码 | 0 成功；1 一般错误；2 用法错误；130 中断；124 作业过期；远端命令退出码透传（0-255） |
| `--` 分隔符 | `exec INSTANCE -- CMD`、`run -- CMD`、`ssh INSTANCE -- CMD` |
| 错误结构化 | `--json` 下错误也是 JSON（`{"error":{code,message,hint,…}}`），stderr |
| 幂等/预览 | 全局 `--idempotency-key`、`--dry-run`；高危删除需 `--confirm ID` 或 `--force` |
| 版本/补全 | `bupt version`、`--version`；bash/zsh/fish 补全 |

## 5. 审阅重点：可能偏离 Unix 风格的设计

1. **一条命令两个路径**：`instance`/`job`/`data`/`spec`/`ssh`/`up`… 同时是顶层和 `compute` 子命令。树里同义命令重复出现（`compute run run`、`compute catalog estimate` vs 顶层 `estimate`）。Unix 传统是一件事一个名字。
2. **组命令默认执行子命令**：`bupt instance`、`bupt job`、`bupt data`、`bupt spec`、`bupt preset`、`bupt auth`、`bupt task`、`bupt compute storage`、`bupt compute run` 不带子命令时直接执行默认动作（多为 `ls`）。传统是组命令只打印帮助或报错。
3. **`quota` 歧义**：顶层 `bupt quota` = 配额上限（quota.get），`bupt compute quota` = 个人与存储额度，另有 `bupt resources` = 资源池总览。三个近义入口。
4. **负向布尔旗标**：`--no-wait`/`--no-renew`/`--no-tty`/`--no-color`/`--no-project` 与正向成对（cobra 惯例），但 `--wait` 默认 true、`--force-stop` 默认 true 这类“默认即副作用”需要读文档才知道。
5. **帮助文案中英混杂**：`instance`/`job`/`lease`/`ssh` 等是英文 Short，`login`/`logout`/`data mkdir|rm`/`compute features`/`remote order` 等是中文 Short；错误提示、`Next:` 提示也是中英混排。对 grep/脚本无影响，但统一性差。
6. **自定义 help 体系**：`bupt`（裸执行）= 中文索引页，`bupt help` 有按用途浏览、`--all` 完整树；`help COMMAND` 走 cobra。三层帮助，用户需要学习入口。
7. **`--json` 的错误输出在 stderr**：管道 `| jq` 时错误不进管道（合理），但 `--jsonl` 场景下错误是单条 JSON 而非 JSONL，混流时需要脚本特判。
8. **`-q` 语义**：不是传统 quiet（抑制输出），而是“只输出标识/标量”，例如 `bupt api usage -q` 输出余额、`bupt resources -q` 输出模板 ID 列表。这是本项目自定义语义。
9. **删除确认方式**：`data rm --confirm 原文件名`（要再输一遍文件名）、`news read ID|all` 的 `|` 语法在 Use 行里只是文档提示，不是真的管道。
10. **`compute` 下塞进大量门户只读查询**（school job/template/dataset/notice/llm/remote…），`compute` 一棵树 60+ 叶子，层级深度到 4（`compute remote order create`）。
11. **`admin` 权限命令与普通命令同树**：无 `--admin` 分离，也无运行时权限隔离入口，仅靠平台侧鉴权。
12. **语言偏好**：命令名全英文小写（符合 Unix），但参数占位（`INSTANCE`/`QUOTA_ID`）与部分 flag 值用中文语义（`--cleanup none|stop|release|delete` 等仍是英文值，OK）。
13. **`version` 有两条路**：`bupt version` 与 `bupt --version`（cobra 内置），输出格式不同（前者 JSON `{"version":…}`，后者纯文本）。
14. **`ssh` 的子命令**：`ssh ensure` 是挂在 ssh 下的第二个动词，而 `exec`/`proxy` 是平级命令——SSH 相关动词分散在平级和子级两处。

## 6. 退出码总表

| 码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 一般/远端错误（含控制器错误，带 `error.code`） |
| 2 | 用法错误、未知命令 |
| 124 | 作业按期限过期退出 |
| 130 | SIGINT 中断 / 作业被取消 |
| 0–255 | `exec`/`run`/`job` 透传远端进程退出码 |

## 7. 快速验证方法（审阅者可自行跑）

```sh
bupt                     # 裸执行 = 中文索引页
bupt help --all          # 完整命令树 + 全局选项
bupt help api / compute  # 按用途浏览
bupt api model --json | jq '.models[].id'
bupt compute instance -q # 只输出运行中实例 ID
bupt ssh demo -- echo hi; echo $?   # 透传远端退出码
bupt instance            # 观察：组命令直接执行 ls
```
