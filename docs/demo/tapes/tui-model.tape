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
Type "R"
Wait+Screen /在线调试/
Type "2"
Wait+Screen /64K/
Sleep 1.8s
Escape
Wait+Screen /在线调试/
Type "1"
Wait+Screen /选择任务/
Enter
Wait+Screen /qwen-demo/
Sleep 1.3s
Enter
Wait+Screen /你的问题/
Wait+Screen /上下文 64K/
Sleep 1.6s
Type "/"
Ctrl+U
Type "你的问题"
Enter
Sleep 150ms
Enter
Ctrl+U
Type "GPU 和 CPU 有什么区别？"
Ctrl+S
Sleep 750ms
Type "/"
Ctrl+U
Type "调用密钥"
Enter
Sleep 150ms
Enter
Wait+Screen /课程调试/
Sleep 600ms
Type "/"
Ctrl+U
Type "课程调试"
Enter
Sleep 150ms
Sleep 800ms
Type "/"
Ctrl+U
Type "发送请求"
Enter
Sleep 150ms
Enter
Wait+Screen /34 Token/
Wait+Screen /费用/
Wait+Screen /耗时 0.33 秒/
Backspace
Sleep 6.0s
Hide
Type "q"
