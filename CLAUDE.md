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
- **Colors print as palette indexes.** A consumer's colors are 0-15 and follow the terminal's
  theme, so a golden pins the index and never an RGB value.
- **Golden updates go through `TUIDRIVE_UPDATE`, not a flag.** A package-level `-update` panics
  when a test binary also imports another package that registers one, and a flag fails in any
  package under `./...` that does not import this one.
- **`bubbletea/v2` and `ultraviolet` stay at the lowest version that works.** A consumer's build
  takes the highest version any dependency asks for, so a higher floor here changes a shipped
  program's renderer because of a test dependency. `go mod tidy` and `go get @latest` raise
  them, and `internal/sampletui` compiling in this module is what proves `x/vt` builds against
  the ultraviolet Bubble Tea asks for.

## Emulator facts the code relies on

- **`x/vt` answers terminal queries through an unbuffered pipe.** With no reader, the emulator
  blocks in `Write`. `pumpInput` never stops reading, for that reason.
- **`Emulator.Read` and `Emulator.Close` touch `closed` with no lock.** So shutdown closes the
  input pipe, waits for the reader to end, and only then closes the emulator.
- **`SendKey` silently drops a combination it has no encoding for**, such as `ctrl+up`. `ParseKey`
  refuses those, so a script fails at parse rather than pressing nothing.
- **`x/vt` tracks mode 2026 but answers a query for it as not recognized, so `onModeRequested`
  answers instead.** Bubble Tea v2 brackets its frames only after that reply, and a read then never
  lands inside a frame. Bubble Tea v1 never brackets, so the quiet window is what protects its frames.

## Never write the breaking-change trailer in a commit message

Those two words — either number, colon or not, subject or body — cut a major
release here, and the module path carries no `/vN` suffix, so once a major
exists `go install …@latest` resolves the highest v1 instead and every installed
copy is stranded. The analyzer matches them unanchored against the raw message,
so no config prevents it and it majors even a `fix:` commit. **The ban covers a
commit that merely discusses the trailer**; name it some other way and never
quote it. Deliberate majors use `chore(release-major)`.
