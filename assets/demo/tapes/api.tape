# bupt TUI demo: browse models and run a chat trial with a managed key.
# Plays pre-rendered frames from the real TUI; never touches real accounts.
Set Shell zsh
Set Columns 72
Set Rows 24
Set FontSize 18
Set Padding 0
Set Margin 0
Set Framerate 24
Set TypingSpeed 40ms
Set CursorBlink false
Set Theme { "background": "#1b222c", "foreground": "#e6edf3", "cursor": "#58a6ff" }

Type 'export PATH="assets/demo/bin:$PATH"'
Enter
Sleep 800ms

Type "bupt tui api"
Enter
Sleep 1.6s

Type "2"
Sleep 900ms
Escape
Sleep 700ms
Type "1"
Sleep 1s
Enter
Sleep 800ms
Enter
Sleep 800ms
Enter
Sleep 900ms
Type "用一句话介绍北邮的算力平台"
Sleep 400ms
Escape
Sleep 700ms
Down
Sleep 600ms
Enter
Sleep 900ms
Enter
Sleep 700ms
Up
Sleep 600ms
Enter
Sleep 700ms
Enter
Sleep 1.2s
