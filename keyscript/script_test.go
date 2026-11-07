package keyscript

import (
	"strings"
	"testing"
)

func TestParseRefusesWhatCouldNeverRun(t *testing.T) {
	cases := map[string]string{
		"unknown verb":    "sleep 1s",
		"unknown key":     "press j nope",
		"key with no way": "press ctrl+up",
		"wait on nothing": "wait",
		"bad pattern":     "wait /(/",
		"open pattern":    "gone /abc",
		"frame path":      "frame ../escape",
		"mask needs one":  "mask abc def",
		"zero timeout":    "timeout 0s",
		"exit word":       "exits soon",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("flow.keys", strings.NewReader("# a comment\n\n"+line+"\n"))
			if err == nil {
				t.Fatalf("%q parsed", line)
			}
			if !strings.Contains(err.Error(), "flow.keys:3: ") {
				t.Errorf("error lacks its line number: %v", err)
			}
		})
	}
}

func TestParseReportsEveryBadLineAtOnce(t *testing.T) {
	_, err := Parse("flow.keys", strings.NewReader("press nope\nwait ok\nframe ../x\n"))
	if err == nil {
		t.Fatal("a script with two bad lines parsed")
	}
	for _, want := range []string{"flow.keys:1: ", "flow.keys:3: "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}
