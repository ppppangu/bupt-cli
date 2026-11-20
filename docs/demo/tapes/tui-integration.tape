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
Wait+Screen /在我的程序中调用/
Type "3"
Wait+Screen /代码工作台/
Type "/"
Ctrl+U
Type "模型"
Enter
Sleep 150ms
Enter
Wait+Screen /qwen-demo/
Enter
Sleep 600ms
Type "/"
Ctrl+U
Type "调用密钥"
Enter
Sleep 150ms
Enter
Wait+Screen /课程调试/
Type "/"
Ctrl+U
Type "程序开发"
Enter
Sleep 150ms
Sleep 900ms
Type "/"
Ctrl+U
Type "示例格式"
Enter
Sleep 150ms
Enter
Wait+Screen /Python 标准库/
Type "/"
Ctrl+U
Type "Python"
Enter
Sleep 150ms
Wait+Screen /import os/
Backspace
Wait+Screen /程序开发/
Sleep 1.45s
Type "/"
Ctrl+U
Type "展开或收起全文"
Enter
Sleep 150ms
Enter
Backspace
PageDown 2
Wait+Screen /BUPT_API_KEY/
Sleep 2.7s
Hide
Type "s"
Wait+Screen /保存到新文件/
Type "example.py"
Enter
Wait+Screen /已保存/
Show
Type "/"
Ctrl+U
Type "展开或收起全文"
Enter
Sleep 150ms
Enter
Backspace
Type "/"
Ctrl+U
Type "示例格式"
Enter
Sleep 150ms
Enter
Wait+Screen /Python 标准库/
Type "/"
Ctrl+U
Type "curl"
Enter
Sleep 150ms
Wait+Screen /BUPT_API_KEY/
Backspace
Wait+Screen /程序开发/
Sleep 2s
Type "c"
Wait+Screen /已向终端发送复制请求/
Sleep 3.0s
Hide
Type "s"
Wait+Screen /保存到新文件/
Type "example.sh"
Enter
Wait+Screen /已保存/
Type "q"
