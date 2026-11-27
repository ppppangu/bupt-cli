---
description: "让 Pi、Codex 或 Claude Code 通过 shell 发起扫码登录、读取结果，并在超时后恢复作业。"
---

# 在 AI agent 中使用 bupt

先按[安装指南](README.md)安装程序并设置工作区。纯命令行环境首次设置可运行 `bupt init`。如果 agent 的旧 shell 提示找不到 `bupt`，用 `~/.local/bin/bupt` 作为命令路径即可。

## 在会话里扫码

1. 让 agent 执行 `bupt login --agent`。这条命令返回当前步骤的 JSON。若含有 `qr_file`，向用户展示这张 PNG，或给出可打开的本地路径。
2. 用户扫码时，agent 按 `poll_after_ms` 指定的间隔执行 `bupt auth poll --agent`。每次沿用相同的控制器和 profile。出现新图片时再展示，WebVPN 和统一认证可能各有一次扫码。
3. 当 `stage` 为 `complete` 且 `state` 为 `authenticated`，登录完成。此时可以执行 `bupt api model --output id` 获取每行一个的模型 ID。

二维码文件位于运行 CLI 的机器。如果用户看不到该文件，运行 `bupt auth poll --agent --output table` 并展开工具输出，直接扫字符码。有桌面时也可显式加 `--open` 打开当前图片。二维码过期后重新运行 `bupt login --agent`。

## 发现命令和恢复作业

`bupt help --all --json` 列出命令路径，`bupt help compute job run --json` 给出具体参数。查询数据使用 `--json`，逐条处理列表时可用 `--jsonl`。SSH、日志和文件下载到标准输出时使用原始字节流。

| shell 收到的结果 | agent 接下来做什么 |
| --- | --- |
| 收到后台作业 ID | 保存 ID，用 `bupt compute job wait JOB_ID --json` 续等 |
| 前台提交已获回执，等待超时 | 从 stderr 的 JSON 错误读取 `run_id`，对该 ID 续等 |
| 提交返回 `SUBMISSION_UNKNOWN` | 保留原命令、费用授权和返回的 `idempotency_key`，恢复连接后用相同的 `--idempotency-key` 重试 |
| 模型调用返回 `MODEL_RESULT_UNKNOWN` | 先用 `bupt api usage` 核查近期用量，再由用户决定是否重新发送 |
| 命令退出 2，错误为 `USAGE` | 检查命令拼写和选项，修正后再运行 |

表中的 `JOB_ID` 要换成实际回执。需要申请新算力时，先用 `--dry-run` 核价，由用户确定时限和可接受的日价上限，再提交。已有实例上的作业可直接按[后台作业指南](jobs.md)运行。
