# pywasi -d: pick packages, then a REPL with them. Recorded with VHS
# (https://github.com/charmbracelet/vhs), from the repository root, with
# pywasi built into build/demo and pandas with duckdb linked once already
# (otherwise the recording waits on compiling them):
#
#   go build -o build/demo/pywasi ./cmd/pywasi
#   vhs docs/picker.tape

Output docs/picker.gif

Set Shell "bash"
Set FontSize 18
Set Width 1280
Set Height 760
Set Padding 24
Set Theme "Catppuccin Mocha"
Set TypingSpeed 70ms
Set CursorBlink false

Hide
Type "export PATH=$PWD/build/demo:$PATH PS1='$ ' && cd /tmp && clear"
Enter
Show

Type "pywasi -d"
Sleep 600ms
Enter
Sleep 3s

# Start from nothing selected.
Type "c"
Sleep 800ms

# pandas: numpy comes with it (the lighter check mark).
Type "/pandas"
Sleep 500ms
Enter
Sleep 400ms
Space
Sleep 1.2s

Type "/duckdb"
Sleep 500ms
Enter
Sleep 400ms
Space
Sleep 1s

# Back to the whole list.
Type "/"
Sleep 300ms
Escape
Sleep 3s

Enter
Sleep 2.5s
# The rest of the wait for linking isn't shown.
Hide
Wait@3m />>>/
Show
Sleep 1s

Type `df = pd.DataFrame({"city": ["Oslo", "Lima", "Oslo"], "temp": [3, 19, 5]})`
Sleep 300ms
Enter
Sleep 800ms
Type `duckdb.sql("select city, avg(temp) as avg_temp from df group by city order by city").df()`
Sleep 300ms
Enter
Sleep 3.5s

Type "exit()"
Sleep 300ms
Enter
Sleep 1s
