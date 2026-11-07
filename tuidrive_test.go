package tuidrive

import (
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDriveRealBinary(t *testing.T) {
	term := Start(t, Program{Path: Build(t, "./internal/sampletui")})

	term.WaitFor("sort newest")
	term.Mask(`\d+s ago`, "<age>")
	term.Golden("list")

	picture := filepath.Join(t.TempDir(), "list.png")
	term.WritePNG(picture)
	assertImageShowsYellow(t, picture)

	term.Press("j", "j", "enter")
	term.WaitFor("kind: link")
	term.Golden("opened")

	term.Press("esc")
	term.WaitFor("sort newest")
	term.Press("q")
	if code := term.WaitExit(); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func assertImageShowsYellow(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	img, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Dx(); got != 1000 {
		t.Errorf("image width = %d, want 1000 for 100 columns", got)
	}
	yellow := color.RGBA{0xfc, 0xe9, 0x4f, 0xff}
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			if color.RGBAModel.Convert(img.At(x, y)) == yellow {
				return
			}
		}
	}
	t.Error("the image holds no pixel of palette color 11, which the sample draws for the state new")
}

func TestRunScriptComparesEachFrame(t *testing.T) {
	term := Start(t, Program{Path: Build(t, "./internal/sampletui")})
	term.RunScript("testdata/open-row.keys")
}

func TestEnvironmentIsBuiltNotInherited(t *testing.T) {
	t.Setenv("STRAY", "leaked")
	// Wide enough that a long temporary directory never wraps the scratch path.
	term := Start(t, Program{
		Path: Build(t, "./internal/sampletui"),
		Env:  map[string]string{"CHOSEN": "yes"},
		Cols: 400,
	})
	term.WaitFor("sort newest")
	term.Press("e")
	term.WaitFor("chosen=yes")

	screen := term.Frame().Text()
	if strings.Contains(screen, "leaked") {
		t.Errorf("a variable the caller did not name reached the program:\n%s", screen)
	}
	if want := "state=" + filepath.Join(term.ScratchDir(), "state"); !strings.Contains(screen, want) {
		t.Errorf("screen lacks %q:\n%s", want, screen)
	}
}

func TestGoldenUpdateAndDiffer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "frame.txt")

	if err := compareGoldenFile(path, "one\n", false); err == nil {
		t.Error("a missing golden passed")
	}

	if err := compareGoldenFile(path, "one\ntwo\n", true); err != nil {
		t.Fatal(err)
	}
	if err := compareGoldenFile(path, "one\ntwo\n", false); err != nil {
		t.Errorf("an unchanged screen failed: %v", err)
	}

	err := compareGoldenFile(path, "one\nTWO\n", false)
	if err == nil {
		t.Fatal("a changed row passed")
	}
	for _, want := range []string{"2 want: two", "2 got:  TWO"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "want: one") {
		t.Errorf("failure lists a row that did not change:\n%v", err)
	}
}
