# bupt · 北邮算力命令行

面向北京邮电大学学生和教师的第三方工具。用自己的学校账号登录「人人有算力」，可以在终端里查模型、余额和 API Key，也可以选算力资源、管理实例和作业。输入 `bupt` 查看命令，输入 `bupt tui` 进入全屏界面。

<p align="center">
  <a href="https://github.com/ppppangu/bupt-cli/releases"><img src="https://img.shields.io/github/v/release/ppppangu/bupt-cli" alt="最新版本"></a>
  <a href="https://github.com/ppppangu/bupt-cli/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue" alt="许可"></a>
  <a href="https://github.com/ppppangu/bupt-cli"><img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20WSL2-lightgrey" alt="平台"></a>
</p>

> **非官方声明**：本工具是与学校无隶属、无委托关系的**第三方工具**。学校账号密码仅在登录时提交给学校统一身份认证的 HTTPS 页面（auth.bupt.edu.cn），不经过任何其他服务器；登录会话、API Key 等凭据加密保存在本机 `~/.bupt/`。请自行遵守学校和「人人有算力」平台的使用规定，违规使用导致的封号、配额与合规后果由使用者自行承担，分发者不对学校系统变更或风控处置负责。

## 一屏看懂

**TUI 管理模型 API**：查模型价格与健康状态，在线调试直接回复，密钥一处选择、创建与停用。

<img src="assets/bupt-tui-api.gif" width="720" alt="TUI 模型 API：查模型、在线调试、选择密钥、查看回复">

**TUI 管理算力服务器**：浏览资源模板与配额，创建实例前先核对规格，实例列表一键筛选、展开详情、连接 SSH。

<img src="assets/bupt-tui-compute.gif" width="720" alt="TUI 算力服务器：资源目录、创建实例表单、实例筛选与详情">

**CLI 面向脚本与 Agent**：`--json` 结构化输出直接进管道，一行命令筛选出想要的模型。

<img src="assets/bupt-cli.gif" width="720" alt="CLI：帮助、--json 管道 jq、实例与用量查询">

动图为真实界面渲染的模拟数据（不涉及真实账号），重新生成方法见 [assets/demo](assets/demo/README.md)。

## 收到安装包后

解压 `bupt-版本号.zip`，在包含 `install.sh` 的目录打开终端，依次执行以下命令。

```sh
sh ./install.sh
export PATH="$HOME/.local/bin:$PATH"
bupt tui
```

同一份 ZIP 适用于 Apple 芯片 Mac、Intel Mac 及 64 位 Linux（x86 或 ARM）。Windows 用户请在 WSL2 的 Linux 终端安装和使用。安装包自带程序，每位使用者在自己的设备上初始化和登录。[安装说明与 WSL2 用法](packaging/使用说明.txt)

首次打开 TUI，在首页选「首次设置这台电脑」，然后选择「登录学校账号」。喜欢命令行的用户可依次运行 `bupt init` 和 `bupt login`。校外网络认证过期时先扫 WebVPN 二维码，接下来可选择统一身份扫码或账号密码。命令行的密码入口是 `bupt login --sso-password`。

## 常用操作

| 想做的事 | 命令 |
| --- | --- |
| 查模型 ID、上下文、价格和健康状态 | `bupt api model` |
| 查近 30 天的用量和余额 | `bupt api usage` |
| 查看我的 API Key | `bupt api key` |
| 查看可申请的算力模板与配额 | `bupt compute catalog` |
| 查看我的实例、预留和截止时间 | `bupt compute instance` |
| 查看平台登录和资源摘要 | `bupt status` |
| 退出当前配置的学校会话 | `bupt logout` |

初次使用可在首页打开「帮助与账户」，或运行 `bupt help tutorial`。桌面上多个 TUI 共用控制器的账号状态，在「账户及相关设置」可调整顶栏。个人配置和状态默认位于 `~/.bupt/`。

课堂演示可用 `bupt tui api` 或 `bupt tui compute`。首页按 `R` 进入模型 API，按 `S` 进入裸算力服务器，按 `U` 查看余额与用量。就绪节点可从「我的实例」连接。↑↓ 选择或滚动阅读，→ 展开分组或打开详情，Enter 执行选中动作，`/` 筛选本页，Esc 返回，退格清除筛选，`q` 退出。脚本可用 `bupt api model --json` 取得结构化结果。[实例、SSH、文件和作业用法](docs/usage.md)

## 给 AI Agent 和脚本

CLI 为机器调用而设计：所有查询命令支持 `--json`（单条结构）与 `--jsonl`（逐行记录），`-q` 只输出最要紧的值，`--non-interactive` 关闭所有提示。诊断和错误写 stderr，表格和结果写 stdout，管道里不会混入干扰。

```sh
# 只留下输入单价低于 0.7 积分的模型 ID
bupt api model --json | jq -r '.models[] | select(.input_points_per_million_tokens < 0.7) | .id'

# 拿到正在运行的实例 ID，交给下一个工具
bupt compute instance --json | jq -r '.instances[] | select(.state == "running") | .id'

# 把 Key 导出到文件或管道（权限 0600 / 标准输出）
bupt api key export KEY_ID --output -
```

把下面的命令包装成 Agent 工具即可，JSON 输入输出契约见 [协议说明](docs/protocol.md#cli-与控制器-rpc)：

```json
{
  "name": "bupt_api_model",
  "description": "列出北邮算力平台可见模型及其 ID、上下文、价格和健康状态",
  "input_schema": {
    "type": "object",
    "properties": { "query": { "type": "string", "description": "按 ID、名称或能力过滤，可留空" } }
  },
  "command": "bupt api model --json"
}
```

模型试用同样可脚本化：`bupt api model try qwen3.8-27b --key KEY_ID --text '问题'`，回复、用量和耗时随 `--json` 一起返回。[命令行示例与结果格式](docs/usage.md#模型试用)

### 在 TUI 里输入并发送

打开 `bupt tui api`，选「在线调试 → 对话」，再选支持对话的模型。选中「你的问题」按 Enter 编辑，Enter 换行，Esc 保留草稿。选「发送请求」后，回复和用量会显示在同一工作台，可修改输入再次发送。

在工作台选「生成调用代码」会带入当前模型、输入和密钥。在代码工作台可切换 Python、curl、JSON 或 bupt 命令，复制或另存全文。选择密钥时，Enter 应用并关闭选择器，Esc 取消。

试用使用当前控制器已保管、已启用并授权此模型的 Key。仅一把符合条件时会自动选中，多把时可自行选择。学校按实际调用计量，浏览和编辑只读取管理数据。[命令行示例与结果格式](docs/usage.md#模型试用)

## 给班级或课题组分发

已有安装包时，把版本化的 ZIP 和同名 `.sha256` 校验文件一起转发即可。收到的人使用自己的配额和权限。

分发即承诺维护：学校统一身份认证页面、WebVPN 或平台接口改版后，需要及时修复并发布新版，再通知使用者升级；无法继续维护时应停止分发，并说明最后支持版本。正式分发使用不带 dev 后缀的版本号（如 0.1.0），并为对应提交打 Git 标签。

维护者从源码重新打包时，在 macOS 或 Linux 上准备 Go 1.26 和 Python 3，然后在仓库根目录执行以下命令。

```sh
make release
make check-release
```

版本号取自 `VERSION`。面向师生的文件是 `dist/bupt-版本号.zip` 和 `dist/bupt-版本号.zip.sha256`。压缩包内附安装脚本、中文使用说明、命令手册、第三方许可和四个平台的二进制。打包只收录这些明确列出的文件。面向源码使用者可再运行 `make source`，它归档当前 Git 提交。

## 开发

`cmd/bupt/` 是程序入口，`internal/` 是 CLI、TUI、控制器和学校平台适配代码，`packaging/` 是随包交付给使用者的文件，`scripts/` 负责构建与打包。

```sh
make build
make test
make race
make vet
```

`make install` 用源码构建并安装本机版本。发布流程和校验方法见[分发说明](docs/releasing.md)，开发接口见[架构](docs/architecture.md)与[协议](docs/protocol.md)。
