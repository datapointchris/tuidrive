package ptyscreen

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/go-fonts/dejavu/dejavusansmono"
	"github.com/go-fonts/dejavu/dejavusansmonobold"
	"github.com/go-fonts/dejavu/dejavusansmonoboldoblique"
	"github.com/go-fonts/dejavu/dejavusansmonooblique"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// The cell box fits DejaVu Sans Mono at 16 pixels, whose advance is 9.6 and whose
// ascent and descent total 18.6, so box-drawing lines meet across cells.
const (
	cellWidth   = 10
	cellHeight  = 19
	baseline    = 15
	fontSize    = 16
	underlineAt = 17
	strikeAt    = 10
)

// The default colors and the 16 base colors of the Tango palette, which reads
// well on a dark background. An image has to pick one, because a frame holds
// palette indexes and the program's real terminal decides what they look like.
var (
	defaultForeground = color.RGBA{0xd3, 0xd7, 0xcf, 0xff}
	defaultBackground = color.RGBA{0x1c, 0x1c, 0x1c, 0xff}
	basePalette       = [16]color.RGBA{
		{0x2e, 0x34, 0x36, 0xff},
		{0xcc, 0x00, 0x00, 0xff},
		{0x4e, 0x9a, 0x06, 0xff},
		{0xc4, 0xa0, 0x00, 0xff},
		{0x34, 0x65, 0xa4, 0xff},
		{0x75, 0x50, 0x7b, 0xff},
		{0x06, 0x98, 0x9a, 0xff},
		{0xd3, 0xd7, 0xcf, 0xff},
		{0x55, 0x57, 0x53, 0xff},
		{0xef, 0x29, 0x29, 0xff},
		{0x8a, 0xe2, 0x34, 0xff},
		{0xfc, 0xe9, 0x4f, 0xff},
		{0x72, 0x9f, 0xcf, 0xff},
		{0xad, 0x7f, 0xa8, 0xff},
		{0x34, 0xe2, 0xe2, 0xff},
		{0xee, 0xee, 0xec, 0xff},
	}
)

type faceSet struct {
	regular, bold, oblique, boldOblique font.Face
}

var loadFaces = sync.OnceValues(func() (faceSet, error) {
	var faces faceSet
	for ttf, face := range map[*[]byte]*font.Face{
		&dejavusansmono.TTF:            &faces.regular,
		&dejavusansmonobold.TTF:        &faces.bold,
		&dejavusansmonooblique.TTF:     &faces.oblique,
		&dejavusansmonoboldoblique.TTF: &faces.boldOblique,
	} {
		parsed, err := opentype.Parse(*ttf)
		if err != nil {
			return faceSet{}, fmt.Errorf("parse font: %w", err)
		}
		*face, err = opentype.NewFace(parsed, &opentype.FaceOptions{Size: fontSize, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return faceSet{}, fmt.Errorf("load font: %w", err)
		}
	}
	return faces, nil
})

// Image draws the screen as it stood, colors included, whatever masks the frame
// carries.
func (f Frame) Image() (image.Image, error) {
	faces, err := loadFaces()
	if err != nil {
		return nil, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, f.cols*cellWidth, f.rows*cellHeight))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(defaultBackground), image.Point{}, draw.Src)
	for y, row := range f.drawn {
		col := 0
		for _, cell := range row {
			isCursor := f.cursorVisible && y == f.cursorRow && col == f.cursorCol
			drawCell(canvas, faces, col, y, cell, isCursor)
			col += cell.width
		}
	}
	return canvas, nil
}

// WritePNG writes the screen as a PNG, creating the file's directory if it must.
func (f Frame) WritePNG(path string) error {
	img, err := f.Image()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func drawCell(canvas *image.RGBA, faces faceSet, col, row int, cell frameCell, isCursor bool) {
	style := cell.style
	fg := resolveColor(style.Fg, defaultForeground)
	bg := resolveColor(style.Bg, defaultBackground)
	if style.Attrs&uv.AttrReverse != 0 {
		fg, bg = bg, fg
	}
	if isCursor {
		fg, bg = bg, fg
	}
	if style.Attrs&uv.AttrFaint != 0 {
		fg = blend(fg, bg)
	}

	left, top := col*cellWidth, row*cellHeight
	box := image.Rect(left, top, left+cell.width*cellWidth, top+cellHeight)
	if bg != defaultBackground {
		draw.Draw(canvas, box, image.NewUniform(bg), image.Point{}, draw.Src)
	}
	if style.Attrs&uv.AttrConceal == 0 && cell.text != " " {
		drawer := font.Drawer{
			Dst:  canvas,
			Src:  image.NewUniform(fg),
			Face: chooseFace(faces, style.Attrs),
			Dot:  fixed.P(left, top+baseline),
		}
		drawer.DrawString(cell.text)
	}
	if style.Underline != uv.UnderlineNone {
		line := resolveColor(style.UnderlineColor, fg)
		draw.Draw(canvas, image.Rect(box.Min.X, top+underlineAt, box.Max.X, top+underlineAt+1), image.NewUniform(line), image.Point{}, draw.Src)
	}
	if style.Attrs&uv.AttrStrikethrough != 0 {
		draw.Draw(canvas, image.Rect(box.Min.X, top+strikeAt, box.Max.X, top+strikeAt+1), image.NewUniform(fg), image.Point{}, draw.Src)
	}
}

func chooseFace(faces faceSet, attrs uint8) font.Face {
	bold := attrs&uv.AttrBold != 0
	italic := attrs&uv.AttrItalic != 0
	switch {
	case bold && italic:
		return faces.boldOblique
	case bold:
		return faces.bold
	case italic:
		return faces.oblique
	default:
		return faces.regular
	}
}

func resolveColor(c color.Color, fallback color.RGBA) color.RGBA {
	switch c := c.(type) {
	case nil:
		return fallback
	case ansi.BasicColor:
		return basePalette[int(c)&0xf]
	case ansi.IndexedColor:
		if c < 16 {
			return basePalette[c]
		}
	}
	r, g, b, _ := c.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 0xff}
}

func blend(a, b color.RGBA) color.RGBA {
	return color.RGBA{
		uint8((uint16(a.R) + uint16(b.R)) / 2),
		uint8((uint16(a.G) + uint16(b.G)) / 2),
		uint8((uint16(a.B) + uint16(b.B)) / 2),
		0xff,
	}
}
