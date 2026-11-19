package ptyscreen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// namedKeys uses the spellings a Bubble Tea program reads from a key message.
var namedKeys = map[string]rune{
	"backspace": uv.KeyBackspace,
	"delete":    uv.KeyDelete,
	"down":      uv.KeyDown,
	"end":       uv.KeyEnd,
	"enter":     uv.KeyEnter,
	"esc":       uv.KeyEscape,
	"f1":        uv.KeyF1,
	"f2":        uv.KeyF2,
	"f3":        uv.KeyF3,
	"f4":        uv.KeyF4,
	"f5":        uv.KeyF5,
	"f6":        uv.KeyF6,
	"f7":        uv.KeyF7,
	"f8":        uv.KeyF8,
	"f9":        uv.KeyF9,
	"f10":       uv.KeyF10,
	"f11":       uv.KeyF11,
	"f12":       uv.KeyF12,
	"home":      uv.KeyHome,
	"insert":    uv.KeyInsert,
	"left":      uv.KeyLeft,
	"pgdown":    uv.KeyPgDown,
	"pgup":      uv.KeyPgUp,
	"right":     uv.KeyRight,
	"space":     uv.KeySpace,
	"tab":       uv.KeyTab,
	"up":        uv.KeyUp,
}

// controlPunctuation is what ctrl+ accepts beside the letters.
const controlPunctuation = `[\]^_`

// KeyNames says what a key can be, for the command's help and a refused key name.
// It is a paragraph of its own within 80 columns, because the list of names alone
// is wider than a terminal. The names wrap, and the modifiers keep a line of their
// own so the punctuation ctrl+ takes is never split across two.
var KeyNames = ansi.Wordwrap("A key is one character; "+strings.Join(keyNameList(), ", ")+";", 80, "") +
	fmt.Sprintf("\nctrl+<letter or one of %s>, ctrl+space; alt+<any of these>; or shift+tab.",
		strings.Join(strings.Split(controlPunctuation, ""), " "))

// keyNameList is namedKeys in order, with the function keys read as one f1-fN
// range rather than sorted as text, where f10 would land before f2.
func keyNameList() []string {
	var names []string
	functionKeys := 0
	for name := range namedKeys {
		if number, ok := strings.CutPrefix(name, "f"); ok {
			if _, err := strconv.Atoi(number); err == nil {
				functionKeys++
				continue
			}
		}
		names = append(names, name)
	}
	names = append(names, fmt.Sprintf("f1-f%d", functionKeys))
	slices.Sort(names)
	return names
}

// ParseKey reads a key's name into the event the emulator encodes. It refuses a
// combination the emulator has no encoding for, because SendKey would send
// nothing and the program would never see the press.
func ParseKey(name string) (uv.KeyPressEvent, error) {
	key, ok := parseKey(name)
	if !ok {
		return uv.KeyPressEvent{}, fmt.Errorf("unknown key %q.\n%s", name, KeyNames)
	}
	return key, nil
}

func parseKey(name string) (uv.KeyPressEvent, bool) {
	if rest, ok := strings.CutPrefix(name, "alt+"); ok {
		key, ok := parseKey(rest)
		key.Mod |= uv.ModAlt
		return key, ok
	}
	if name == "shift+tab" {
		return uv.KeyPressEvent{Code: uv.KeyTab, Mod: uv.ModShift}, true
	}
	if rest, ok := strings.CutPrefix(name, "ctrl+"); ok {
		if rest == "space" {
			return uv.KeyPressEvent{Code: uv.KeySpace, Mod: uv.ModCtrl}, true
		}
		if code := []rune(rest); len(code) == 1 && hasControlCode(code[0]) {
			return uv.KeyPressEvent{Code: code[0], Mod: uv.ModCtrl}, true
		}
		return uv.KeyPressEvent{}, false
	}
	if code, ok := namedKeys[name]; ok {
		return uv.KeyPressEvent{Code: code}, true
	}
	if code := []rune(name); len(code) == 1 {
		return uv.KeyPressEvent{Code: code[0], Text: name}, true
	}
	return uv.KeyPressEvent{}, false
}

func hasControlCode(code rune) bool {
	return (code >= 'a' && code <= 'z') || strings.ContainsRune(controlPunctuation, code)
}
