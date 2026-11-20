Require bupt
Require jq
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
Type 'test "$BUPT_DEMO_FIXTURE" = 1 || exit 2; PS1="❯ "; set -o pipefail; clear'
Enter
Sleep 300ms
Show
Type "bupt api model ls"
Enter
Wait+Screen /qwen-demo/
Sleep 1.9s
Type "bupt api model example qwen-demo"
Enter
Wait+Screen /BUPT_API_KEY/
Sleep 2.15s
Ctrl+L
Type "bupt compute catalog ls"
Enter
Wait+Screen /gpu-a/
Sleep 1.45s
Type "bupt compute catalog estimate --spec gpu-a"
Enter
Wait+Screen /1.25/
Sleep 1.4s
Type "bupt compute instance create lab --spec gpu-a --max-runtime 20m"
Enter
Wait+Screen /确认提交/
Sleep 1.7s
Type "y"
Enter
Wait+Screen /已提交创建请求/
Sleep 800ms
Type "bupt compute instance wait lab --json | jq '{state, ssh_state}'"
Enter
Wait+Screen /ready/
Sleep 1.25s
Type "bupt ssh lab"
Enter
Wait+Line /student@gpu-lab/
Type "hostname"
Enter
Wait+Line /student@gpu-lab/
Sleep 1.45s
Type "exit"
Enter
Sleep 550ms
Type "bupt compute instance stop lab --wait --json | jq -r .state"
Enter
Wait+Line /^❯/
Sleep 1s
Type "bupt compute instance show lab --json | jq -r .state"
Enter
Wait+Line /^❯/
Sleep 3.0s
