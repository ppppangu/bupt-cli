# bupt · 北邮算力命令行

面向北邮学生和教师的第三方终端工具，在命令行或全屏界面调用模型、申请 GPU 服务器和连接 SSH。

## 安装

在 macOS、Linux 或 WSL2 终端运行以下命令。

```sh
curl -fsSL https://github.com/ppppangu/bupt-cli/releases/latest/download/install.sh | sh
```

装好后，在新终端运行 `bupt tui`，按提示设置并扫码登录。

[离线安装](packaging/使用说明.txt) · [Agent 用法](docs/usage.md#登录与凭据)

## 创建并连接 GPU 服务器

<img src="docs/demo/assets/tui-compute.gif" width="780" alt="在 TUI 核对 GPU 配置与费用，创建实例，连接 SSH，再回到详情页停机">

核对价格后创建实例，就绪后进入 SSH。退出远端终端即可回到原实例详情并停机。

## 模型 API

### 在线调试

<img src="docs/demo/assets/tui-model.gif" width="780" loading="lazy" alt="选择模型与授权密钥，发送问题，在同一页查看回答、Token、费用和延迟">

选模型、写问题、用已有 Key 发送。回答和本次用量会留在同一个工作台。

### 在程序中调用

<img src="docs/demo/assets/tui-integration.gif" width="780" loading="lazy" alt="选择模型和 Key，在 Python 与 curl 间切换，复制引用 BUPT_API_KEY 的示例">

选好模型和 Key 后切换 Python 或 curl，复制使用 `BUPT_API_KEY` 环境变量的代码。

## 命令行

<img src="docs/demo/assets/cli-demo.gif" width="780" loading="lazy" alt="用 CLI 查询模型和生成示例，核价创建 GPU 实例，等待 SSH，连接并停机">

在终端生成调用代码，再创建、连接和停止服务器。脚本可用 `--output json`、`--output jsonl` 或 `--output id` 读取结果。

演示使用编译后的 `bupt` 和隔离的模拟学校数据。[录制与复验](docs/demo/README.md)

## 功能

| 任务 | 可以做什么 |
| --- | --- |
| 模型 API | 查价格和上下文，管理授权 Key，在线调用，生成接入示例 |
| GPU 实例 | 按模板申请，提交前核对日价，等待 SSH 就绪，停止或归还算力 |
| 存储与连接 | 用 SSH、远程命令、文件传输和端口转发继续处理实验 |
| 费用与用量 | 核对模型请求、算力账单、账户余额和个人配额 |

[联系作者与问题反馈](#联系作者与问题反馈)

## 文档

- [CLI 用法](docs/usage.md#日常使用) · [命令手册](docs/bupt.1)
- [TUI 指南](docs/usage.md#从总览开始)
- [架构](docs/architecture.md) · [协议与脚本契约](docs/protocol.md)
- [构建与分发](docs/releasing.md)

> **非官方声明**：本工具是与学校无隶属、无委托关系的第三方工具。请使用自己的账号和配额，并遵守学校和「人人有算力」平台的使用规定。登录会话和 API Key 等凭据加密保存在本机。密码由学校统一身份认证处理。

## 联系作者与问题反馈

可反馈程序错误、安装与使用问题、模型 API、学校算力平台、配额与计费问题，以及功能建议。

<!-- feedback-contact:start -->
**联系邮箱** [meidici911@gmail.com](mailto:meidici911@gmail.com)
<!-- feedback-contact:end -->

请说明问题现象、操作步骤和发生时间。TUI 中可从「帮助与反馈」或错误页的「反馈此问题」生成邮件。终端可用 `bupt feedback --template` 取得正文，用 `bupt doctor --report` 生成可审阅的脱敏诊断。

作者可协助排查。账号、计费、审批和权限由学校管理部门处理。邮件请勿附上密码、完整 API Key、令牌或私钥。
