// Package keyscript reads and replays a key script: lines of keys, waits and
// frames that run against a program started from clean state. The same script
// runs from the command line and from a Go test, so an exploration is already the
// regression test.
package keyscript

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/datapointchris/tuidrive/ptyscreen"
)

// Syntax is the script grammar as the command's help and a parse failure print it.
const Syntax = `A script is one verb per line. A line starting with # is a comment.

  press KEY...          send keys: j enter esc tab up ctrl+c shift+tab space
  type TEXT             send text to a text field
  wait MATCH            wait for MATCH to be on the screen
  gone MATCH            wait for MATCH to be off the screen
  frame NAME            read the settled screen as a frame called NAME
  mask /REGEX/ TOKEN    replace each match with TOKEN in every later frame
  timeout DURATION      bound the waits that follow, such as 30s
  exits [CODE]          wait for the program to exit with CODE, 0 by default

MATCH is bare text, "quoted text" or /a regular expression/. Text matches within
one row. A pattern reads the whole screen as one line per row, so (?m)^ and $ anchor
a row. TEXT and TOKEN are bare or quoted. A line ends with its text, so # in the
middle of a line is part of it.`

// Script is a parsed key script.
type Script struct {
	name  string
	steps []step
}

// FrameSink receives each frame a script names. The frame reads through the
// script's masks.
type FrameSink func(name string, frame ptyscreen.Frame) error

// step is one line of a script. It runs against the replay in progress.
type step interface {
	run(replay *replay) error
}

type replay struct {
	terminal *ptyscreen.Terminal
	sink     FrameSink
	masks    []ptyscreen.Mask
}

// Parse reads a script. It refuses the whole script on any line it cannot read,
// because a typo found after the program has started costs a replay. The name
// prefixes every error.
func Parse(name string, source io.Reader) (*Script, error) {
	script := &Script{name: name}
	scanner := bufio.NewScanner(source)
	var problems []error
	for number := 1; scanner.Scan(); number++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parsed, err := parseLine(line)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s:%d: %w", name, number, err))
			continue
		}
		script.steps = append(script.steps, numberedStep{number: number, line: line, step: parsed})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return script, nil
}

// Run replays the script against a program already started.
func (s *Script) Run(terminal *ptyscreen.Terminal, sink FrameSink) error {
	state := &replay{terminal: terminal, sink: sink}
	for _, current := range s.steps {
		if err := current.run(state); err != nil {
			return fmt.Errorf("%s:%w", s.name, err)
		}
	}
	return nil
}

// numberedStep names the line in a failure.
type numberedStep struct {
	number int
	line   string
	step   step
}

func (n numberedStep) run(state *replay) error {
	if err := n.step.run(state); err != nil {
		return fmt.Errorf("%d: %s: %w", n.number, n.line, err)
	}
	return nil
}

func parseLine(line string) (step, error) {
	verb, argument, _ := strings.Cut(line, " ")
	argument = strings.TrimSpace(argument)
	switch verb {
	case "press":
		return parsePress(argument)
	case "type":
		return parseType(argument)
	case "wait":
		return parseWait(argument, false)
	case "gone":
		return parseWait(argument, true)
	case "frame":
		return parseFrame(argument)
	case "mask":
		return parseMask(argument)
	case "timeout":
		return parseTimeout(argument)
	case "exits":
		return parseExits(argument)
	default:
		return nil, fmt.Errorf("unknown verb %q.\n%s", verb, Syntax)
	}
}

type pressStep struct{ keys []string }

func parsePress(argument string) (step, error) {
	keys := strings.Fields(argument)
	if len(keys) == 0 {
		return nil, errors.New("press needs at least one key")
	}
	for _, key := range keys {
		if _, err := ptyscreen.ParseKey(key); err != nil {
			return nil, err
		}
	}
	return pressStep{keys: keys}, nil
}

func (s pressStep) run(state *replay) error { return state.terminal.Press(s.keys...) }

type typeStep struct{ text string }

func parseType(argument string) (step, error) {
	text, err := parseText(argument)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, errors.New("type needs text")
	}
	return typeStep{text: text}, nil
}

func (s typeStep) run(state *replay) error { return state.terminal.Type(s.text) }

type waitStep struct {
	text    string
	pattern *regexp.Regexp
	gone    bool
}

func parseWait(argument string, gone bool) (step, error) {
	if argument == "" {
		return nil, errors.New("a wait names the text or /pattern/ it expects")
	}
	if strings.HasPrefix(argument, "/") {
		pattern, rest, err := parsePattern(argument)
		if err != nil {
			return nil, err
		}
		if rest != "" {
			return nil, fmt.Errorf("unexpected %q after the pattern", rest)
		}
		return waitStep{pattern: pattern, gone: gone}, nil
	}
	text, err := parseText(argument)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, errors.New("a wait names the text or /pattern/ it expects")
	}
	return waitStep{text: text, gone: gone}, nil
}

func (s waitStep) run(state *replay) error {
	switch {
	case s.pattern != nil && s.gone:
		return state.terminal.WaitGonePattern(s.pattern)
	case s.pattern != nil:
		return state.terminal.WaitForPattern(s.pattern)
	case s.gone:
		return state.terminal.WaitGone(s.text)
	default:
		return state.terminal.WaitFor(s.text)
	}
}

type frameStep struct{ name string }

var frameName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func parseFrame(argument string) (step, error) {
	if !frameName.MatchString(argument) {
		return nil, fmt.Errorf("frame needs a name of letters, digits, dots, dashes and underscores, and got %q", argument)
	}
	return frameStep{name: argument}, nil
}

func (s frameStep) run(state *replay) error {
	state.terminal.Settle()
	return state.sink(s.name, state.terminal.Frame().Masked(state.masks...))
}

type maskStep struct{ mask ptyscreen.Mask }

func parseMask(argument string) (step, error) {
	pattern, rest, err := parsePattern(argument)
	if err != nil {
		return nil, err
	}
	token, err := parseText(rest)
	if err != nil {
		return nil, err
	}
	return maskStep{mask: ptyscreen.Mask{Pattern: pattern, Token: token}}, nil
}

func (s maskStep) run(state *replay) error {
	state.masks = append(state.masks, s.mask)
	return nil
}

type timeoutStep struct{ timeout time.Duration }

func parseTimeout(argument string) (step, error) {
	timeout, err := time.ParseDuration(argument)
	if err != nil {
		return nil, fmt.Errorf("timeout needs a duration such as 30s: %w", err)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("timeout needs a duration above zero, and got %s", timeout)
	}
	return timeoutStep{timeout: timeout}, nil
}

func (s timeoutStep) run(state *replay) error {
	state.terminal.SetWaitTimeout(s.timeout)
	return nil
}

type exitsStep struct{ code int }

func parseExits(argument string) (step, error) {
	if argument == "" {
		return exitsStep{}, nil
	}
	code, err := strconv.Atoi(argument)
	if err != nil {
		return nil, fmt.Errorf("exits needs a code: %w", err)
	}
	return exitsStep{code: code}, nil
}

func (s exitsStep) run(state *replay) error {
	code, err := state.terminal.WaitExit()
	if err != nil {
		return err
	}
	if code != s.code {
		return fmt.Errorf("the program exited with code %d", code)
	}
	return nil
}

// parseText reads bare or quoted text. Quotes keep leading and trailing spaces.
func parseText(argument string) (string, error) {
	if !strings.HasPrefix(argument, `"`) {
		return argument, nil
	}
	text, err := strconv.Unquote(argument)
	if err != nil {
		return "", fmt.Errorf("bad quoted text %s: %w", argument, err)
	}
	return text, nil
}

// parsePattern reads a /pattern/ from the front of argument, and returns what
// follows it. A slash inside the pattern is written \/, which a regular expression
// reads as a plain slash.
func parsePattern(argument string) (*regexp.Regexp, string, error) {
	if !strings.HasPrefix(argument, "/") {
		return nil, "", fmt.Errorf("expected a /pattern/, and got %q", argument)
	}
	end := -1
	for i := 1; i < len(argument); i++ {
		if argument[i] == '\\' {
			i++
			continue
		}
		if argument[i] == '/' {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, "", fmt.Errorf("pattern %q has no closing /", argument)
	}
	pattern, err := regexp.Compile(argument[1:end])
	if err != nil {
		return nil, "", fmt.Errorf("bad pattern: %w", err)
	}
	return pattern, strings.TrimSpace(argument[end+1:]), nil
}
