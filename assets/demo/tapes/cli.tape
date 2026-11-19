# bupt CLI demo: scriptable commands and JSON output for agents.
# The shim prints canned data; help text comes from the real binary.
Set Shell zsh
Set Columns 96
Set Rows 24
Set FontSize 16
Set Padding 0
Set Margin 0
Set Framerate 24
Set TypingSpeed 45ms
Set CursorBlink false
Set Theme { "background": "#0d1117", "foreground": "#e6edf3", "cursor": "#58a6ff" }

Type 'export PATH="assets/demo/bin:$PATH"'
Enter
Sleep 800ms

Type "bupt --help"
Enter
Sleep 2.6s

Type "bupt api model --json | jq -r '.models[] | select(.input_points_per_million_tokens < 0.7) | .id'"
Enter
Sleep 2.2s

Type "bupt compute instance --json"
Enter
Sleep 2.2s

Type "bupt status"
Enter
Sleep 1.8s

Type "bupt api usage --days 7"
Enter
Sleep 2.6s
