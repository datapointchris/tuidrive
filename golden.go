package tuidrive

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// updateVariable regenerates goldens when it is 1. It is a variable rather than a
// flag, so it reaches every package under ./... and a test binary that already
// registers its own -update keeps working.
const updateVariable = "TUIDRIVE_UPDATE"

// goldenUpdateRequested refuses any other value, because a switch read as set would
// take TUIDRIVE_UPDATE=0 as yes, rewrite every golden and report green.
func goldenUpdateRequested() (bool, error) {
	switch value := os.Getenv(updateVariable); value {
	case "":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("%s takes 1 to write goldens, or no value to compare them, and got %q", updateVariable, value)
	}
}

// Golden waits for the output to settle, then compares the screen with
// testdata/<test name>/<name>.txt and its style with <name>.style beside it. A
// failure prints the screen, the golden and the rows that differ, and the test
// goes on. Run with TUIDRIVE_UPDATE=1 to write them.
func (term *Terminal) Golden(name string) {
	term.t.Helper()
	term.compareGolden(name, term.terminal.SettledFrame())
}

func (term *Terminal) compareGolden(name string, frame Frame) {
	term.t.Helper()
	base := filepath.Join("testdata", filepath.FromSlash(term.t.Name()), name)
	update, err := goldenUpdateRequested()
	if err != nil {
		term.t.Fatalf("tuidrive: %v", err)
	}
	for _, file := range []struct{ path, got string }{
		{base + ".txt", frame.Text()},
		{base + ".style", frame.Style()},
	} {
		if err := compareGoldenFile(file.path, file.got, update); err != nil {
			term.t.Errorf("tuidrive: %v", err)
		}
	}
}

func compareGoldenFile(path, got string, update bool) error {
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("write golden: %w", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			return fmt.Errorf("write golden: %w", err)
		}
		return nil
	}
	want, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("golden %s does not exist. Write it with %s=1 go test\ngot:\n%s", path, updateVariable, got)
	}
	if err != nil {
		return fmt.Errorf("read golden: %w", err)
	}
	if string(want) == got {
		return nil
	}
	return fmt.Errorf("golden %s differs from the screen. Accept this with %s=1 go test\nlines that differ:\n%sgot:\n%swant:\n%s",
		path, updateVariable, lineDiff(string(want), got), got, want)
}

// lineDiff compares by position rather than aligning lines, because a screen's
// row number is part of what it says.
func lineDiff(want, got string) string {
	wantLines := strings.Split(strings.TrimSuffix(want, "\n"), "\n")
	gotLines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	var b strings.Builder
	for i := range max(len(wantLines), len(gotLines)) {
		wantLine, gotLine := "", ""
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if wantLine != gotLine {
			fmt.Fprintf(&b, "%3d want: %s\n%3d got:  %s\n", i+1, wantLine, i+1, gotLine)
		}
	}
	return b.String()
}
