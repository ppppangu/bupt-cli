---
description: "让 Pi、Codex 或 Claude Code 登录同一个工作区，完成一次只读查询。"
---

# AI agent：登录与查询

这里说的 AI agent 是代你执行 shell 命令的助手，用的还是同一份 bupt 程序和配置。手机上的身份确认、是否授权计费，始终由你自己决定。先按[快速开始](README.md)装好、设置工作区；纯命令行环境也可以用 `bupt init` 单独设置本机控制器。

## 把二维码交给用户

让助手运行 `bupt login --agent`，每次调用只返回当前一步的 JSON，助手照着返回内容往下走：

1. 看到 `qr_file` 时，把 PNG 或对应的文件路径展示给用户——它在运行 bupt 的那台机器上。远程终端可以用 `bupt auth poll --agent --output table` 显示字符码，本机有桌面环境的话用 `--open` 直接打开图片。
2. 按 `poll_after_ms` 给的毫秒间隔运行 `bupt auth poll --agent`，继续用原来的控制器和 profile。图片路径变了就展示下一张码，等用户扫码期间不用重新发起登录。
3. 同时看到 `stage=complete` 和 `state=authenticated`，才算登录完成，可以进入业务查询。码过期了就重新登录；学校要求接管已有会话时，这个决定交给账号本人。

## 做一次只读查询

可直接给助手一个任务：「列出我能用的模型 ID。」对应的命令只有一行：

```sh
bupt api model --output id
```

每行是一个模型 ID。查算力实例就运行 `bupt compute instance --json`。想知道还有哪些命令，先看 `bupt help --all --json` 的命令索引，再用 `bupt help compute job run --json` 查某条命令的选项。

要是打算让助手提交长作业，先核对好时限和费用授权，再让它把回执保存下来。连接断掉或者结果不明时怎么处理，见[常见问题](troubleshooting.md#终端断开任务还在吗)。
