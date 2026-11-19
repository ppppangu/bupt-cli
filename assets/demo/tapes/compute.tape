# bupt TUI demo: browse templates, review the create form, manage instances.
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

Type "bupt tui compute"
Enter
Sleep 1.6s

Type "3"
Sleep 900ms
Escape
Sleep 700ms
Type "2"
Sleep 1s
Enter
Sleep 900ms
Down
Sleep 500ms
Down
Sleep 500ms
Down
Sleep 500ms
Down
Sleep 500ms
Down
Sleep 500ms
Enter
Sleep 1.1s
Escape
Sleep 800ms
Escape
Sleep 800ms
Escape
Sleep 800ms
Type "1"
Sleep 1.1s
Type "/gpu"
Sleep 900ms
Backspace
Sleep 600ms
Backspace
Sleep 600ms
Backspace
Sleep 900ms
Enter
Sleep 900ms
Up
Sleep 900ms
Right
Sleep 1.2s
