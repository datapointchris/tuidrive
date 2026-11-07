package main

import (
	"bytes"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datapointchris/tuidrive"
)

func runTool(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	root := rootCommand()
	root.SetArgs(args)
	root.SetOut(&stdout)
	root.SetIn(strings.NewReader(stdin))
	err := root.Execute()
	return stdout.String(), err
}

func TestRunPrintsAndSavesFrames(t *testing.T) {
	binary := tuidrive.Build(t, "../../internal/sampletui")
	out := filepath.Join(t.TempDir(), "frames")

	stdout, err := runTool(t, "wait sort newest\npress enter\nwait kind:\nframe opened\npress q\nexits\n",
		"run", "-", "--out", out, "--size", "80x24", "--", binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- frame opened (80x24)", "kind: note"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	saved, err := os.ReadFile(filepath.Join(out, "opened.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "kind: note") {
		t.Errorf("opened.txt lacks the screen:\n%s", saved)
	}
	style, err := os.ReadFile(filepath.Join(out, "opened.style"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(style), "1: 1-9 bold") {
		t.Errorf("opened.style lacks the bold title: %s", style)
	}
	picture, err := os.Open(filepath.Join(out, "opened.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = picture.Close() }()
	if _, err := png.Decode(picture); err != nil {
		t.Errorf("opened.png: %v", err)
	}
}

func TestRunWithoutAFramePrintsTheEndScreen(t *testing.T) {
	stdout, err := runTool(t, "wait hello\n", "run", "-", "--", "sh", "-c", "printf hello; sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "--- frame end") || !strings.Contains(stdout, "hello") {
		t.Errorf("stdout lacks the end screen:\n%s", stdout)
	}
}

func TestRunNamesTheFailedLineAndShowsTheScreen(t *testing.T) {
	_, err := runTool(t, "wait hello\nwait never drawn\n", "run", "-", "--timeout", "200ms", "--", "sh", "-c", "printf hello; sleep 30")
	if err == nil {
		t.Fatal("a wait that cannot pass passed")
	}
	for _, want := range []string{"stdin:2: wait never drawn", `text "never drawn"`, "hello"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	var usage usageError
	if errors.As(err, &usage) {
		t.Errorf("a failed wait reads as a bad command line, which exits 2: %v", err)
	}
}

// A program that panics on a key is what a replay exists to catch, whichever line of
// the script meets the crash.
func TestRunReportsACrashWithTheScreen(t *testing.T) {
	const crash = `printf ready; read -r _; printf 'panic: nil pointer'; exit 2`
	failing := map[string]string{
		"exits expects another code": "wait ready\npress enter\nexits\n",
		"key after the crash":        "wait ready\npress enter\nwait panic\npress y\n",
		"no exits line":              "wait ready\npress enter\nwait panic\nframe after\n",
	}
	for name, script := range failing {
		t.Run(name, func(t *testing.T) {
			_, err := runTool(t, script, "run", "-", "--", "sh", "-c", crash)
			if err == nil {
				t.Fatal("a crash passed")
			}
			if !strings.Contains(err.Error(), "panic: nil pointer") {
				t.Errorf("failure lacks the crash text:\n%v", err)
			}
		})
	}
	if _, err := runTool(t, "wait ready\npress enter\nexits 2\n", "run", "-", "--", "sh", "-c", crash); err != nil {
		t.Errorf("an exits line naming the code failed: %v", err)
	}
}

func TestRunRefusesACommandLineThatCouldNeverRun(t *testing.T) {
	cases := map[string][]string{
		"no program":     {"run", "-"},
		"bad size":       {"run", "-", "--size", "wide", "--", "sh"},
		"bad env":        {"run", "-", "--env", "NOEQUALS", "--", "sh"},
		"missing binary": {"run", "-", "--", "/nonexistent/program"},
		"unknown flag":   {"run", "-", "--nope", "--", "sh"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := runTool(t, "wait x\n", args...)
			var usage usageError
			if !errors.As(err, &usage) {
				t.Errorf("err = %v, want a usage error, which exits 2", err)
			}
		})
	}
}
