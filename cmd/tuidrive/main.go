// tuidrive replays a key script against a terminal program's real binary on a pty,
// and prints or saves the frames it names.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/datapointchris/tuidrive/keyscript"
	"github.com/datapointchris/tuidrive/ptyscreen"
)

// version is set through -ldflags by goreleaser.
var version = "dev"

var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// buildVersion falls back to the module version, because go install applies no
// ldflags but does stamp a tagged build. A pseudo-version from a local build is
// valid semver too, which is why only a plain vX.Y.Z counts.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && releaseVersion.MatchString(info.Main.Version) {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return version
}

// usageError is a command line that could never run, which exits 2.
type usageError struct{ error }

func main() {
	root := rootCommand()
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		var usage usageError
		if errors.As(err, &usage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "tuidrive",
		Short: "Drive a terminal program's real binary on a pty, with nobody at the keyboard",
		Long: `tuidrive runs a terminal program's real binary on a pty at a fixed size, presses
keys, waits on named text, and prints or saves the screen.

Every run starts the program from clean state: a built environment, with HOME and
every XDG directory under a scratch directory that is removed afterward. So a script
that shows a bug replays the same way every time, and a Go test can commit it.`,
		Example: `  tuidrive run open-row.keys -- ./inbox
  tuidrive run open-row.keys --out frames --env INBOX_DB=/tmp/inbox.db -- ./inbox`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       buildVersion(),
	}
	root.AddCommand(runCommand())
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.InitDefaultVersionFlag()
	if flag := root.Flags().Lookup("version"); flag != nil {
		flag.Shorthand = ""
	}
	return root
}

type runOptions struct {
	size    string
	env     []string
	dir     string
	out     string
	timeout time.Duration
}

func runCommand() *cobra.Command {
	var options runOptions
	cmd := &cobra.Command{
		Use:   "run <script> [flags] -- <program> [args...]",
		Short: "Replay a key script against a program, and print or save its frames",
		Long: `Replay a key script against a program started from clean state. Each frame the
script names prints to stdout. With --out it is also saved as NAME.txt, NAME.style
and NAME.png. A script that names no frame prints the screen it ended on, as "end".

A script of - reads from stdin. PATH is the only variable the program inherits, so
anything else it should read goes through --env. A failed step prints the screen it
was looking at and exits 1. A command line or script that cannot run exits 2.

` + keyscript.Syntax,
		Example: `  tuidrive run open-row.keys -- ./inbox
  tuidrive run open-row.keys --size 80x24 --out frames -- ./inbox --flag
  printf 'wait inbox\npress j enter\nwait kind:\nframe opened\n' | tuidrive run - -- ./inbox`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash != 1 || len(args) < 2 {
				return usageError{errors.New("run takes one script, then -- and the program: tuidrive run <script> -- <program> [args...]")}
			}
			return run(cmd.InOrStdin(), cmd.OutOrStdout(), args[0], args[1], args[2:], options)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.size, "size", fmt.Sprintf("%dx%d", ptyscreen.DefaultCols, ptyscreen.DefaultRows), "screen size as COLSxROWS")
	flags.StringArrayVar(&options.env, "env", nil, "NAME=VALUE to add to the program's environment, repeatable")
	flags.StringVar(&options.dir, "dir", "", "working directory for the program (default a scratch directory)")
	flags.StringVar(&options.out, "out", "", "directory to save each frame into as NAME.txt, NAME.style and NAME.png")
	flags.DurationVar(&options.timeout, "timeout", ptyscreen.DefaultWaitTimeout, "bound on each wait, until a script's timeout verb changes it")
	return cmd
}

func run(stdin io.Reader, stdout io.Writer, scriptPath, programPath string, programArgs []string, options runOptions) error {
	program, err := options.program(programPath, programArgs)
	if err != nil {
		return usageError{err}
	}
	script, err := parseScript(stdin, scriptPath)
	if err != nil {
		return usageError{err}
	}
	terminal, err := ptyscreen.Open(program)
	if err != nil {
		return usageError{err}
	}
	defer func() { _ = terminal.Close() }()

	started := time.Now()
	frames := 0
	sink := func(name string, frame ptyscreen.Frame) error {
		frames++
		return emitFrame(stdout, name, frame, time.Since(started), options.out)
	}
	if err := script.Run(terminal, sink); err != nil {
		return err
	}
	if frames == 0 {
		terminal.Settle()
		return sink("end", terminal.Frame())
	}
	return nil
}

func (o runOptions) program(path string, args []string) (ptyscreen.Program, error) {
	cols, rows, err := parseSize(o.size)
	if err != nil {
		return ptyscreen.Program{}, err
	}
	env := make(map[string]string, len(o.env))
	for _, pair := range o.env {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || name == "" {
			return ptyscreen.Program{}, fmt.Errorf("--env takes NAME=VALUE, and got %q", pair)
		}
		env[name] = value
	}
	return ptyscreen.Program{
		Path:        path,
		Args:        args,
		Env:         env,
		Dir:         o.dir,
		Cols:        cols,
		Rows:        rows,
		WaitTimeout: o.timeout,
	}, nil
}

func parseSize(size string) (cols, rows int, err error) {
	colsText, rowsText, ok := strings.Cut(size, "x")
	if ok {
		cols, err = strconv.Atoi(colsText)
		if err == nil {
			rows, err = strconv.Atoi(rowsText)
		}
	}
	if !ok || err != nil || cols < 1 || rows < 1 {
		return 0, 0, fmt.Errorf("--size takes COLSxROWS such as 100x30, and got %q", size)
	}
	return cols, rows, nil
}

func parseScript(stdin io.Reader, path string) (*keyscript.Script, error) {
	if path == "-" {
		return keyscript.Parse("stdin", stdin)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return keyscript.Parse(path, file)
}

func emitFrame(stdout io.Writer, name string, frame ptyscreen.Frame, elapsed time.Duration, out string) error {
	cols, rows := frame.Size()
	header := fmt.Sprintf("--- frame %s (%dx%d) at %s\n", name, cols, rows, elapsed.Round(time.Millisecond))
	if _, err := io.WriteString(stdout, header+frame.Text()); err != nil {
		return err
	}
	if out == "" {
		return nil
	}
	base := filepath.Join(out, name)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(base+".txt", []byte(frame.Text()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(base+".style", []byte(frame.Style()), 0o644); err != nil {
		return err
	}
	return frame.WritePNG(base + ".png")
}
