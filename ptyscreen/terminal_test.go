package ptyscreen

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func openShell(t *testing.T, script string, timeout time.Duration) *Terminal {
	t.Helper()
	term, err := Open(Program{Path: "sh", Args: []string{"-c", script}, WaitTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := term.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return term
}

func TestFailedWaitShowsTheScreen(t *testing.T) {
	term := openShell(t, `printf 'on the screen'; sleep 30`, 200*time.Millisecond)
	if err := term.WaitFor("on the screen"); err != nil {
		t.Fatal(err)
	}

	err := term.WaitFor("never drawn")
	var failed *WaitError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want a WaitError", err)
	}
	for _, want := range []string{`text "never drawn"`, "on the screen", "100x30"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure lacks %q:\n%v", want, err)
		}
	}
}

func TestWaitEndsOnceTheProgramExits(t *testing.T) {
	term := openShell(t, `printf 'last words'; exit 3`, time.Minute)
	started := time.Now()

	err := term.WaitFor("never drawn")
	var failed *WaitError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want a WaitError", err)
	}
	if !failed.Exited || failed.ExitCode != 3 {
		t.Errorf("Exited = %v, ExitCode = %d, want true and 3", failed.Exited, failed.ExitCode)
	}
	if !strings.Contains(failed.Error(), "last words") {
		t.Errorf("failure lacks the program's last output:\n%v", failed)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("the wait took %s on a program already gone", elapsed)
	}
}

func TestWaitReadsOnlyFinishedFrames(t *testing.T) {
	const hold = 300 * time.Millisecond
	term := openShell(t, `printf 'whole\n\033[?2026hpartial'; sleep 0.3; printf '\033[?2026l'; sleep 30`, 5*time.Second)
	if err := term.WaitFor("whole"); err != nil {
		t.Fatal(err)
	}
	if screen := term.Frame().Text(); strings.Contains(screen, "partial") {
		t.Errorf("read a frame mid-update:\n%s", screen)
	}
	started := time.Now()
	if err := term.WaitFor("partial"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > hold+2*time.Second {
		t.Errorf("the closed update took %s to show", elapsed)
	}
}

// A Bubble Tea v1 program reads `press j j` in one read as the one key "jj", which
// matches no binding, so the cursor stays put and no step fails.
func TestPressGivesEachKeyItsOwnRead(t *testing.T) {
	if os.Getenv("PTYSCREEN_COUNT_READS") == "1" {
		printEachRead()
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	term, err := Open(Program{
		Path: "sh",
		Args: []string{"-c", `stty raw -echo; exec "$0" -test.run='^TestPressGivesEachKeyItsOwnRead$'`, self},
		Env:  map[string]string{"PTYSCREEN_COUNT_READS": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })
	if err := term.WaitFor("bytes=0 reads=0"); err != nil {
		t.Fatal(err)
	}

	const keys = 12
	for range keys {
		if err := term.Press("j"); err != nil {
			t.Fatal(err)
		}
	}
	if err := term.WaitFor(fmt.Sprintf("bytes=%d ", keys)); err != nil {
		t.Fatal(err)
	}
	var reads int
	if _, err := fmt.Sscanf(term.Frame().Text(), "bytes=%d reads=%d", new(int), &reads); err != nil {
		t.Fatal(err)
	}
	if reads <= keys/2 {
		t.Errorf("%d keys arrived in %d reads", keys, reads)
	}
}

// printEachRead takes a moment between reads, as a program does while it handles
// what it read.
func printEachRead() {
	buf := make([]byte, 256)
	bytes, reads := 0, 0
	for {
		fmt.Printf("\rbytes=%d reads=%d ", bytes, reads)
		time.Sleep(2 * time.Millisecond)
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		bytes += n
		reads++
	}
}

func TestStyleReadsPaletteIndexesAndIgnoresUnpaintedBlanks(t *testing.T) {
	term := openShell(t,
		`printf '\033[93mnew\033[m \033[1;38;5;205mbold\033[m \033[48;5;4m  \033[m \033[38;2;1;2;3mrgb\033[m \033[91m \033[m \033[91mfoo bar\033[m'; sleep 30`,
		5*time.Second)
	if err := term.WaitFor("foo bar"); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"1: 1-3 fg=11",
		"1: 5-8 fg=205 bold",
		"1: 10-11 bg=4",
		"1: 13-15 fg=#010203",
		"1: 19-25 fg=9",
	}, "\n") + "\n"
	if got := term.Frame().Style(); got != want {
		t.Errorf("style runs:\n%s\nwant:\n%s", got, want)
	}
}

func TestMaskKeepsARowStableWhenTheMatchChangesWidth(t *testing.T) {
	render := func(age string) Frame {
		t.Helper()
		term := openShell(t, `printf '`+age+`  \033[93mtitle\033[m'; sleep 30`, 5*time.Second)
		if err := term.WaitFor("title"); err != nil {
			t.Fatal(err)
		}
		term.AddMask(Mask{Pattern: regexp.MustCompile(`\d+[mh] ago\s*`), Token: "<age> "})
		return term.Frame()
	}
	wide, narrow := render("59m ago"), render("1h ago ")
	if wide.Text() != "<age> title\n" || narrow.Text() != wide.Text() {
		t.Errorf("text: %q and %q, want both %q", wide.Text(), narrow.Text(), "<age> title\n")
	}
	if wide.Style() != "1: 7-11 fg=11\n" || narrow.Style() != wide.Style() {
		t.Errorf("style: %q and %q, want both %q", wide.Style(), narrow.Style(), "1: 7-11 fg=11\n")
	}
}
