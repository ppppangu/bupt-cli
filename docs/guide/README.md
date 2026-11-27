---
description: "在 macOS、Linux 或 WSL2 上安装 bupt，用自己的北邮账号打开算力工作台。"
---

# 安装并登录 bupt

准备一台 x86_64 或 arm64 电脑、一个有平台使用权限的北邮账号，以及能完成学校扫码认证的手机。Windows 用户在 WSL2 的 Linux 终端中操作。

## 安装

```sh
curl -fsSL https://github.com/ppppangu/bupt-cli/releases/latest/download/install.sh | sh
```

看到安装成功和程序路径后，新开一个终端，运行以下命令。

```sh
bupt tui
```

如果想在当前终端立即打开，可以运行 `~/.local/bin/bupt tui`。

<details>
<summary>已经下载了 ZIP 安装包？</summary>

解压整份安装包，在包含 `install.sh` 的目录运行 `sh ./install.sh`，然后打开工作台。

</details>

## 扫码登录

1. 首页选择「首次设置这台电脑」，按 Enter 创建个人工作区。工作区负责保存会话和跟踪作业，设置一次即可。
2. 选择「登录学校账号」。如果学校要求 WebVPN 认证，先扫 WebVPN 码并在手机上确认，再扫统一身份认证码。仍有效的认证步骤会自动跳过。
3. 回到首页，按 **R** 打开「模型 API」，选择「模型与价格」。看到模型列表后，就可以选模型试用。按 Esc 返回首页，按 **S** 进入「裸算力服务器」。

方向键选择，Enter 打开，Esc 返回，q 退出。Linux 或 WSL2 提示无法启动用户服务时，可在工作台选择「作为本机进程启动」。[解决常见问题](troubleshooting.md)

## 接下来做什么

| 我想做什么 | 操作指南 |
| --- | --- |
| 发一个问题给模型，取得 Python 或 curl 示例 | [试用模型并生成调用代码](models.md) |
| 租用 GPU、通过 SSH 连接、用完归还 | [申请 GPU 并连接 SSH](gpu.md) |
| 把代码上传到实例，关闭终端后继续运行 | [上传文件并运行后台作业](jobs.md) |
| 核对积分、调用量和实例账单 | [查看余额和账单](usage.md) |
| 让 Pi、Codex 或 Claude Code 代为登录和查询 | [在 AI agent 中使用 bupt](agents.md) |

要配置远程控制器、SSH 中继或 CI 身份，请查阅[详细命令与配置手册](https://github.com/ppppangu/bupt-cli/blob/main/docs/usage.md)。
