# tuidrive — Claude Code instructions

A test-only Go module and its command. A consumer imports the root package from
`_test.go` files, so a shipped program never links the terminal emulator.

The root package fails the test on any failed step, and imports `testing`. `ptyscreen` is
the pty, the emulator, the waits and the frames, and returns errors. `keyscript` parses and
replays scripts. `cmd/tuidrive` is a command over those two, with no `testing` in its link.
`internal/sampletui` is the Bubble Tea v2 program the suite drives.

## Constraints that must not regress

- **A wait names what it waits for.** Waiting for the screen to go idle passes on a blank
  frame, because a program commonly holds one while it loads. So a script has no `sleep` and
  no idle wait.
- **The program's environment is built, never inherited.** `PATH` is the one value passed
  down. `HOME` and every `XDG_*` directory sit under a scratch directory. A TUI with no data
  path configured opens its real database, so inheriting the environment risks real data, and
  it also makes one machine's run differ from another's.
- **The scratch directory sits directly under the system's temporary directory.** A Unix
  socket path stops at about 108 bytes, and a test's own temporary directory can use most of
  that.
- **A script holds keys, waits and frames, and never the binary, the size or the
  environment.** So one script runs against two builds, and the session that explored with it
  commits it unchanged as the test.
- **A wait and an image read what the screen holds, and only `Text` and `Style` read through
  masks.** A mask must never hide what a wait expects.
- **`Style` leaves out what nothing paints.** A renderer may or may not carry a foreground
  across a blank, so goldens captured under one Bubble Tea version compare against another.
- **A color with a palette index prints as that index.** A consumer's colors are 0-15 and follow
  the terminal's theme, so a golden pins the index. A truecolor value has no index and prints as
  `#rrggbb`.
- **A run checking a truecolor color passes `--env COLORTERM=truecolor`.** `environ` sets
  `TERM=xterm-256color` and no `COLORTERM`, so Bubble Tea detects 256 colors and rounds every
  24-bit color before writing: a theme's `#2b3b51` records as `bg=23`.
- **Golden updates go through `TUIDRIVE_UPDATE`, not a flag.** A package-level `-update` panics
  when a test binary also imports another package that registers one, and a flag fails in any
  package under `./...` that does not import this one.
- **Every requirement in `go.mod` stays as low as it can, indirect ones included.** A consumer's
  build takes the highest version any dependency asks for, so a floor here changes what a consumer
  ships because of a test dependency.
  - `x/vt` asks for `x/ansi`, `go-runewidth`, `displaywidth`, `uax29`, `colorprofile` and
    `go-colorful` above what a Bubble Tea v1 program has, so those move whatever this module does.
    The README says so.
  - `bubbletea/v2` and `ultraviolet` sit at the versions this module's Bubble Tea v2 consumers run,
    so `internal/sampletui` compiling here proves `x/vt` builds against what they ship. Bubble Tea
    v2.0.0 also passes the suite, and it would narrow those moves without ending them.
  - `x/image` decides `x/text`, and sits at the first release past the advisory `govulncheck` finds
    through font drawing.
  - `go get @latest` and `go get -u` raise these, and `go get` leaves an indirect line high after a
    direct one comes down, so check `go.mod` after either.

## Emulator facts the code relies on

- **`x/vt` answers terminal queries through an unbuffered pipe.** With no reader, the emulator
  blocks in `Write` with `mu` held. So `pumpInput` never stops reading, and it hands what it reads
  to a queue, because a write to the pty blocks once a program stops reading its terminal.
- **`creack/pty` leaves the pty's master in blocking mode.** A read or write of it can never be
  interrupted by closing it. A write stuck at `Close` stays stuck until the process ends, so nothing
  waits for it.
- **`Emulator.Read` and `Emulator.Close` touch `closed` with no lock.** So shutdown closes the
  input pipe, waits for the reader to end, and only then closes the emulator.
- **`SendKey` silently drops a combination it has no encoding for**, such as `ctrl+up`. `ParseKey`
  refuses those, so a script fails at parse rather than pressing nothing.
- **`x/vt` tracks mode 2026 but answers a query for it as not recognized, so `onModeRequested`
  answers instead.** Bubble Tea v2 brackets its frames only after that reply, and a read then never
  lands inside a frame. Bubble Tea v1 never brackets, so the quiet window is what protects its frames.

## Never write the breaking-change trailer in a commit message

Those two words — either number, colon or not, subject or body — mark a major. On
0.x, `allow-initial-development-versions` turns that into a minor. From v1 on they
tag v2, and the module path carries no `/v2` suffix, so `go install …@latest` stays
on the highest v1 and every installed copy is stranded. The analyzer matches them
unanchored against the raw message, so no config prevents it and it majors even a
`fix:` commit. **The ban covers a commit that merely discusses the trailer**; name
it some other way and never quote it. Deliberate majors use `chore(release-major)`,
which on 0.x also cuts a minor until that setting comes off.
