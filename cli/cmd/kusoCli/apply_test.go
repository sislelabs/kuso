package kusoCli

import "testing"

func TestReadProjectFromYAML(t *testing.T) {
	cases := map[string]string{
		"project: foo\n":                        "foo",
		"project: foo # note\n":                 "foo",
		"project: foo  # trailing comment\n":    "foo",
		"project: \"foo\" # q\n":                "foo",
		"project: 'foo' # q\n":                  "foo",
		"project: my#app\n":                     "my#app", // '#' not preceded by space = literal
		"project: 'a # b'\n":                    "a # b",  // '#' inside quotes is literal
		"baseDomain: x\nproject: bar\n":         "bar",
		"# project: commented\nproject: real\n": "real",
	}
	for in, want := range cases {
		if got := readProjectFromYAML([]byte(in)); got != want {
			t.Errorf("readProjectFromYAML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripInlineComment(t *testing.T) {
	cases := map[string]string{
		"foo":        "foo",
		"foo # bar":  "foo",
		"foo\t# bar": "foo",
		"my#app":     "my#app",
		"'a # b'":    "'a # b'",
		"\"a # b\"":  "\"a # b\"",
		"# whole":    "",
		"a 'b' # c":  "a 'b'",
	}
	for in, want := range cases {
		if got := stripInlineComment(in); got != want {
			t.Errorf("stripInlineComment(%q) = %q, want %q", in, got, want)
		}
	}
}
