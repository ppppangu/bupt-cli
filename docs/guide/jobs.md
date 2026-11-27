---
description: "把一个可直接运行的小程序放到 lab，保存作业 ID，并从新的本地终端取回输出。"
---

# 上传代码并运行后台作业

接着使用[上一节](gpu.md)已验证 SSH 的 `lab`。一个**作业**是这台机器上运行的一次程序，有自己的输出和退出码。本例用 Python 3 打印机器名，把结果写入作业日志；远端实例需已安装 Python 3。

## 1. 从本机送一份代码到实例

在本机的空目录中新建 `hello_bupt.py`。内容只需两行：

```python
import socket
print("running on", socket.gethostname())
```

在这个目录运行：

```sh
bupt cp ./hello_bupt.py lab:hello_bupt.py
bupt exec lab -- ls -l hello_bupt.py
```

`lab:` 表示实例上的路径。看到远端文件名后再提交，这样可以把「没传上去」和「程序运行失败」区分开。

## 2. 提交并保存作业 ID

先在实例详情核对剩余运行时间和清理策略。这个作业沿用当前托管周期；如果策略包含停机或释放，作业结束后控制器会开始清理。实际实验的大文件应事先安排写入持久存储。

```sh
bupt compute job run --instance lab --detach --output id -- python3 hello_bupt.py
```

输出的一行就是**作业 ID**，请复制保存。最后一个 `--` 后是远端要运行的程序。用了 `--detach`，提交后可以关闭本地终端。

## 3. 在新终端查看结果

在使用同一工作区和账号的终端中，把 `JOB_ID` 换成刚保存的 ID：

```sh
bupt compute job wait JOB_ID --json
bupt compute job logs JOB_ID
```

在日志中看到 `running on` 和实例主机名，说明你上传的程序已经在实例上执行。长作业可以先用 `bupt compute job logs JOB_ID --follow` 查看实时进度，按 Ctrl+C 结束查看。

实验结束后继续[检查清理状态、归还算力](tasks/release.md)。等待超时或丢失响应时，先看[如何用回执恢复任务](tasks/receipts.md)。
