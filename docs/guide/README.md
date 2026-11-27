---
description: "装好 bupt，用校园账号登录，查到第一份可用资源。"
---

# 快速开始

模型 API 用于直接调用学校部署好的模型，GPU 实例用于把你自己的代码放上去跑。两者共用同一个工作台，安装和登录也只需要做一次。这篇快速开始只涉及查询，不会产生任何费用。

## 1. 安装并打开工作台

你需要一个已开通算力平台权限的北邮账号，以及一台能扫码的手机。在 macOS、Linux 或 WSL2 的终端里运行以下命令（支持 x86_64 和 arm64）：

```sh
curl -fsSL https://github.com/ppppangu/bupt-cli/releases/latest/download/install.sh | sh
```

安装成功后，开一个新终端运行：

```sh
bupt tui
```

屏幕会进入「校园算力平台」工作台：方向键选择，Enter 打开，Esc 返回，q 退出。如果新终端还是找不到命令，直接运行 `~/.local/bin/bupt tui`。

<details>
<summary>离线安装或在 WSL2 里使用</summary>

拿到的是 ZIP 安装包时，解压后进入包含 `install.sh` 的目录，运行 `sh ./install.sh`。Windows 用户在 WSL2 的 Linux 终端里执行同样的安装命令即可。首次设置如果提示无法启动用户服务，在工作台里选「作为本机进程启动」。

</details>

## 2. 登录校园账号

在首页选「首次设置这台电脑」，按提示创建个人工作区——以后 bupt 会在这里保存你的登录会话和任务记录。然后选「登录学校账号」，按手机上的提示完成认证：如果先弹出 WebVPN 码，确认后再扫统一身份认证码。本地已有有效会话时，这两步会自动跳过。

## 3. 查一次资源

回到首页，选一行和你要做的事对应：想用模型按 R，想要 GPU 按 S。能看到对应的列表，这次快速开始就完成了。

![登录后的工作台示例，R 打开模型 API，S 打开裸算力服务器，U 查看用量](.gitbook/assets/workbench.png)

<table data-view="cards">
  <thead><tr><th></th><th></th><th data-hidden data-card-target data-type="content-ref"></th></tr></thead>
  <tbody>
    <tr><td><strong>我想用现成的模型</strong></td><td>按 R →「模型与价格」，看到模型名称和价格之后，可以试着发一条消息。</td><td><a href="models.md">完成第一次对话</a></td></tr>
    <tr><td><strong>我想运行自己的代码</strong></td><td>按 S →「资源与价格」，看到可申请的模板和配额之后，再挑一台合适的机器。</td><td><a href="gpu.md">申请并连接 GPU 实例</a></td></tr>
  </tbody>
</table>

工作台随时可以退出，下次用 `bupt tui` 回来就行。拿不准该走哪条路线，可以先看看[模型调用和 GPU 实例的区别](concepts.md)；如果是 AI agent 代你操作终端，直接看[AI agent 的登录和查询方式](agents.md)。
