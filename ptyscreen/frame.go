package ptyscreen

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Frame is the screen at one moment. It stays valid after the program ends.
type Frame struct {
	cols, rows int
	// shown holds what Text and Style read, which Masked rewrites.
	shown [][]frameCell
	// drawn holds what the screen held, which Image reads.
	drawn         [][]frameCell
	cursorRow     int
	cursorCol     int
	cursorVisible bool
}

type frameCell struct {
	text  string
	width int
	style uv.Style
}

// captureFrame copies the emulator's cells. The caller holds mu, or is the
// emulator's own callback.
func (t *Terminal) captureFrame() Frame {
	cols, rows := t.emulator.Width(), t.emulator.Height()
	cells := make([][]frameCell, rows)
	for y := range rows {
		row := make([]frameCell, 0, cols)
		for x := 0; x < cols; {
			width := 1
			text := " "
			var style uv.Style
			if cell := t.emulator.CellAt(x, y); cell != nil {
				style = cell.Style
				if cell.Content != "" {
					text = cell.Content
				}
				if cell.Width > 1 {
					width = cell.Width
				}
			}
			row = append(row, frameCell{text: text, width: width, style: style})
			x += width
		}
		cells[y] = row
	}
	cursor := t.emulator.CursorPosition()
	return Frame{
		cols:          cols,
		rows:          rows,
		shown:         cells,
		drawn:         cells,
		cursorRow:     cursor.Y,
		cursorCol:     cursor.X,
		cursorVisible: !t.emulator.CursorHidden(),
	}
}

// Size returns the screen's columns and rows.
func (f Frame) Size() (cols, rows int) { return f.cols, f.rows }

// Text returns the screen with masks applied: one line per row, each trimmed of
// trailing spaces, with the blank rows at the bottom left off.
func (f Frame) Text() string { return linesText(f.shown) }

// rawText is what a wait reads, because a mask must not hide what it waits for.
func (f Frame) rawText() string { return linesText(f.drawn) }

func linesText(cells [][]frameCell) string {
	lines := make([]string, len(cells))
	last := -1
	for y, row := range cells {
		lines[y] = rowText(row)
		if lines[y] != "" {
			last = y
		}
	}
	if last < 0 {
		return ""
	}
	return strings.Join(lines[:last+1], "\n") + "\n"
}

func rowText(row []frameCell) string {
	var b strings.Builder
	for _, cell := range row {
		b.WriteString(cell.text)
	}
	return strings.TrimRight(b.String(), " ")
}

// boxed returns the drawn screen between rules, for a failure message.
func (f Frame) boxed() string {
	rule := strings.Repeat("-", f.cols)
	return rule + "\n" + f.rawText() + rule + "\n"
}

// Style returns the screen's styled runs with masks applied, one per line, as
// "row: first-last attributes", such as "3: 12-14 fg=11 bold". Rows and columns
// count from 1. A color prints as its palette index where it has one, and as
// #rrggbb where it does not. Nothing paints a space's foreground, weight or slant,
// so they are left out, and a plain space neither ends a run nor starts one. Two
// renderers that differ only in how they reach a blank produce the same runs, and
// a color change on a title of three words diffs as one line.
func (f Frame) Style() string {
	var b strings.Builder
	for y, row := range f.shown {
		col := 1
		runStart, runEnd := 0, 0
		runStyle := ""
		flush := func() {
			if runStyle != "" {
				fmt.Fprintf(&b, "%d: %d-%d %s\n", y+1, runStart, runEnd, runStyle)
			}
		}
		for _, cell := range row {
			style := visibleStyle(cell)
			if style != "" || !isBlank(cell) {
				if style != runStyle {
					flush()
					runStart = col
					runStyle = style
				}
				runEnd = col + cell.width - 1
			}
			col += cell.width
		}
		flush()
	}
	return b.String()
}

func isBlank(cell frameCell) bool {
	return strings.TrimSpace(cell.text) == ""
}

func visibleStyle(cell frameCell) string {
	style := cell.style
	reversed := style.Attrs&uv.AttrReverse != 0
	blank := isBlank(cell)
	var parts []string
	if style.Fg != nil && (!blank || reversed) {
		parts = append(parts, "fg="+colorName(style.Fg))
	}
	if style.Bg != nil {
		parts = append(parts, "bg="+colorName(style.Bg))
	}
	if !blank {
		parts = appendAttr(parts, style, uv.AttrBold, "bold")
		parts = appendAttr(parts, style, uv.AttrFaint, "faint")
		parts = appendAttr(parts, style, uv.AttrItalic, "italic")
		parts = appendAttr(parts, style, uv.AttrBlink, "blink")
		parts = appendAttr(parts, style, uv.AttrRapidBlink, "rapid-blink")
		parts = appendAttr(parts, style, uv.AttrConceal, "conceal")
	}
	parts = appendAttr(parts, style, uv.AttrReverse, "reverse")
	parts = appendAttr(parts, style, uv.AttrStrikethrough, "strike")
	if style.Underline != uv.UnderlineNone {
		parts = append(parts, underlineName(style.Underline))
		if style.UnderlineColor != nil {
			parts = append(parts, "underline-color="+colorName(style.UnderlineColor))
		}
	}
	return strings.Join(parts, " ")
}

func appendAttr(parts []string, style uv.Style, attr uint8, name string) []string {
	if style.Attrs&attr != 0 {
		return append(parts, name)
	}
	return parts
}

func underlineName(underline uv.Underline) string {
	switch underline {
	case uv.UnderlineSingle:
		return "underline"
	case uv.UnderlineDouble:
		return "underline=double"
	case uv.UnderlineCurly:
		return "underline=curly"
	case uv.UnderlineDotted:
		return "underline=dotted"
	case uv.UnderlineDashed:
		return "underline=dashed"
	case uv.UnderlineNone:
		return ""
	default:
		return fmt.Sprintf("underline=%d", underline)
	}
}

func colorName(c color.Color) string {
	switch c := c.(type) {
	case ansi.BasicColor:
		return fmt.Sprint(uint8(c))
	case ansi.IndexedColor:
		return fmt.Sprint(uint8(c))
	default:
		r, g, b, _ := c.RGBA()
		return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	}
}

// Mask replaces each match of Pattern on a row with Token before Text and Style
// read the frame. Token needs no particular width, so a match that changes width
// between runs, such as "59m ago" and "1h ago", leaves the row the same. A
// pattern that takes the padding after it keeps the rest of the row in place too.
type Mask struct {
	Pattern *regexp.Regexp
	Token   string
}

// Masked returns a frame whose Text and Style read through the masks. Its Image
// still shows what the screen held, and a wait never sees a mask.
func (f Frame) Masked(masks ...Mask) Frame {
	if len(masks) == 0 {
		return f
	}
	shown := make([][]frameCell, len(f.shown))
	for y, row := range f.shown {
		for _, mask := range masks {
			row = maskRow(row, mask)
		}
		shown[y] = row
	}
	f.shown = shown
	return f
}

func maskRow(row []frameCell, mask Mask) []frameCell {
	text := rowText(row)
	matches := mask.Pattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return row
	}
	token := make([]frameCell, 0, len(mask.Token))
	for _, r := range mask.Token {
		token = append(token, frameCell{text: string(r), width: 1})
	}
	masked := make([]frameCell, 0, len(row))
	offset := 0
	next := 0
	for _, cell := range row {
		for next < len(matches) && offset >= matches[next][1] {
			next++
		}
		if next < len(matches) && offset >= matches[next][0] {
			if offset == matches[next][0] {
				masked = append(masked, token...)
			}
		} else {
			masked = append(masked, cell)
		}
		offset += len(cell.text)
	}
	return masked
}
