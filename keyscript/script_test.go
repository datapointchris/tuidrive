package keyscript

import (
	"strings"
	"testing"
)

func TestParseRefusesWhatCouldNeverRun(t *testing.T) {
	cases := map[string]struct{ line, want string }{
		"unknown verb":    {"sleep 1s", `unknown verb "sleep"`},
		"unknown key":     {"press j nope", `unknown key "nope"`},
		"key with no way": {"press ctrl+up", `unknown key "ctrl+up"`},
		"wait on nothing": {"wait", "names the text"},
		"bad pattern":     {"wait /(/", "bad pattern"},
		"open pattern":    {"gone /abc", "no closing /"},
		"frame path":      {"frame ../escape", "frame needs a name"},
		"mask needs one":  {"mask abc def", "expected a /pattern/"},
		"zero timeout":    {"timeout 0s", "above zero"},
		"exit word":       {"exits soon", "exits needs a code"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("flow.keys", strings.NewReader("# a comment\n\n"+tc.line+"\n"))
			if err == nil {
				t.Fatalf("%q parsed", tc.line)
			}
			if !strings.Contains(err.Error(), "flow.keys:3: ") {
				t.Errorf("error lacks its line number: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error lacks %q: %v", tc.want, err)
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
