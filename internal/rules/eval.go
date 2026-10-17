package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

// Host is what rule evaluation needs from the daemon: builtins and programs.
type Host interface {
	// Builtin runs a builtin such as input_id or path_id. When test is true it
	// only reports whether the builtin exists and would succeed.
	Builtin(name string, args []string, d *device.Device) (map[string]string, error)
	// Program runs an external program and returns its standard output.
	Program(command string, d *device.Device) (string, error)
}

// Outcome holds what the rules decided for one event.
type Outcome struct {
	Name         string
	Owner        string
	Group        string
	Mode         os.FileMode
	ModeSet      bool
	Run          []string
	RunBuiltin   []string
	StaticNodes  []string
	LinkPriority int
	DBPersist    bool
	Watch        bool
	Errors       []string
	finals       map[string]bool
}

type state struct {
	dev       *device.Device
	host      Host
	out       *Outcome
	result    string
	escape    int
	replaceWS bool
}

// Apply evaluates every rule against the device, updating its properties,
// tags and links and returning the remaining decisions.
func (s *Set) Apply(d *device.Device, host Host) *Outcome {
	st := &state{dev: d, host: host, out: &Outcome{finals: map[string]bool{}}}
	for i := 0; i < len(s.Rules); i++ {
		r := s.Rules[i]
		if st.matches(r) && r.gotoAt >= 0 {
			i = r.gotoAt - 1
		}
	}
	return st.out
}

func (st *state) matches(r *Rule) bool {
	parentChecked := false
	for _, t := range r.Tokens {
		switch t.Key {
		case "KERNELS", "SUBSYSTEMS", "DRIVERS", "ATTRS", "TAGS":
			if parentChecked {
				continue
			}
			parentChecked = true
			if st.matchParent(r) == nil {
				return false
			}
			continue
		}
		if !t.Op.IsMatch() {
			continue
		}
		if !st.match(t) {
			return false
		}
	}
	for _, t := range r.Tokens {
		if t.Op.IsMatch() {
			continue
		}
		st.assign(r, t)
	}
	return true
}

func (st *state) matchParent(r *Rule) *device.Device {
	for _, p := range st.dev.Ancestors() {
		ok := true
		for _, t := range r.Tokens {
			var value string
			var exists bool
			switch t.Key {
			case "KERNELS":
				value, exists = p.SysName, true
			case "SUBSYSTEMS":
				value, exists = p.Subsystem, true
			case "DRIVERS":
				value, exists = p.Driver, true
			case "ATTRS":
				value, exists = p.Attr(t.Attr)
			case "TAGS":
				exists = p.Tags[st.format(t.Value)]
				if (t.Op == OpMatch) != exists {
					ok = false
				}
				continue
			default:
				continue
			}
			if !exists {
				if t.Op == OpMatch {
					ok = false
				}
				continue
			}
			if !compare(t.Op, value, st.format(t.Value)) {
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok {
			return p
		}
	}
	return nil
}

func compare(op Op, value, pattern string) bool {
	m := Glob(pattern, value)
	if op == OpNoMatch {
		return !m
	}
	return m
}

func (st *state) match(t Token) bool {
	d := st.dev
	switch t.Key {
	case "ACTION":
		return compare(t.Op, d.Action, st.format(t.Value))
	case "DEVPATH":
		return compare(t.Op, d.DevPath, st.format(t.Value))
	case "KERNEL":
		return compare(t.Op, d.SysName, st.format(t.Value))
	case "SUBSYSTEM":
		return compare(t.Op, d.Subsystem, st.format(t.Value))
	case "DRIVER":
		return compare(t.Op, d.Driver, st.format(t.Value))
	case "NAME":
		return compare(t.Op, st.out.Name, st.format(t.Value))
	case "SYMLINK":
		want := st.format(t.Value)
		found := false
		for l := range d.Links {
			if Glob(want, strings.TrimPrefix(l, "/dev/")) {
				found = true
				break
			}
		}
		return found == (t.Op == OpMatch)
	case "ENV":
		value, ok := d.Props[t.Attr]
		if !ok {
			return (t.Op == OpNoMatch) == (st.format(t.Value) != "")
		}
		return compare(t.Op, value, st.format(t.Value))
	case "CONST":
		value := ""
		switch t.Attr {
		case "arch":
			value = "x86-64"
		case "virt":
			value = virt()
		}
		return compare(t.Op, value, st.format(t.Value))
	case "TAG":
		return d.Tags[st.format(t.Value)] == (t.Op == OpMatch)
	case "ATTR", "SYSCTL":
		var value string
		var ok bool
		if t.Key == "SYSCTL" {
			data, err := os.ReadFile(filepath.Join("/proc/sys", strings.ReplaceAll(t.Attr, ".", "/")))
			value, ok = strings.TrimSpace(string(data)), err == nil
		} else {
			value, ok = d.Attr(st.format(t.Attr))
		}
		if !ok {
			return t.Op == OpNoMatch
		}
		return compare(t.Op, value, st.format(t.Value))
	case "TEST":
		path := st.format(t.Value)
		if !strings.HasPrefix(path, "/") {
			path = filepath.Join(d.SysPath(), path)
		}
		info, err := os.Stat(path)
		exists := err == nil
		if exists && t.Attr != "" {
			mode, perr := strconv.ParseUint(t.Attr, 8, 32)
			if perr == nil {
				exists = uint64(info.Mode().Perm())&mode != 0
			}
		}
		return exists == (t.Op == OpMatch)
	case "PROGRAM":
		out, err := st.host.Program(st.format(t.Value), d)
		if err != nil {
			return t.Op == OpNoMatch
		}
		st.result = strings.TrimRight(out, "\n")
		return t.Op == OpMatch
	case "RESULT":
		return compare(t.Op, st.result, st.format(t.Value))
	case "IMPORT":
		return st.importValues(t) == (t.Op == OpMatch)
	}
	return true
}

func (st *state) importValues(t Token) bool {
	d := st.dev
	value := st.format(t.Value)
	switch t.Attr {
	case "builtin":
		fields := SplitArgs(value)
		if len(fields) == 0 {
			return false
		}
		props, err := st.host.Builtin(fields[0], fields[1:], d)
		if err != nil {
			return false
		}
		for k, v := range props {
			d.Props[k] = v
		}
		return true
	case "program":
		out, err := st.host.Program(value, d)
		if err != nil {
			return false
		}
		importLines(d, out)
		return true
	case "file":
		data, err := os.ReadFile(value)
		if err != nil {
			return false
		}
		importLines(d, string(data))
		return true
	case "db":
		return importDB(d, value)
	case "parent":
		p := d.Parent()
		if p == nil {
			return false
		}
		loadDB(p)
		found := false
		for k, v := range p.Props {
			if Glob(value, k) {
				d.Props[k] = v
				found = true
			}
		}
		return found
	case "cmdline":
		data, err := os.ReadFile("/proc/cmdline")
		if err != nil {
			return false
		}
		for _, field := range strings.Fields(string(data)) {
			k, v, has := strings.Cut(field, "=")
			if k == value {
				if !has {
					v = "1"
				}
				d.Props[value] = v
				return true
			}
		}
		return false
	}
	return false
}

// SplitArgs splits a command line on whitespace, keeping quoted words whole.
func SplitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inWord = true
		case c == ' ' || c == '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

func importLines(d *device.Device, text string) {
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || k == "" {
			continue
		}
		d.Props[k] = strings.Trim(v, `"'`)
	}
}

// DBDir is where the database of previous events lives.
var DBDir = "/run/udev/data"

func importDB(d *device.Device, key string) bool {
	data, err := os.ReadFile(filepath.Join(DBDir, d.ID()))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "E:") {
			k, v, ok := strings.Cut(line[2:], "=")
			if ok && k == key {
				d.Props[k] = v
				return true
			}
		}
	}
	return false
}

func loadDB(d *device.Device) {
	data, err := os.ReadFile(filepath.Join(DBDir, d.ID()))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "E:"):
			if k, v, ok := strings.Cut(line[2:], "="); ok {
				if _, exists := d.Props[k]; !exists {
					d.Props[k] = v
				}
			}
		case strings.HasPrefix(line, "G:"):
			d.Tags[line[2:]] = true
		}
	}
}

func (st *state) final(key string, op Op) bool {
	if st.out.finals[key] {
		return true
	}
	if op == OpAssignFinal {
		st.out.finals[key] = true
	}
	return false
}

func (st *state) assign(r *Rule, t Token) {
	d := st.dev
	switch t.Key {
	case "NAME":
		if st.final("NAME", t.Op) {
			return
		}
		st.out.Name = st.format(t.Value)
	case "SYMLINK":
		if st.final("SYMLINK", t.Op) {
			return
		}
		if t.Op == OpAssign || t.Op == OpAssignFinal {
			d.Links = map[string]int{}
		}
		st.replaceWS = st.escape != escapeNone
		formatted := st.format(t.Value)
		st.replaceWS = false
		for _, l := range strings.Fields(formatted) {
			l = escapePath(l)
			if !strings.HasPrefix(l, "/dev/") {
				l = "/dev/" + l
			}
			if t.Op == OpRemove {
				delete(d.Links, l)
			} else {
				d.Links[l] = st.out.LinkPriority
			}
		}
	case "OWNER":
		if !st.final("OWNER", t.Op) {
			st.out.Owner = st.format(t.Value)
		}
	case "GROUP":
		if !st.final("GROUP", t.Op) {
			st.out.Group = st.format(t.Value)
		}
	case "MODE":
		if st.final("MODE", t.Op) {
			return
		}
		if m, err := strconv.ParseUint(st.format(t.Value), 8, 32); err == nil {
			st.out.Mode = os.FileMode(m)
			st.out.ModeSet = true
		}
	case "ENV":
		value := st.format(t.Value)
		switch t.Op {
		case OpAdd:
			if old := d.Props[t.Attr]; old != "" {
				value = old + " " + value
			}
		}
		if value == "" {
			delete(d.Props, t.Attr)
		} else {
			d.Props[t.Attr] = value
		}
	case "TAG":
		tag := st.format(t.Value)
		switch t.Op {
		case OpRemove:
			delete(d.Tags, tag)
		case OpAssign, OpAssignFinal:
			d.Tags = map[string]bool{tag: true}
		default:
			d.Tags[tag] = true
		}
	case "ATTR":
		if err := d.WriteAttr(st.format(t.Attr), st.format(t.Value)); err != nil {
			st.out.Errors = append(st.out.Errors, fmt.Sprintf("%s:%d: ATTR{%s}: %v", r.File, r.Line, t.Attr, err))
		}
	case "SYSCTL":
		path := filepath.Join("/proc/sys", strings.ReplaceAll(st.format(t.Attr), ".", "/"))
		if err := os.WriteFile(path, []byte(st.format(t.Value)), 0); err != nil {
			st.out.Errors = append(st.out.Errors, fmt.Sprintf("%s:%d: SYSCTL{%s}: %v", r.File, r.Line, t.Attr, err))
		}
	case "RUN":
		if st.final("RUN", t.Op) {
			return
		}
		if t.Op == OpAssign || t.Op == OpAssignFinal {
			st.out.Run, st.out.RunBuiltin = nil, nil
		}
		if t.Attr == "builtin" {
			st.out.RunBuiltin = append(st.out.RunBuiltin, st.format(t.Value))
		} else {
			st.out.Run = append(st.out.Run, st.format(t.Value))
		}
	case "IMPORT":
		st.importValues(t)
	case "OPTIONS":
		for _, opt := range strings.Split(st.format(t.Value), ",") {
			opt = strings.TrimSpace(opt)
			switch {
			case strings.HasPrefix(opt, "link_priority="):
				if v, err := strconv.Atoi(opt[len("link_priority="):]); err == nil {
					st.out.LinkPriority = v
				}
			case strings.HasPrefix(opt, "static_node="):
				st.out.StaticNodes = append(st.out.StaticNodes, opt[len("static_node="):])
			case opt == "string_escape=replace":
				st.escape = escapeReplace
			case opt == "string_escape=none":
				st.escape = escapeNone
			case opt == "db_persist":
				st.out.DBPersist = true
			case opt == "watch":
				st.out.Watch = true
			case opt == "nowatch":
				st.out.Watch = false
			}
		}
	}
}

const (
	escapeUnset = iota
	escapeReplace
	escapeNone
)

func escapePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			fmt.Fprintf(&b, "\\x%02x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func virt() string {
	if data, err := os.ReadFile("/sys/class/dmi/id/sys_vendor"); err == nil {
		v := strings.ToLower(string(data))
		for _, name := range []string{"qemu", "kvm", "vmware", "virtualbox", "innotek", "microsoft", "xen"} {
			if strings.Contains(v, name) {
				return "vm"
			}
		}
	}
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil && strings.Contains(string(data), " hypervisor") {
		return "vm"
	}
	return "none"
}
