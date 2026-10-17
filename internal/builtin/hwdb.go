package builtin

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/singularityos-lab/sinty-devd/internal/device"
)

// HWDB is a read-only view of the compiled hardware database (hwdb.bin).
type HWDB struct {
	data      []byte
	nodeSize  uint64
	childSize uint64
	valueSize uint64
	root      uint64
}

// OpenHWDB loads a compiled hwdb.bin.
func OpenHWDB(path string) (*HWDB, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 80 || string(data[:8]) != "KSLPHHRH" {
		return nil, fmt.Errorf("%s is not a hardware database", path)
	}
	le := binary.LittleEndian
	db := &HWDB{
		data:      data,
		nodeSize:  le.Uint64(data[32:]),
		childSize: le.Uint64(data[40:]),
		valueSize: le.Uint64(data[48:]),
		root:      le.Uint64(data[56:]),
	}
	if db.nodeSize < 24 || db.childSize < 16 || db.valueSize < 16 || db.root >= uint64(len(data)) {
		return nil, fmt.Errorf("%s has an unexpected layout", path)
	}
	return db, nil
}

type node struct {
	prefix   uint64
	children int
	values   uint64
	off      uint64
}

func (db *HWDB) node(off uint64) (node, bool) {
	if off+24 > uint64(len(db.data)) {
		return node{}, false
	}
	le := binary.LittleEndian
	return node{
		prefix:   le.Uint64(db.data[off:]),
		children: int(db.data[off+8]),
		values:   le.Uint64(db.data[off+16:]),
		off:      off,
	}, true
}

func (db *HWDB) str(off uint64) string {
	if off >= uint64(len(db.data)) {
		return ""
	}
	end := bytes.IndexByte(db.data[off:], 0)
	if end < 0 {
		return ""
	}
	return string(db.data[off : off+uint64(end)])
}

func (db *HWDB) child(n node, i int) (byte, uint64) {
	off := n.off + db.nodeSize + uint64(i)*db.childSize
	if off+16 > uint64(len(db.data)) {
		return 0, 0
	}
	return db.data[off], binary.LittleEndian.Uint64(db.data[off+8:])
}

func (db *HWDB) lookupChild(n node, c byte) (node, bool) {
	for i := 0; i < n.children; i++ {
		cc, off := db.child(n, i)
		if cc == c {
			return db.node(off)
		}
	}
	return node{}, false
}

func (db *HWDB) addValues(n node, out map[string]string) {
	base := n.off + db.nodeSize + uint64(n.children)*db.childSize
	for i := uint64(0); i < n.values; i++ {
		off := base + i*db.valueSize
		if off+16 > uint64(len(db.data)) {
			return
		}
		key := db.str(binary.LittleEndian.Uint64(db.data[off:]))
		value := db.str(binary.LittleEndian.Uint64(db.data[off+8:]))
		if !strings.HasPrefix(key, " ") {
			continue
		}
		out[key[1:]] = value
	}
}

// Lookup returns every property whose pattern matches the modalias.
func (db *HWDB) Lookup(modalias string) map[string]string {
	out := map[string]string{}
	n, ok := db.node(db.root)
	i := 0
	for ok {
		if n.prefix != 0 {
			prefix := db.str(n.prefix)
			for p := 0; p < len(prefix); p++ {
				c := prefix[p]
				if c == '*' || c == '?' || c == '[' {
					db.fnmatch(n, p, "", safeSuffix(modalias, i+p), out)
					return out
				}
				if i+p >= len(modalias) || c != modalias[i+p] {
					return out
				}
			}
			i += len(prefix)
		}
		for _, wild := range []byte{'*', '?', '['} {
			if child, found := db.lookupChild(n, wild); found {
				db.fnmatch(child, 0, string(wild), safeSuffix(modalias, i), out)
			}
		}
		if i >= len(modalias) {
			db.addValues(n, out)
			return out
		}
		n, ok = db.lookupChild(n, modalias[i])
		i++
	}
	return out
}

func safeSuffix(s string, i int) string {
	if i >= len(s) {
		return ""
	}
	return s[i:]
}

func (db *HWDB) fnmatch(n node, p int, buf, search string, out map[string]string) {
	buf += safeSuffix(db.str(n.prefix), p)
	for i := 0; i < n.children; i++ {
		c, off := db.child(n, i)
		if child, ok := db.node(off); ok {
			db.fnmatch(child, 0, buf+string(c), search, out)
		}
	}
	if n.values > 0 {
		if ok, err := filepath.Match(globNoSlash(buf), globNoSlash(search)); err == nil && ok {
			db.addValues(n, out)
		}
	}
}

func globNoSlash(s string) string {
	return strings.ReplaceAll(s, "/", "\x00")
}

func modaliasOf(d *device.Device, subsystem string) string {
	for _, p := range d.Ancestors() {
		if subsystem != "" && p.Subsystem != subsystem {
			continue
		}
		if m, ok := p.Attr("modalias"); ok && m != "" {
			return m
		}
		if m := p.Props["MODALIAS"]; m != "" {
			return m
		}
		if subsystem != "" {
			return ""
		}
	}
	return ""
}

func (r *Registry) hwdb(args []string, d *device.Device) (map[string]string, error) {
	if r.HWDB == nil {
		return nil, fmt.Errorf("no hardware database")
	}
	subsystem, prefix, explicit := "", "", ""
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--subsystem="):
			subsystem = strings.TrimPrefix(a, "--subsystem=")
		case strings.HasPrefix(a, "--lookup-prefix="):
			prefix = strings.TrimPrefix(a, "--lookup-prefix=")
		case strings.HasPrefix(a, "-s"):
			subsystem = strings.TrimPrefix(a, "-s")
		case strings.HasPrefix(a, "-p"):
			prefix = strings.TrimPrefix(a, "-p")
		default:
			explicit = strings.Trim(a, `'"`)
		}
	}
	key := explicit
	if key == "" {
		m := modaliasOf(d, subsystem)
		if m == "" {
			return nil, fmt.Errorf("no modalias")
		}
		key = prefix + m
	}
	props := r.HWDB.Lookup(key)
	if len(props) == 0 {
		return nil, fmt.Errorf("no match")
	}
	return props, nil
}
