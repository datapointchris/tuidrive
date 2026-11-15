package ptyscreen

import "testing"

func TestFaceDrawsAnIconDejaVuLacksFromTheSymbolsFont(t *testing.T) {
	faces, err := loadFaces()
	if err != nil {
		t.Fatal(err)
	}
	symbols, err := loadSymbolFaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		icon rune
		want int
	}{
		{0xf0001, 0}, // a Material Design icon
		{0xf015, 1},  // a Font Awesome icon
	} {
		got, err := faces.face(string(tc.icon), 0)
		if err != nil {
			t.Fatal(err)
		}
		if got != symbols[tc.want].face {
			t.Errorf("face(%U) is not symbols font %d", tc.icon, tc.want)
		}
	}
	if got, _ := faces.face("a", 0); got != faces.regular {
		t.Errorf("face(%q) is not DejaVu's regular face", "a")
	}
}
