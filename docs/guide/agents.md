---
description: "让 Pi、Codex 或 Claude Code 登录同一个工作区，完成一次只读查询。"
---

# AI agent：登录与查询

这里的 AI agent 是代你执行 shell 命令的助手。它使用同一份 bupt 程序和配置；你负责在手机上确认校园身份，并决定是否授权计费。先完成[安装与工作区设置](README.md)。纯命令行环境可用 `bupt init` 设置本机控制器。

## 把二维码交给用户

让助手运行 `bupt login --agent`。每次调用只返回当前一步的 JSON。助手接下来按返回内容行动：

1. 有 `qr_file` 时，向用户展示 PNG 或可打开的文件路径。它位于运行 bupt 的机器上。远程终端可用 `bupt auth poll --agent --output table` 显示字符码；本机有桌面时可用 `--open` 打开图片。
2. 按 `poll_after_ms` 给出的毫秒间隔运行 `bupt auth poll --agent`。沿用原来的控制器和 profile，图片路径变化时再展示下一张码。等待用户扫码期间无需重新发起登录。
3. 同时看到 `stage=complete` 和 `state=authenticated`，才进入业务查询。二维码过期时重新发起登录，学校要求接管已有会话时交给账号本人决定。

## 做一次只读查询

可直接给助手一个任务：「列出我能用的模型 ID。」对应的命令只有一行：

```sh
bupt api model --output id
```

每行是一个模型 ID。查算力实例则运行 `bupt compute instance --json`。要发现其他命令，先取 `bupt help --all --json` 的命令索引，再用 `bupt help compute job run --json` 查选项。

需要助手提交长作业时，先核对时限和费用授权，并让它保存回执。[用回执恢复任务](tasks/receipts.md)
