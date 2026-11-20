Require bupt
Set Shell bash
Set Columns 100
Set Rows 28
Set FontSize 17
Set FontFamily "Noto Sans Mono CJK SC, Menlo, monospace"
Set Padding 14
Set Framerate 12
Set TypingSpeed 16ms
Set CursorBlink false
Set Theme { "background": "#111923", "foreground": "#e6edf3", "cursor": "#76b7ff" }

Hide
Type 'test "$BUPT_DEMO_FIXTURE" = 1 || exit 2; PS1="❯ "; clear'
Enter
Sleep 300ms
Show
Type "bupt tui"
Enter
Wait+Screen /裸算力服务器/
Sleep 800ms
Type "S"
Wait+Screen /我的实例/
Type "1"
Wait+Screen /course-dev/
Sleep 1.2s
Type "/"
Ctrl+U
Type "创建实例"
Enter
Sleep 150ms
Enter
Wait+Screen /gpu-a/
Type "/"
Ctrl+U
Type "gpu-a"
Enter
Sleep 150ms
Enter
Wait+Screen /实例名称/
Wait+Screen /1.25 积分/
Sleep 1.15s
Type "/"
Ctrl+U
Type "实例名称"
Enter
Sleep 150ms
Enter
Type "lab"
Enter
Sleep 400ms
Type "/"
Ctrl+U
Type "核对创建实例"
Enter
Sleep 150ms
Enter
Wait+Screen /确认创建实例/
Wait+Screen /A100-40GB/
Wait+Screen /1.25 积分/
Sleep 2.4s
Type "/"
Ctrl+U
Type "确认创建实例"
Enter
Sleep 150ms
Enter
Wait+Screen /创建实例.*准备中|创建实例.*创建待核对/
Wait+Screen /启动中/
Wait+Screen /连接 SSH/
Sleep 1.8s
Type "/"
Ctrl+U
Type "连接 SSH"
Enter
Sleep 150ms
Enter
Wait+Screen /student@gpu-lab/
Sleep 850ms
Type "hostname"
Enter
Wait+Screen /student@gpu-lab.*hostname/
Sleep 1.3s
Type "exit"
Enter
Wait+Screen /本页筛选.*连接 SSH/
Backspace
Wait+Screen /仅停止运行/
Sleep 550ms
Type "/"
Ctrl+U
Type "仅停止运行"
Enter
Sleep 150ms
Enter
Wait+Screen /确认仅停止运行/
Sleep 1.5s
Type "/"
Ctrl+U
Type "确认仅停止运行"
Enter
Sleep 150ms
Enter
Wait+Screen /已停止/
Backspace
Down
Sleep 3.0s
Hide
Type "q"
