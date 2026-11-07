// tuidrive replays a key script against a terminal program's real binary on a pty,
// and prints or saves the frames it names.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/datapointchris/goclikit"
	"github.com/datapointchris/goselfupdate"
	"github.com/datapointchris/goselfupdate/autoupdate"
	"github.com/spf13/cobra"

	"github.com/datapointchris/tuidrive/keyscript"
	"github.com/datapointchris/tuidrive/ptyscreen"
)

// version is set through -ldflags by goreleaser.
var version = "dev"

// buildVersion falls back to the module version, because go install applies no
// ldflags but does stamp a tagged build.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && goselfupdate.IsReleaseVersion(info.Main.Version) {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return version
}

// updateConfig is shared by the update command and the update check, so the two
// cannot name different releases.
func updateConfig() goselfupdate.Config {
	return goselfupdate.Config{
		Owner:   "datapointchris",
		Repo:    "tuidrive",
		Binary:  "tuidrive",
		Version: buildVersion(),
	}
}

func main() {
	err := execute(rootCommand(), autoupdate.Config{Update: updateConfig()})
	os.Exit(exitCode(err, os.Stderr))
}

// execute takes the update check's config, so a test runs the shipped path with
// the check suppressed.
func execute(root *cobra.Command, config autoupdate.Config) error {
	return goclikit.Execute(context.Background(), root, config)
}

func exitCode(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	if !errors.Is(err, goclikit.ErrReported) {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
	}
	var interrupted interruptedError
	if errors.As(err, &interrupted) {
		return 128 + int(interrupted.signal)
	}
	if errors.Is(err, goclikit.ErrUsage) {
		return 2
	}
	return 1
}

// interruptedError is a run a signal ended, which exits with the shell's code for it.
type interruptedError struct{ signal syscall.Signal }

func (e interruptedError) Error() string { return "interrupted by " + e.signal.String() }

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
	root.SetVersionTemplate("tuidrive {{.Version}}\n")
	// Registered here because cobra's own version flag takes -v, which is for verbosity.
	root.Flags().Bool("version", false, "print the version")
	root.AddCommand(runCommand(), goclikit.UpdateCommand(updateConfig()))
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
script names prints to stdout, and its time since the start prints to stderr. With
--out it is also saved as NAME.txt, NAME.style and NAME.png. A script that names no
frame prints the screen it ended on, as "end".

A script of - reads from stdin. PATH is the only variable the program inherits, so
anything else it should read goes through --env. A step the program failed prints the
screen it was looking at and exits 1, and so does a program that ended with a code
other than 0 where no exits line named it. A command line or script that cannot run
exits 2.

` + keyscript.Syntax,
		Example: `  tuidrive run open-row.keys -- ./inbox
  tuidrive run open-row.keys --size 80x24 --out frames -- ./inbox --flag
  printf 'wait inbox\npress j enter\nwait kind:\nframe opened\n' | tuidrive run - -- ./inbox`,
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() != 1 || len(args) < 2 {
				return goclikit.UsageError(errors.New("run takes one script, then -- and the program: tuidrive run <script> -- <program> [args...]"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, args[0], args[1], args[2:], options)
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

func run(cmd *cobra.Command, scriptPath, programPath string, programArgs []string, options runOptions) (err error) {
	program, err := options.program(programPath, programArgs)
	if err != nil {
		return goclikit.UsageError(err)
	}
	script, err := parseScript(cmd.InOrStdin(), scriptPath)
	if err != nil {
		return goclikit.UsageError(err)
	}
	if err := program.Check(); err != nil {
		return goclikit.UsageError(err)
	}
	if options.out != "" {
		if err := os.MkdirAll(options.out, 0o755); err != nil {
			return goclikit.UsageError(fmt.Errorf("--out: %w", err))
		}
	}

	// Caught, because the scratch directory only goes away if Close runs.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)

	terminal, err := ptyscreen.Open(program)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, terminal.Close()) }()

	replayed := make(chan error, 1)
	go func() { replayed <- replay(cmd, script, terminal, options.out) }()
	select {
	case err := <-replayed:
		return err
	case received := <-interrupts:
		caught, _ := received.(syscall.Signal)
		return interruptedError{signal: caught}
	}
}

func replay(cmd *cobra.Command, script *keyscript.Script, terminal *ptyscreen.Terminal, out string) error {
	started := time.Now()
	frames := 0
	sink := func(name string, frame ptyscreen.Frame) error {
		frames++
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "frame %s at %s\n", name, time.Since(started).Round(time.Millisecond))
		return emitFrame(cmd.OutOrStdout(), name, frame, out)
	}
	if err := script.Run(terminal, sink); err != nil {
		return err
	}
	if frames == 0 {
		return sink("end", terminal.SettledFrame())
	}
	return nil
}

func (o runOptions) program(path string, args []string) (ptyscreen.Program, error) {
	cols, rows, err := parseSize(o.size)
	if err != nil {
		return ptyscreen.Program{}, err
	}
	if err := ptyscreen.CheckWaitTimeout(o.timeout); err != nil {
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

func emitFrame(stdout io.Writer, name string, frame ptyscreen.Frame, out string) error {
	cols, rows := frame.Size()
	header := fmt.Sprintf("--- frame %s (%dx%d)\n", name, cols, rows)
	if _, err := io.WriteString(stdout, header+frame.Text()); err != nil {
		return err
	}
	if out == "" {
		return nil
	}
	base := filepath.Join(out, name)
	if err := os.WriteFile(base+".txt", []byte(frame.Text()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(base+".style", []byte(frame.Style()), 0o644); err != nil {
		return err
	}
	return frame.WritePNG(base + ".png")
}
