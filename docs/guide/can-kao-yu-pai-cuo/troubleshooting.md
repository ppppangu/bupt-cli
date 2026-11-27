---
description: 按当前看到的提示恢复安装、登录、实例连接或作业等待。
---

# 解决常见问题

先看卡在哪一步：打开工作台时先查本机控制器；扫码时看认证阶段；已经提交的任务先找回执。

## 安装后找不到 bupt

新开终端，运行 `bupt version`。如果仍提示找不到命令，试试 `~/.local/bin/bupt version`。能看到版本号说明程序已经安装，可直接用 `~/.local/bin/bupt tui` 打开工作台。使用自选安装目录时，把路径换成实际目录。

## 提示 AGENT\_NOT\_RUNNING

这里的 agent 指 bupt 的本机控制器。打开 `bupt tui`，在首页选择「首次设置这台电脑」或「启动本机控制器」。Linux 或 WSL2 没有用户级 systemd 时，可选「作为本机进程启动」。然后用 `bupt agent status` 检查连接。

## 登录卡在二维码

先看提示属于 WebVPN 还是统一身份认证，每个阶段都要在手机上确认。二维码过期后重新运行 `bupt login`，AI agent 使用 `bupt login --agent`。如果学校要求替换另一个 WebVPN 会话，让账号本人决定是否接管。[Agent 扫码方式](../cao-zuo-zhi-nan/agents/)

## 实例显示运行中，SSH 仍连不上

1. 用 `bupt compute instance show lab` 核对实例，示例中的 `lab` 换成自己的实例名或 ID。
2. 若正在准备 SSH，运行 `bupt compute instance wait lab` 等待连接验证。若提示需要实例终端密码，运行 `bupt auth access` 补充凭据。
3. 对已经运行且提示缺少 SSH 路由的实例，在学校会话有效时运行 `bupt compute instance prepare-ssh lab`，再用 `bupt ssh lab` 连接。校外连接还需要可达的直连地址或已配置的中继。

## 终端断开，任务还在吗？

提交实例后的操作 ID 可用于 `bupt compute instance wait --operation 操作ID`。后台作业用 `bupt compute job show JOB_ID` 查看当前状态，再用 `bupt compute job wait JOB_ID` 续等。请替换为各自的实际 ID。若清理状态仍为 `cleanup_pending`，检查登录状态及实例的预留情况。[按回执恢复任务](../cao-zuo-zhi-nan/agents/receipts.md)

## 反馈问题

记录出错的命令、发生时间和错误码。运行 `bupt feedback --template` 可生成反馈正文，`bupt doctor --report` 可生成脱敏诊断。在「帮助与反馈」中可以找到联系方式。发送前请移除密码、完整 API Key、令牌和私钥。
