# demo.tape — lazylocal viral terminal demo
# Generate with Charmbracelet VHS: vhs demo.tape

Output demo.gif

Set Shell "zsh"
Set FontSize 15
Set Width 1200
Set Height 680
Set Theme "Catppuccin Mocha"
Set Padding 16
Set LetterSpacing 0
Set LineHeight 1.2

# Launch lazylocal TUI
Type "lazylocal"
Sleep 500ms
Enter

# 1. 3 listening ports automatically claimed as .localhost subdomains
Sleep 3s

# 2. Switch focus from Services pane to HTTP Traffic pane
Tab
Sleep 800ms

# 3. Navigate down to the 500 Internal Server Error checkout request
Down
Sleep 1s

# 4. Press 'r' to instant Replay the captured HTTP request
Type "r"
Sleep 2s

# 5. Press 'd' to view live side-by-side response diff (500 -> 200 OK)
Type "d"
Sleep 2.5s

# 6. Zombie process detected (orphaned git worktree) -> Press 'Z' to sweep
Type "Z"
Sleep 1s
Type "y"
Sleep 2s

# 7. Quit cleanly
Type "q"
Sleep 1s
