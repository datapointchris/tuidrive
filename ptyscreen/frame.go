package ptyscreen

import (
	"bytes"
	"fmt"
	"image/color"
	"regexp"
	"slices"
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
	var f Frame
	t.captureInto(&f)
	return f
}

// captureInto copies the emulator's cells into f, writing over f's own rows
// wherever the screen's size lets it, so a frame captured on every synchronized
// update allocates only when the screen is resized. Nothing else may hold f's
// rows, so a frame filled this way is handed out only through clone. The caller
// holds mu, or is the emulator's own callback.
func (t *Terminal) captureInto(f *Frame) {
	cols, rows := t.emulator.Width(), t.emulator.Height()
	cells := f.drawn
	if len(cells) != rows {
		cells = make([][]frameCell, rows)
	}
	for y := range rows {
		row := cells[y][:0]
		if cap(row) < cols {
			row = make([]frameCell, 0, cols)
		}
		for x := 0; x < cols; {
			cell := t.cellAt(x, y)
			row = append(row, cell)
			x += cell.width
		}
		cells[y] = row
	}
	cursor := t.emulator.CursorPosition()
	*f = Frame{
		cols:          cols,
		rows:          rows,
		shown:         cells,
		drawn:         cells,
		cursorRow:     cursor.Y,
		cursorCol:     cursor.X,
		cursorVisible: !t.emulator.CursorHidden(),
	}
}

// clone returns a copy of a frame captureInto filled, sharing no cells with it,
// so the next capture into the original leaves the copy as it was.
func (f Frame) clone() Frame {
	cells := make([][]frameCell, len(f.drawn))
	for y, row := range f.drawn {
		cells[y] = slices.Clone(row)
	}
	f.shown = cells
	f.drawn = cells
	return f
}

// cellAt reads one emulator cell, as a space of width 1 where the emulator holds
// none. The caller holds mu, or is the emulator's own callback.
func (t *Terminal) cellAt(x, y int) frameCell {
	cell := frameCell{text: " ", width: 1}
	if c := t.emulator.CellAt(x, y); c != nil {
		cell.style = c.Style
		if c.Content != "" {
			cell.text = c.Content
		}
		if c.Width > 1 {
			cell.width = c.Width
		}
	}
	return cell
}

// screenText is what currentFrame().rawText() returns, read straight from the
// emulator. A wait reads it on every output, and needs the text alone, so it
// skips building a frame of cells only to join them. The caller holds mu.
func (t *Terminal) screenText() string {
	if t.synchronizing {
		return t.lastFinished.rawText()
	}
	cols, rows := t.emulator.Width(), t.emulator.Height()
	var screen strings.Builder
	var line []byte
	blankRows := 0
	for y := range rows {
		line = line[:0]
		for x := 0; x < cols; {
			cell := t.cellAt(x, y)
			line = append(line, cell.text...)
			x += cell.width
		}
		line = bytes.TrimRight(line, " ")
		if len(line) == 0 {
			blankRows++
			continue
		}
		for ; blankRows > 0; blankRows-- {
			screen.WriteByte('\n')
		}
		screen.Write(line)
		screen.WriteByte('\n')
	}
	return screen.String()
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

// Report returns the drawn screen between rules with its size, as a failure prints
// it. It ignores masks, so a failure shows what the program really drew.
func (f Frame) Report() string {
	rule := strings.Repeat("-", f.cols)
	return fmt.Sprintf("screen (%dx%d):\n%s\n%s%s\n", f.cols, f.rows, rule, f.rawText(), rule)
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

// masked returns a frame whose Text and Style read through the masks. Its Image
// still shows what the screen held, and a wait never sees a mask.
func (f Frame) masked(masks []Mask) Frame {
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
