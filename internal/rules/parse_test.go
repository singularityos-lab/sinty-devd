package rules

import (
	"os"
	"strings"
	"testing"
)

func TestParseLine(t *testing.T) {
	rules, err := Parse(strings.NewReader(`# comment
SUBSYSTEM=="input", KERNEL=="event*", ATTRS{name}=="Foo \"Bar\"", ENV{ID_X}="1", \
  TAG+="uaccess", GOTO="end"
LABEL="end"
`), "t.rules")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules", len(rules))
	}
	r := rules[0]
	if len(r.Tokens) != 6 || r.Tokens[2].Attr != "name" || r.Tokens[2].Value != `Foo "Bar"` || r.Tokens[4].Op != OpAdd {
		t.Fatalf("bad tokens %+v", r.Tokens)
	}
	if r.Goto != "end" || rules[1].Label != "end" || r.Line != 2 {
		t.Fatalf("bad goto/label/line %+v %+v", r, rules[1])
	}
}

func TestParseRejectsBroken(t *testing.T) {
	rules, err := Parse(strings.NewReader("KERNEL==\"a\"\nKERNEL==unquoted\n"), "t.rules")
	if err == nil || len(rules) != 1 {
		t.Fatalf("want one rule and an error, got %d %v", len(rules), err)
	}
}

func TestLoadImageRules(t *testing.T) {
	dir := os.Getenv("SINTY_DEVD_RULES")
	if dir == "" {
		t.Skip("set SINTY_DEVD_RULES to a rules.d directory")
	}
	set, err := LoadDirs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Rules) < 100 {
		t.Fatalf("only %d rules", len(set.Rules))
	}
	for _, r := range set.Rules {
		if r.Goto != "" && r.gotoAt < 0 {
			t.Errorf("%s:%d: GOTO %q has no label", r.File, r.Line, r.Goto)
		}
	}
}
