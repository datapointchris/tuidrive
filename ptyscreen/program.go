package ptyscreen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// The size and wait a Program gets when it names none.
const (
	DefaultCols        = 100
	DefaultRows        = 30
	DefaultWaitTimeout = 10 * time.Second
)

// Program names what to run and the environment it runs in.
type Program struct {
	// Path is a file path or a name on PATH.
	Path string
	Args []string
	// Env adds to or overrides the built environment. PATH is the only value
	// the parent passes down on its own, so anything else the program should
	// read goes here.
	Env map[string]string
	// Dir is the working directory. Empty means a scratch directory, so a
	// relative path the program writes lands nowhere real.
	Dir string
	// Cols and Rows default to DefaultCols and DefaultRows.
	Cols, Rows int
	// WaitTimeout bounds each wait, and defaults to DefaultWaitTimeout.
	WaitTimeout time.Duration
}

func (p Program) withDefaults() Program {
	if p.Cols == 0 {
		p.Cols = DefaultCols
	}
	if p.Rows == 0 {
		p.Rows = DefaultRows
	}
	if p.WaitTimeout == 0 {
		p.WaitTimeout = DefaultWaitTimeout
	}
	return p
}

// Check refuses a program that could never start, for its own reason. Left to the
// fork, a missing working directory reads as a missing binary.
func (p Program) Check() error {
	if _, err := p.resolvedPath(); err != nil {
		return err
	}
	if p.Dir == "" {
		return nil
	}
	info, err := os.Stat(p.Dir)
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working directory %s is not a directory", p.Dir)
	}
	return nil
}

// CheckWaitTimeout is the one bound on a wait, for a flag and a script's verb alike.
func CheckWaitTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("timeout needs a duration above zero, and got %s", timeout)
	}
	return nil
}

func (p Program) resolvedPath() (string, error) {
	found, err := exec.LookPath(p.Path)
	if err != nil {
		return "", fmt.Errorf("program %q: %w", p.Path, err)
	}
	abs, err := filepath.Abs(found)
	if err != nil {
		return "", fmt.Errorf("program %q: %w", p.Path, err)
	}
	return abs, nil
}

// scratchDirs maps each directory variable to its place under the scratch
// directory, so a program that resolves its config, data, state or cache through
// any of them stays away from the real ones.
var scratchDirs = map[string]string{
	"HOME":            "home",
	"TMPDIR":          "tmp",
	"XDG_CACHE_HOME":  "cache",
	"XDG_CONFIG_DIRS": "config-dirs",
	"XDG_CONFIG_HOME": "config",
	"XDG_DATA_DIRS":   "data-dirs",
	"XDG_DATA_HOME":   "data",
	"XDG_RUNTIME_DIR": "runtime",
	"XDG_STATE_HOME":  "state",
}

// createScratchDirs makes the short-lived tree a program runs under. It sits
// directly under the system's temporary directory rather than under a test's,
// because a Unix socket path stops at about 108 bytes and a test's directory
// can use up most of that.
func createScratchDirs() (string, error) {
	scratch, err := os.MkdirTemp("", "tuidrive-")
	if err != nil {
		return "", fmt.Errorf("scratch directory: %w", err)
	}
	for _, name := range scratchDirs {
		if err := os.Mkdir(filepath.Join(scratch, name), 0o700); err != nil {
			_ = os.RemoveAll(scratch)
			return "", fmt.Errorf("scratch directory: %w", err)
		}
	}
	if err := os.Mkdir(filepath.Join(scratch, "work"), 0o700); err != nil {
		_ = os.RemoveAll(scratch)
		return "", fmt.Errorf("scratch directory: %w", err)
	}
	return scratch, nil
}

// environ builds the program's whole environment, because a value inherited from
// the parent makes a run differ between machines or reach real data. TERM
// without COLORTERM reads as 256 colors. TZ and the locale fix how dates and
// character widths render.
func (p Program) environ(scratch string) []string {
	values := map[string]string{
		"LANG":   "C.UTF-8",
		"LC_ALL": "C.UTF-8",
		"PATH":   os.Getenv("PATH"),
		"TERM":   "xterm-256color",
		"TZ":     "UTC",
	}
	for name, dir := range scratchDirs {
		values[name] = filepath.Join(scratch, dir)
	}
	for name, value := range p.Env {
		values[name] = value
	}
	env := make([]string, 0, len(values))
	for name, value := range values {
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env
}
