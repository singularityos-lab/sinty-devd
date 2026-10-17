package rules

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

var longNames = map[string]byte{
	"devnode": 'N', "tempnode": 'N', "attr": 's', "sysfs": 's', "env": 'E', "kernel": 'k',
	"number": 'n', "driver": 'd', "devpath": 'p', "id": 'b', "major": 'M', "minor": 'm',
	"result": 'c', "parent": 'P', "name": 'D', "links": 'L', "root": 'r', "sys": 'S',
}

func (st *state) format(s string) string {
	if !strings.ContainsAny(s, "%$") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '%' && c != '$' {
			b.WriteByte(c)
			continue
		}
		if i+1 < len(s) && s[i+1] == c {
			b.WriteByte(c)
			i++
			continue
		}
		var code byte
		if c == '$' {
			j := i + 1
			for j < len(s) && (s[j] >= 'a' && s[j] <= 'z') {
				j++
			}
			name := s[i+1 : j]
			found, ok := longNames[name]
			if !ok {
				b.WriteByte(c)
				continue
			}
			code = found
			i = j - 1
		} else {
			if i+1 >= len(s) {
				b.WriteByte(c)
				continue
			}
			code = s[i+1]
			i++
		}
		arg := ""
		if i+1 < len(s) && s[i+1] == '{' {
			if end := strings.IndexByte(s[i+1:], '}'); end > 0 {
				arg = s[i+2 : i+1+end]
				i += end + 1
			}
		}
		value := st.expand(code, arg)
		if st.replaceWS {
			value = strings.Join(strings.Fields(value), "_")
		}
		b.WriteString(value)
	}
	return b.String()
}

func (st *state) expand(code byte, arg string) string {
	d := st.dev
	switch code {
	case 'k':
		return d.SysName
	case 'n':
		return d.SysNum()
	case 'd':
		return d.Driver
	case 'p':
		return d.DevPath
	case 'b':
		for _, p := range d.Ancestors() {
			if p != d && p.Subsystem != "" {
				return p.SysName
			}
		}
		return ""
	case 'M':
		if d.Major < 0 {
			return ""
		}
		return strconv.Itoa(d.Major)
	case 'm':
		if d.Minor < 0 {
			return ""
		}
		return strconv.Itoa(d.Minor)
	case 'c':
		if arg == "" {
			return st.result
		}
		fields := strings.Fields(st.result)
		plus := strings.HasSuffix(arg, "+")
		n, err := strconv.Atoi(strings.TrimSuffix(arg, "+"))
		if err != nil || n < 1 || n > len(fields) {
			return ""
		}
		if plus {
			return strings.Join(fields[n-1:], " ")
		}
		return fields[n-1]
	case 'P':
		if p := d.Parent(); p != nil && p.DevNode != "" {
			return strings.TrimPrefix(p.DevNode, "/dev/")
		}
		return ""
	case 'N':
		return d.DevNode
	case 'D':
		if st.out.Name != "" {
			return st.out.Name
		}
		return strings.TrimPrefix(d.DevNode, "/dev/")
	case 'L':
		var links []string
		for _, l := range d.SortedLinks() {
			links = append(links, strings.TrimPrefix(l, "/dev/"))
		}
		return strings.Join(links, " ")
	case 'r':
		return "/dev"
	case 'S':
		return d.Sys
	case 'E':
		return d.Props[arg]
	case 's':
		if arg == "" {
			return ""
		}
		if v, ok := d.Attr(arg); ok {
			return v
		}
		for _, p := range d.Ancestors() {
			if v, ok := p.Attr(arg); ok {
				return v
			}
		}
		return ""
	}
	return fmt.Sprintf("%%%c", code)
}

// Glob matches value against a udev pattern: shell wildcards with "|"
// separating alternatives.
func Glob(pattern, value string) bool {
	if !strings.ContainsAny(pattern, "*?[|") {
		return pattern == value
	}
	for _, alt := range strings.Split(pattern, "|") {
		if ok, err := filepath.Match(alt, value); err == nil && ok {
			return true
		}
		if strings.Contains(alt, "/") || strings.Contains(value, "/") {
			if globSlash(alt, value) {
				return true
			}
		}
	}
	return false
}

func globSlash(pattern, value string) bool {
	p := strings.ReplaceAll(pattern, "/", "\x00")
	v := strings.ReplaceAll(value, "/", "\x00")
	ok, err := filepath.Match(p, v)
	return err == nil && ok
}
