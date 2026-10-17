// Package rules reads udev rules files and evaluates them against devices.
package rules

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Op is the operator of a rule token.
type Op int

const (
	OpMatch Op = iota
	OpNoMatch
	OpAssign
	OpAdd
	OpRemove
	OpAssignFinal
)

func (o Op) String() string {
	return [...]string{"==", "!=", "=", "+=", "-=", ":="}[o]
}

// IsMatch reports whether the operator compares instead of assigning.
func (o Op) IsMatch() bool { return o == OpMatch || o == OpNoMatch }

// Token is one KEY{attr}<op>"value" element of a rule line.
type Token struct {
	Key   string
	Attr  string
	Op    Op
	Value string
}

// Rule is one line of a rules file.
type Rule struct {
	File   string
	Line   int
	Tokens []Token
	Label  string
	Goto   string
	gotoAt int
}

// Set is the ordered list of rules from every rules file.
type Set struct {
	Rules []*Rule
}

var dirs = []string{"/usr/lib/udev/rules.d", "/lib/udev/rules.d", "/etc/udev/rules.d", "/run/udev/rules.d"}

// DefaultDirs returns the directories rules are read from, lowest priority first.
func DefaultDirs() []string { return append([]string(nil), dirs...) }

// LoadDirs reads every *.rules file from dirs. A file in a later directory
// replaces a file with the same name in an earlier one, and files are applied
// in lexical order of their names, as udev does.
func LoadDirs(roots ...string) (*Set, error) {
	byName := map[string]string{}
	for _, dir := range roots {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".rules") {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if real, err := filepath.EvalSymlinks(full); err == nil && real == "/dev/null" {
				delete(byName, e.Name())
				byName[e.Name()] = ""
				continue
			}
			byName[e.Name()] = full
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	set := &Set{}
	for _, name := range names {
		path := byName[name]
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		rules, err := Parse(f, path)
		f.Close()
		if err != nil {
			return nil, err
		}
		set.Rules = append(set.Rules, rules...)
	}
	set.resolve()
	return set, nil
}

// Parse reads the rules of one file. Lines that fail to parse are reported in
// the error but do not stop the rest of the file from loading, matching udev,
// which logs and skips them.
func Parse(r io.Reader, name string) ([]*Rule, error) {
	var out []*Rule
	var bad []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	var pending strings.Builder
	start := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if pending.Len() == 0 {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			start = lineNo
		}
		if strings.HasSuffix(trimmed, "\\") {
			pending.WriteString(strings.TrimSuffix(trimmed, "\\"))
			pending.WriteByte(' ')
			continue
		}
		pending.WriteString(trimmed)
		text := pending.String()
		pending.Reset()
		rule, err := parseLine(text)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s:%d: %v", name, start, err))
			continue
		}
		if rule == nil {
			continue
		}
		rule.File = name
		rule.Line = start
		out = append(out, rule)
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	if len(bad) > 0 {
		return out, &ParseError{Lines: bad}
	}
	return out, nil
}

// ParseError lists the lines that were skipped.
type ParseError struct{ Lines []string }

func (e *ParseError) Error() string { return strings.Join(e.Lines, "; ") }

func parseLine(text string) (*Rule, error) {
	rule := &Rule{}
	i := 0
	for {
		for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == ',') {
			i++
		}
		if i >= len(text) || text[i] == '#' {
			break
		}
		keyStart := i
		for i < len(text) && (isKeyChar(text[i])) {
			i++
		}
		key := text[keyStart:i]
		if key == "" {
			return nil, fmt.Errorf("expected a key at %q", text[keyStart:])
		}
		attr := ""
		if i < len(text) && text[i] == '{' {
			end := strings.IndexByte(text[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unterminated attribute in %s", key)
			}
			attr = text[i+1 : i+end]
			i += end + 1
		}
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		var op Op
		switch {
		case strings.HasPrefix(text[i:], "=="):
			op, i = OpMatch, i+2
		case strings.HasPrefix(text[i:], "!="):
			op, i = OpNoMatch, i+2
		case strings.HasPrefix(text[i:], "+="):
			op, i = OpAdd, i+2
		case strings.HasPrefix(text[i:], "-="):
			op, i = OpRemove, i+2
		case strings.HasPrefix(text[i:], ":="):
			op, i = OpAssignFinal, i+2
		case strings.HasPrefix(text[i:], "="):
			op, i = OpAssign, i+1
		default:
			return nil, fmt.Errorf("missing operator after %s", key)
		}
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		if i >= len(text) || text[i] != '"' {
			return nil, fmt.Errorf("value of %s is not quoted", key)
		}
		i++
		var value strings.Builder
		closed := false
		for i < len(text) {
			c := text[i]
			if c == '\\' && i+1 < len(text) && (text[i+1] == '"' || text[i+1] == '\\') {
				value.WriteByte(text[i+1])
				i += 2
				continue
			}
			if c == '"' {
				closed = true
				i++
				break
			}
			value.WriteByte(c)
			i++
		}
		if !closed {
			return nil, fmt.Errorf("unterminated value of %s", key)
		}
		tok := Token{Key: key, Attr: attr, Op: op, Value: value.String()}
		switch key {
		case "LABEL":
			rule.Label = tok.Value
		case "GOTO":
			rule.Goto = tok.Value
		}
		rule.Tokens = append(rule.Tokens, tok)
	}
	if len(rule.Tokens) == 0 {
		return nil, nil
	}
	return rule, nil
}

func isKeyChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
}

// resolve links every GOTO to the index of its LABEL in the same file.
func (s *Set) resolve() {
	for i, r := range s.Rules {
		r.gotoAt = -1
		if r.Goto == "" {
			continue
		}
		for j := i + 1; j < len(s.Rules); j++ {
			if s.Rules[j].File != r.File {
				break
			}
			if s.Rules[j].Label == r.Goto {
				r.gotoAt = j
				break
			}
		}
	}
}
