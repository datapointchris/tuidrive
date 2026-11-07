// Package tuidrive drives a terminal program's real binary from a Go test. It
// starts the program on a pty at a fixed size with a built environment, presses
// keys, waits on named text, compares the screen with a text golden and a style
// sidecar, and writes a frame as a PNG.
//
// Import it from _test.go files only. A failed step fails the test. A step the
// program failed also prints the screen it was looking at.
//
//	bin := tuidrive.Build(t, ".")
//	term := tuidrive.Start(t, tuidrive.Program{Path: bin})
//	term.WaitFor("sort newest")
//	term.Press("j", "j", "enter")
//	term.WaitFor("kind:")
//	term.Golden("open-row")
//	term.WritePNG(filepath.Join(t.TempDir(), "open-row.png"))
package tuidrive

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/datapointchris/tuidrive/keyscript"
	"github.com/datapointchris/tuidrive/ptyscreen"
)

// Program names what to run and the environment it runs in.
type Program = ptyscreen.Program

// Frame is the screen at one moment.
type Frame = ptyscreen.Frame

// buildLimit bounds a go build, and leaves room for a cold module cache.
const buildLimit = 5 * time.Minute

// Build compiles a Go package to a binary under the test's temporary directory
// and returns its path. A relative package is relative to the test's directory,
// as for go build.
func Build(t testing.TB, pkg string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), buildLimit)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "program-under-test")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, pkg)
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
	return binary
}

// Terminal is a running program whose every failed step fails the test.
type Terminal struct {
	t        testing.TB
	terminal *ptyscreen.Terminal
}

// Start runs the program on a pty, and ends it when the test ends.
func Start(t testing.TB, program Program) *Terminal {
	t.Helper()
	terminal, err := ptyscreen.Open(program)
	if err != nil {
		t.Fatalf("tuidrive: %v", err)
	}
	t.Cleanup(func() {
		if err := terminal.Close(); err != nil {
			t.Errorf("tuidrive: close: %v", err)
		}
	})
	return &Terminal{t: t, terminal: terminal}
}

// ScratchDir returns the directory the program's HOME, XDG directories and default
// working directory sit under. It is removed when the test ends.
func (term *Terminal) ScratchDir() string { return term.terminal.ScratchDir() }

// Press sends each key in turn: a character, or a name such as enter, esc, tab,
// up, ctrl+c or shift+tab.
func (term *Terminal) Press(keys ...string) {
	term.t.Helper()
	if err := term.terminal.Press(keys...); err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
}

// Type sends text to a text field.
func (term *Terminal) Type(text string) {
	term.t.Helper()
	if err := term.terminal.Type(text); err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
}

// SetWaitTimeout bounds the waits that follow.
func (term *Terminal) SetWaitTimeout(timeout time.Duration) {
	term.terminal.SetWaitTimeout(timeout)
}

// WaitFor blocks until the screen contains text and the output has settled.
func (term *Terminal) WaitFor(text string) {
	term.t.Helper()
	if err := term.terminal.WaitFor(text); err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
}

// WaitForPattern blocks until a regular expression matches the screen and the
// output has settled.
func (term *Terminal) WaitForPattern(pattern string) {
	term.t.Helper()
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
	if err := term.terminal.WaitForPattern(compiled); err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
}

// WaitGone blocks until text is off the screen. It passes at once on a blank
// screen, so it follows a wait for something that precedes the text going.
func (term *Terminal) WaitGone(text string) {
	term.t.Helper()
	if err := term.terminal.WaitGone(text); err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
}

// WaitGonePattern is WaitGone for a regular expression.
func (term *Terminal) WaitGonePattern(pattern string) {
	term.t.Helper()
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
	if err := term.terminal.WaitGonePattern(compiled); err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
}

// WaitExit blocks until the program ends and returns its exit code, which is -1
// where a signal ended it.
func (term *Terminal) WaitExit() int {
	term.t.Helper()
	code, err := term.terminal.WaitExit()
	if err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
	return code
}

// Frame returns the screen as it stands, read through this terminal's masks.
func (term *Terminal) Frame() Frame { return term.terminal.Frame() }

// Mask replaces each match of a regular expression with token in the text and
// style of every later frame. Waits and images still read what the screen holds. A
// script's mask lines add to the same list.
func (term *Terminal) Mask(pattern, token string) {
	term.t.Helper()
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
	term.terminal.AddMask(ptyscreen.Mask{Pattern: compiled, Token: token})
}

// WritePNG waits for the output to settle and writes the screen as a PNG.
func (term *Terminal) WritePNG(path string) {
	term.t.Helper()
	if err := term.terminal.SettledFrame().WritePNG(path); err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
}

// RunScript replays a key script against the running program. Each frame the
// script names is compared with its golden, as Golden compares it.
func (term *Terminal) RunScript(path string) {
	term.t.Helper()
	file, err := os.Open(path)
	if err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
	defer func() { _ = file.Close() }()
	script, err := keyscript.Parse(path, file)
	if err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
	err = script.Run(term.terminal, func(name string, frame ptyscreen.Frame) error {
		term.t.Helper()
		term.compareGolden(name, frame)
		return nil
	})
	if err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
}
