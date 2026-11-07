// Package ptyscreen runs a program on a pseudo-terminal and draws its output
// into an emulated screen, so its caller can press keys, wait on what the screen
// shows, and read the screen as text, as style runs or as an image.
package ptyscreen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
)

const (
	// quietWindow is how long the terminal must sit idle, after the last output
	// and the last key, before a wait that has matched returns. So a frame painted
	// across several writes is whole, and a program has had time to answer a key
	// even where the wait's text was already on the screen.
	quietWindow = 50 * time.Millisecond
	// quietLimit ends the search for quiet, because a spinner or a clock never
	// goes quiet, and the text a wait named has already appeared.
	quietLimit = 500 * time.Millisecond
	// terminateGrace is how long a program gets to act on SIGTERM before SIGKILL.
	terminateGrace = 2 * time.Second
	// closeLimit bounds how long Close waits on each stream it has shut.
	closeLimit = 2 * time.Second
	// keyGap follows every key, so a program reads it alone. Bubble Tea v1 turns a
	// burst of characters in one read into one key, and "jj" matches no binding.
	keyGap = 5 * time.Millisecond
	// escapeGap follows an escape key. A key arriving sooner reads as alt plus
	// that key, and a terminal input parser waits a short timeout to tell them
	// apart, DefaultEscTimeout in ultraviolet.
	escapeGap = 75 * time.Millisecond
)

// Terminal is one running program and the screen its output draws.
type Terminal struct {
	path    string
	scratch string
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	pty     xpty.Pty

	mu            sync.Mutex
	emulator      *vt.Emulator
	timeout       time.Duration
	lastActivity  time.Time
	synchronizing bool
	lastFinished  Frame
	masks         []Mask
	exitCode      int
	hasExited     bool

	changed    chan struct{}
	exited     chan struct{}
	outputDone chan struct{}
	inputDone  chan struct{}
	closeOnce  sync.Once
	closeErr   error
}

// Open starts the program on a pty and begins drawing its output.
func Open(program Program) (*Terminal, error) {
	program = program.withDefaults()
	if err := program.Check(); err != nil {
		return nil, err
	}
	path, err := program.resolvedPath()
	if err != nil {
		return nil, err
	}
	scratch, err := createScratchDirs()
	if err != nil {
		return nil, err
	}
	pty, err := xpty.NewPty(program.Cols, program.Rows)
	if err != nil {
		_ = os.RemoveAll(scratch)
		return nil, fmt.Errorf("open pty: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, path, program.Args...)
	cmd.Env = program.environ(scratch)
	cmd.Dir = program.Dir
	if cmd.Dir == "" {
		cmd.Dir = filepath.Join(scratch, "work")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = terminateGrace

	t := &Terminal{
		path:         path,
		scratch:      scratch,
		cmd:          cmd,
		cancel:       cancel,
		pty:          pty,
		emulator:     vt.NewEmulator(program.Cols, program.Rows),
		timeout:      program.WaitTimeout,
		lastActivity: time.Now(),
		changed:      make(chan struct{}, 1),
		exited:       make(chan struct{}),
		outputDone:   make(chan struct{}),
		inputDone:    make(chan struct{}),
	}
	t.emulator.SetCallbacks(vt.Callbacks{
		EnableMode:  t.onModeEnabled,
		DisableMode: t.onModeDisabled,
	})
	t.emulator.RegisterCsiHandler(ansi.Command('?', '$', 'p'), t.onModeRequested)
	t.lastFinished = t.captureFrame()

	if err := pty.Start(cmd); err != nil {
		cancel()
		_ = pty.Close()
		_ = os.RemoveAll(scratch)
		return nil, fmt.Errorf("start %s: %w", path, err)
	}
	// The child holds its own copy now. Closing this one lets a read of the
	// master end once the child and everything it started have gone.
	if unixPty, ok := pty.(*xpty.UnixPty); ok {
		_ = unixPty.Slave().Close()
	}

	go t.pumpOutput()
	go t.pumpInput()
	go t.awaitExit()
	return t, nil
}

// onModeEnabled and onModeDisabled run inside emulator.Write, with mu held.
// A program brackets each frame in DEC mode 2026, so what the screen held as
// the bracket opened is the last whole frame, until the bracket closes.
func (t *Terminal) onModeEnabled(mode ansi.Mode) {
	if mode == ansi.ModeSynchronizedOutput {
		t.lastFinished = t.captureFrame()
		t.synchronizing = true
	}
}

func (t *Terminal) onModeDisabled(mode ansi.Mode) {
	if mode == ansi.ModeSynchronizedOutput {
		t.synchronizing = false
	}
}

// onModeRequested reports mode 2026 as supported, which the emulator does not. A
// Bubble Tea v2 program brackets its frames only after this reply, as it does on the
// terminals people run it on, so a read then never lands inside a frame. Any other
// mode falls through to the emulator's own handler.
func (t *Terminal) onModeRequested(params ansi.Params) bool {
	mode, _, ok := params.Param(0, 0)
	if !ok || mode != ansi.ModeSynchronizedOutput.Mode() {
		return false
	}
	setting := ansi.ModeReset
	if t.synchronizing {
		setting = ansi.ModeSet
	}
	_, _ = io.WriteString(t.emulator.InputPipe(), ansi.ReportMode(ansi.ModeSynchronizedOutput, setting))
	return true
}

func (t *Terminal) pumpOutput() {
	defer close(t.outputDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.mu.Lock()
			_, _ = t.emulator.Write(buf[:n])
			t.lastActivity = time.Now()
			t.mu.Unlock()
			t.notifyChanged()
		}
		if err != nil {
			return
		}
	}
}

// pumpInput carries encoded keys and the emulator's replies to the program's
// terminal queries. They reach the emulator's unbuffered pipe with mu held, so this
// never stops reading, and a queue stands between it and the pty. A write to the pty
// blocks once a program stops reading its terminal, and with no queue that would
// hold mu until the program ended, and no wait could time out.
//
// Close does not wait for the writer. The pty's master is in blocking mode, so a
// write already stuck cannot be interrupted, and it keeps a thread and the master
// until the process ends.
func (t *Terminal) pumpInput() {
	defer close(t.inputDone)
	queue := newInputQueue()
	go queue.writeTo(t.pty)
	buf := make([]byte, 4096)
	for {
		n, err := t.emulator.Read(buf)
		if n > 0 {
			queue.add(buf[:n])
		}
		if err != nil {
			queue.close()
			return
		}
	}
}

func (t *Terminal) awaitExit() {
	defer close(t.exited)
	err := t.cmd.Wait()
	code := 0
	if err != nil {
		code = -1
		if state := t.cmd.ProcessState; state != nil {
			code = state.ExitCode()
		}
	}
	t.mu.Lock()
	t.exitCode = code
	t.hasExited = true
	t.synchronizing = false
	t.mu.Unlock()
	t.notifyChanged()
}

func (t *Terminal) notifyChanged() {
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

// ScratchDir returns the directory the program's HOME, XDG directories and
// default working directory sit under. Close removes it.
func (t *Terminal) ScratchDir() string { return t.scratch }

// Press sends each key in turn. A key is one character, or a name KeyNames lists,
// with ctrl+, alt+ or shift+ where the terminal has an encoding for it.
func (t *Terminal) Press(keys ...string) error {
	for _, name := range keys {
		key, err := ParseKey(name)
		if err != nil {
			return err
		}
		if err := t.sendInput(func() { t.emulator.SendKey(key) }); err != nil {
			return err
		}
		if name == "esc" {
			time.Sleep(escapeGap)
		} else {
			time.Sleep(keyGap)
		}
	}
	return nil
}

// Type sends text as one burst of characters. It is for a text field. A keybinding
// goes through Press, because a program can read a burst in one read as one
// multi-character key, which matches no binding.
func (t *Terminal) Type(text string) error {
	return t.sendInput(func() { t.emulator.SendText(text) })
}

func (t *Terminal) sendInput(send func()) error {
	t.mu.Lock()
	hasExited := t.hasExited
	if !hasExited {
		send()
		t.lastActivity = time.Now()
	}
	t.mu.Unlock()
	if hasExited {
		return t.exitedError()
	}
	return nil
}

func (t *Terminal) exitedError() *ExitedError {
	t.awaitOutputDrained()
	t.mu.Lock()
	defer t.mu.Unlock()
	return &ExitedError{Code: t.exitCode, Frame: t.currentFrame()}
}

// FailedExit lets the output settle, then returns an ExitedError if the program
// has ended with a code other than 0.
func (t *Terminal) FailedExit() error {
	t.Settle()
	if !t.isExited() {
		return nil
	}
	if failure := t.exitedError(); failure.Code != 0 {
		return failure
	}
	return nil
}

// SetWaitTimeout bounds the waits that follow.
func (t *Terminal) SetWaitTimeout(timeout time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timeout = timeout
}

// AddMask applies to every frame Frame returns from here on, after the masks already
// added.
func (t *Terminal) AddMask(mask Mask) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.masks = append(t.masks, mask)
}

// Frame returns the screen as it stands, read through the terminal's masks. While
// the program holds a synchronized update open, that is the last whole frame.
func (t *Terminal) Frame() Frame {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.currentFrame().masked(t.masks)
}

// SettledFrame lets the output settle first, so the frame is not half painted.
func (t *Terminal) SettledFrame() Frame {
	t.Settle()
	return t.Frame()
}

func (t *Terminal) currentFrame() Frame {
	if t.synchronizing {
		return t.lastFinished
	}
	return t.captureFrame()
}

// WaitFor blocks until the screen contains text and the output has settled.
func (t *Terminal) WaitFor(text string) error {
	return t.wait(fmt.Sprintf("text %q", text), func(screen string) bool {
		return strings.Contains(screen, text)
	})
}

// WaitForPattern blocks until pattern matches the screen and the output has
// settled. It reads the whole screen as one line per row, so (?m)^ and $ anchor a
// row.
func (t *Terminal) WaitForPattern(pattern *regexp.Regexp) error {
	return t.wait(fmt.Sprintf("pattern /%s/", pattern), pattern.MatchString)
}

// WaitGone blocks until text is off the screen. It passes at once on a blank
// screen, so it follows a wait for something that precedes the text going.
func (t *Terminal) WaitGone(text string) error {
	return t.wait(fmt.Sprintf("text %q to disappear", text), func(screen string) bool {
		return !strings.Contains(screen, text)
	})
}

// WaitGonePattern is WaitGone for a pattern.
func (t *Terminal) WaitGonePattern(pattern *regexp.Regexp) error {
	return t.wait(fmt.Sprintf("pattern /%s/ to disappear", pattern), func(screen string) bool {
		return !pattern.MatchString(screen)
	})
}

// Settle blocks until the output stops, for at most quietLimit, so a frame read
// next is not half painted.
func (t *Terminal) Settle() {
	_ = t.wait("output to settle", func(string) bool { return true })
}

func (t *Terminal) wait(want string, holds func(screen string) bool) error {
	t.mu.Lock()
	timeout := t.timeout
	t.mu.Unlock()
	started := time.Now()
	deadline := started.Add(timeout)
	var heldSince time.Time
	for {
		hasExited := t.isExited()
		if hasExited {
			t.awaitOutputDrained()
		}
		t.mu.Lock()
		frame := t.currentFrame()
		quietFor := time.Since(t.lastActivity)
		code := t.exitCode
		t.mu.Unlock()

		pause := time.Until(deadline)
		if holds(frame.rawText()) {
			if heldSince.IsZero() {
				heldSince = time.Now()
			}
			if hasExited || quietFor >= quietWindow || time.Since(heldSince) >= quietLimit || pause <= 0 {
				return nil
			}
			pause = min(pause, quietWindow-quietFor)
		} else {
			heldSince = time.Time{}
			if hasExited || pause <= 0 {
				return &WaitError{Want: want, Elapsed: time.Since(started), Frame: frame, Exited: hasExited, ExitCode: code}
			}
		}

		timer := time.NewTimer(pause)
		select {
		case <-t.changed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (t *Terminal) isExited() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hasExited
}

// awaitOutputDrained gives the read of the program's last output time to finish
// after it exits. A read that never ends means something the program started
// still holds the terminal, so the wait is short.
func (t *Terminal) awaitOutputDrained() {
	t.waitOn(t.outputDone, quietWindow)
}

// WaitExit blocks until the program ends and returns its exit code, which is -1
// where a signal ended it. It returns a WaitError if the wait times out.
func (t *Terminal) WaitExit() (int, error) {
	t.mu.Lock()
	timeout := t.timeout
	t.mu.Unlock()
	started := time.Now()
	if !t.waitOn(t.exited, timeout) {
		return 0, &WaitError{Want: "the program to exit", Elapsed: time.Since(started), Frame: t.Frame()}
	}
	t.awaitOutputDrained()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, nil
}

// Close ends the program if it still runs, and removes its scratch directory.
func (t *Terminal) Close() error {
	t.closeOnce.Do(func() { t.closeErr = t.shutDown() })
	return t.closeErr
}

func (t *Terminal) shutDown() error {
	t.cancel()
	var problems []error
	if !t.waitOn(t.exited, terminateGrace+closeLimit) {
		problems = append(problems, fmt.Errorf("%s did not exit after SIGKILL", t.path))
	}
	// Closing the writer rather than calling emulator.Close leaves the read in
	// pumpInput to end by itself, since Emulator.Close and Emulator.Read touch
	// the same field with no lock.
	if input, ok := t.emulator.InputPipe().(io.Closer); ok {
		_ = input.Close()
	}
	_ = t.pty.Close()
	if !t.waitOn(t.outputDone, closeLimit) {
		problems = append(problems, errors.New("the pty read did not end"))
	}
	if !t.waitOn(t.inputDone, closeLimit) {
		problems = append(problems, errors.New("the emulator's input read did not end"))
	}
	_ = t.emulator.Close()
	if err := os.RemoveAll(t.scratch); err != nil {
		problems = append(problems, fmt.Errorf("remove scratch directory: %w", err))
	}
	return errors.Join(problems...)
}

func (t *Terminal) waitOn(done <-chan struct{}, limit time.Duration) bool {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// WaitError says what a wait wanted and shows the screen it was looking at.
type WaitError struct {
	Want     string
	Elapsed  time.Duration
	Frame    Frame
	Exited   bool
	ExitCode int
}

func (e *WaitError) Error() string {
	var b strings.Builder
	if e.Exited {
		fmt.Fprintf(&b, "waited for %s, but the program exited with code %d after %s", e.Want, e.ExitCode, e.Elapsed.Round(time.Millisecond))
	} else {
		fmt.Fprintf(&b, "timed out after %s waiting for %s", e.Elapsed.Round(time.Millisecond), e.Want)
	}
	fmt.Fprintf(&b, "\n%s", e.Frame.Report())
	return b.String()
}

// ExitedError is a key pressed or text typed after the program ended, or an exit
// nothing expected. It shows the screen the program left.
type ExitedError struct {
	Code  int
	Frame Frame
}

func (e *ExitedError) Error() string {
	return fmt.Sprintf("the program already exited with code %d\n%s", e.Code, e.Frame.Report())
}
