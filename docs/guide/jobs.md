---
description: "向已就绪的托管实例上传代码，提交后台作业，并在新终端继续查看结果。"
---

# 上传文件并运行后台作业

先准备一台由 bupt 托管、SSH 已就绪且安装了 Python 3 的实例。下面用 `lab` 指代这台实例，假设本地已有一个 `train.py`。

提交前，在实例详情核对剩余运行时间和清理策略。下面的作业会沿用当前托管周期。如果策略包含停机或释放，作业结束后控制器会执行相应操作。需要保留的实验结果应由脚本写入持久存储。

## 上传并提交

在本地脚本所在的目录上传文件，并检查远端文件名。

```sh
bupt cp ./train.py lab:train.py
bupt exec lab -- ls -l train.py
```

看到远端文件后，提交后台作业。

```sh
bupt compute job run --instance lab --detach --output id -- python3 train.py
```

输出的一行就是作业 ID，请保存。最后一个 `--` 后面是远端要执行的程序和参数。使用 `--detach` 后即可关闭当前终端。

## 查看进度和结果

把 `JOB_ID` 换成刚保存的 ID。在使用同一控制器和账号的终端中查看状态和日志。

```sh
bupt compute job show JOB_ID
bupt compute job logs JOB_ID --follow
```

按 Ctrl+C 可以结束日志查看。要等到作业结束，运行 `bupt compute job wait JOB_ID --json`。这个命令会返回作业结果，并传递远端程序的退出码。等待超时后，用原 ID 再等一次即可。

如果脚本会写出结果文件，在实例仍可通过 SSH 连接时用 `bupt cp lab:result.txt ./result.txt` 下载，把两端文件名换成实际路径。需要长期保留的结果，请事先安排写入持久存储或及时取回，并核对作业结束后的清理状态。
