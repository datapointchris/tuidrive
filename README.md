# tuidrive

Drive a terminal program's real binary on a pty at a fixed size: wait on named text,
compare frames as text, and write a frame as a PNG.

It exists so a program, or an AI session, can test a TUI for real with nobody at the keyboard.
The binary you ship runs on a pseudo-terminal, and its output draws into an emulated screen
([`x/vt`](https://github.com/charmbracelet/x/tree/main/vt)). It works with any terminal
program, whichever library drew it. It needs only `/dev/ptmx`, so it runs on Linux and macOS with
nothing else installed.

## Explore from the command line

```bash
go install github.com/datapointchris/tuidrive/cmd/tuidrive@latest
```

Write what you would do at the keyboard in a script, here `open-row.keys`:

```text
# Wait for text that only appears once the list has loaded.
wait sort newest
frame list
press j j enter
wait kind:
frame opened
press q
exits
```

Replay it, naming the binary after `--`:

```bash
tuidrive run open-row.keys --out frames --env INBOX_DB=/tmp/inbox.db -- ./inbox
```

- **Each `frame` prints the screen as text.** `--out` also saves `opened.txt`, `opened.style`
  and `opened.png`.
- **Each run starts from clean state.** `PATH` is the only variable the program inherits, and
  `HOME` and every XDG directory sit under a scratch directory that is removed afterward. Point
  the program at its data with `--env`.
- **A step the program failed exits 1 and prints the screen it was looking at**, with the script's
  line. A program that ends with a code other than 0 fails the run too, unless an `exits` line names
  that code.
- **`tuidrive run --help` lists every verb and key.**

## Commit the same script as a test

```go
package inbox_test

import (
    "testing"

    "github.com/datapointchris/tuidrive"
)

func TestOpenRow(t *testing.T) {
    term := tuidrive.Start(t, tuidrive.Program{
        Path: tuidrive.Build(t, "."),
        Env:  map[string]string{"INBOX_DB": seededDatabase(t)},
    })
    term.RunScript("testdata/open-row.keys")
}
```

Each `frame` in the script now compares against
`testdata/TestOpenRow/<frame>.txt` and `<frame>.style`. Write them the first time, and accept a
change after that, with:

```bash
TUIDRIVE_UPDATE=1 go test -run TestOpenRow .
```

A test can also make each step itself, and write an image of any frame:

```go
term := tuidrive.Start(t, tuidrive.Program{Path: bin, Cols: 80, Rows: 24})
term.WaitFor("sort newest")
term.Press("j", "j", "enter")
term.WaitFor("kind:")
term.Golden("opened")
term.WritePNG(filepath.Join(t.TempDir(), "opened.png"))
term.Press("q")
if code := term.WaitExit(); code != 0 {
    t.Errorf("exit code %d", code)
}
```

Import it from `_test.go` files only, so what you ship never links the emulator. Its requirements
still count toward your module's versions, so adding it can raise the terminal-width and ANSI
modules your program ships with. Compare `go list -m all` before and after.

## What a frame holds

- **The text golden** is the screen, with trailing spaces and blank bottom rows left off.
- **The style sidecar** has one line per styled run, as `row: first-last attributes`. Rows and
  columns count from 1.

  ```text
  1: 1-9 bold
  3: 3-14 reverse
  3: 16-18 fg=11
  ```

  A color reads as its palette index where it has one, and `#rrggbb` where it does not. A plain
  space neither ends a run nor starts one, so a color change diffs as one line.
- **The PNG** is the screen as it stood, colors included. It paints palette colors with the Tango
  palette, because the indexes alone do not say what your terminal shows.

## Keep a golden stable

- **Point the program at fixed data.** A live source changes between runs, and a row arriving
  moves every row under it.
- **A wait names text that only appears after the change you caused.** Waiting for text already on
  the screen passes at once. An idle wait would pass on the blank frame a program holds while it
  loads, so there is none.
- **Mask what the clock changes.** `mask /\d+[mh] ago\s*/ "<age> "` in a script, or
  `term.Mask(pattern, token)` in a test, replaces each match in the text and style of later
  frames. The token needs no particular width, so `59m ago` and `1h ago` read the same. Taking the
  padding in the pattern keeps the rest of the row in place too. Waits and images still see the real
  screen.
- **A clock cannot be faked from outside a Go binary**, because Go reads the time through the vDSO
  and `LD_PRELOAD` never sees the call. So fixture timestamps sit in the middle of whatever bucket
  the program rounds them to.

## When the binary needs what a test cannot give it

A program that will not start without a service can run its real model in the test binary instead.
Start the test binary itself on the pty, and have it take a branch that runs the program with a
fixture:

```go
func TestInbox(t *testing.T) {
    if os.Getenv("INBOX_HELPER") == "1" {
        if _, err := tea.NewProgram(newModel(fixtureLoader)).Run(); err != nil {
            t.Fatal(err)
        }
        return
    }
    self, err := os.Executable()
    if err != nil {
        t.Fatal(err)
    }
    term := tuidrive.Start(t, tuidrive.Program{
        Path: self,
        Args: []string{"-test.run=^TestInbox$"},
        Env:  map[string]string{"INBOX_HELPER": "1"},
    })
    term.WaitFor("sort newest")
}
```

That runs the real model and renderer on a real pty. It skips what `main` wires up, such as config
and the service's client.

## License

[MIT](LICENSE).
