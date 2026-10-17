// Package daemon processes device events: rules, device nodes, the database
// and the broadcast to libudev clients.
package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/singularityos-lab/sinty-devd/internal/builtin"
	"github.com/singularityos-lab/sinty-devd/internal/db"
	"github.com/singularityos-lab/sinty-devd/internal/device"
	"github.com/singularityos-lab/sinty-devd/internal/rules"
	"github.com/singularityos-lab/sinty-devd/internal/uevent"
)

// Config holds the paths the daemon works with.
type Config struct {
	Sys       string
	Dev       string
	RulesDirs []string
	HWDB      string
	HelperDir string
	Timeout   time.Duration
	NoRun     bool
}

// DefaultConfig returns the paths of a normal system.
func DefaultConfig() Config {
	hwdb := "/etc/udev/hwdb.bin"
	if _, err := os.Stat(hwdb); err != nil {
		hwdb = "/usr/lib/udev/hwdb.bin"
	}
	return Config{
		Sys:       "/sys",
		Dev:       "/dev",
		RulesDirs: rules.DefaultDirs(),
		HWDB:      hwdb,
		HelperDir: "/usr/lib/udev",
		Timeout:   30 * time.Second,
	}
}

// Daemon is the event processor.
type Daemon struct {
	cfg      Config
	rules    *rules.Set
	builtins *builtin.Registry
	sender   *uevent.Socket
	queue    chan map[string]string
	mu       sync.Mutex
	pending  int
	lastSeq  uint64
	idle     *sync.Cond
	groups   map[string]int
	users    map[string]int
}

// New loads rules and the hardware database.
func New(cfg Config) (*Daemon, error) {
	set, err := rules.LoadDirs(cfg.RulesDirs...)
	if err != nil {
		if _, partial := err.(*rules.ParseError); !partial {
			return nil, err
		}
		log.Printf("some rules were skipped: %v", err)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	d := &Daemon{
		cfg:      cfg,
		rules:    set,
		builtins: builtin.New(cfg.HWDB),
		queue:    make(chan map[string]string, 4096),
		groups:   map[string]int{},
		users:    map[string]int{},
	}
	d.idle = sync.NewCond(&d.mu)
	return d, nil
}

// Reload rereads the rules.
func (d *Daemon) Reload() error {
	set, err := rules.LoadDirs(d.cfg.RulesDirs...)
	if set == nil {
		return err
	}
	d.mu.Lock()
	d.rules = set
	d.mu.Unlock()
	return err
}

// Builtin implements rules.Host.
func (d *Daemon) Builtin(name string, args []string, dev *device.Device) (map[string]string, error) {
	return d.builtins.Run(name, args, dev)
}

// Program implements rules.Host: it runs a helper with the device properties
// in its environment and returns its standard output.
func (d *Daemon) Program(command string, dev *device.Device) (string, error) {
	args := rules.SplitArgs(command)
	if len(args) == 0 {
		return "", fmt.Errorf("empty command")
	}
	if !strings.HasPrefix(args[0], "/") {
		args[0] = filepath.Join(d.cfg.HelperDir, args[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = environ(dev)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

func environ(dev *device.Device) []string {
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	for k, v := range dev.Props {
		env = append(env, k+"="+v)
	}
	return env
}

// Enqueue adds a kernel event to the queue.
func (d *Daemon) Enqueue(env map[string]string) {
	d.mu.Lock()
	d.pending++
	d.mu.Unlock()
	d.queue <- env
}

// Run processes queued events until the context ends.
func (d *Daemon) Run(ctx context.Context) error {
	sender, err := uevent.Sender()
	if err != nil {
		return err
	}
	d.sender = sender
	for {
		select {
		case <-ctx.Done():
			return nil
		case env := <-d.queue:
			if err := d.Process(env); err != nil {
				log.Printf("%s: %v", uevent.String(env), err)
			}
			d.mu.Lock()
			d.pending--
			if s, err := strconv.ParseUint(env["SEQNUM"], 10, 64); err == nil && s > d.lastSeq {
				d.lastSeq = s
			}
			if d.pending == 0 {
				d.idle.Broadcast()
			}
			d.mu.Unlock()
		}
	}
}

// Settle waits until every queued event and every event the kernel has
// announced so far is processed, or the timeout passes.
func (d *Daemon) Settle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		busy := d.pending > 0 || (d.lastSeq > 0 && d.lastSeq < kernelSeqnum(d.cfg.Sys))
		d.mu.Unlock()
		if !busy {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func kernelSeqnum(sys string) uint64 {
	data, err := os.ReadFile(filepath.Join(sys, "kernel/uevent_seqnum"))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return n
}

// Process handles one event from start to finish.
func (d *Daemon) Process(env map[string]string) error {
	dev, err := device.FromUevent(d.cfg.Sys, env)
	if err != nil {
		return err
	}
	if dev.Action == "remove" {
		return d.remove(dev)
	}
	if dev.Action == "change" || dev.Action == "move" || dev.Action == "bind" {
		if props, tags, _, _, ok := db.Read(dev.ID()); ok {
			for k, v := range props {
				if _, exists := dev.Props[k]; !exists && strings.HasPrefix(k, "ID_") {
					dev.Props[k] = v
				}
			}
			for _, t := range tags {
				dev.Tags[t] = true
			}
		}
	}
	d.mu.Lock()
	set := d.rules
	d.mu.Unlock()
	out := set.Apply(dev, d)
	for _, e := range out.Errors {
		log.Print(e)
	}
	if dev.Subsystem == "net" && out.Name != "" && out.Name != dev.SysName && dev.Action == "add" {
		if err := renameInterface(dev.SysName, out.Name); err != nil {
			log.Printf("renaming %s to %s: %v", dev.SysName, out.Name, err)
		} else {
			dev.Props["INTERFACE"] = out.Name
			dev.Props["INTERFACE_OLD"] = dev.SysName
			dev.DevPath = filepath.Join(filepath.Dir(dev.DevPath), out.Name)
			dev.SysName = out.Name
			dev.Props["DEVPATH"] = dev.DevPath
		}
	}
	if dev.DevNode != "" && d.cfg.Dev != "" {
		d.applyNode(dev, out)
	}
	for _, b := range out.RunBuiltin {
		args := rules.SplitArgs(b)
		if len(args) > 0 {
			if props, err := d.builtins.Run(args[0], args[1:], dev); err == nil {
				for k, v := range props {
					dev.Props[k] = v
				}
			}
		}
	}
	for _, cmd := range out.Run {
		if d.cfg.NoRun {
			continue
		}
		if _, err := d.Program(cmd, dev); err != nil {
			log.Printf("%s: RUN %q: %v", dev.DevPath, cmd, err)
		}
	}
	initialized := monotonicUsec()
	if _, _, _, old, ok := db.Read(dev.ID()); ok && old > 0 && dev.Action != "add" {
		initialized = old
	}
	if err := db.Write(dev, initialized, out.LinkPriority); err != nil {
		log.Printf("%s: database: %v", dev.DevPath, err)
	}
	d.broadcast(dev, initialized)
	return nil
}

func (d *Daemon) remove(dev *device.Device) error {
	_, _, links, _, _ := db.Read(dev.ID())
	for _, l := range links {
		if target, err := os.Readlink(l); err == nil && filepath.Join(filepath.Dir(l), target) == dev.DevNode {
			os.Remove(l)
		}
	}
	db.Remove(dev)
	d.broadcast(dev, 0)
	return nil
}

func (d *Daemon) broadcast(dev *device.Device, initialized int64) {
	if d.sender == nil {
		return
	}
	props := map[string]string{}
	for k, v := range dev.Props {
		props[k] = v
	}
	props["ACTION"] = dev.Action
	props["DEVPATH"] = dev.DevPath
	props["SUBSYSTEM"] = dev.Subsystem
	if links := dev.SortedLinks(); len(links) > 0 {
		props["DEVLINKS"] = strings.Join(links, " ")
	}
	tags := dev.SortedTags()
	if len(tags) > 0 {
		props["TAGS"] = ":" + strings.Join(tags, ":") + ":"
		props["CURRENT_TAGS"] = props["TAGS"]
	}
	if initialized > 0 {
		props["USEC_INITIALIZED"] = strconv.FormatInt(initialized, 10)
	}
	if err := d.sender.Broadcast(uevent.Encode(props, dev.Subsystem, dev.DevType, tags)); err != nil {
		log.Printf("%s: broadcast: %v", dev.DevPath, err)
	}
}

func (d *Daemon) applyNode(dev *device.Device, out *rules.Outcome) {
	uid, gid := -1, -1
	if v, err := strconv.Atoi(dev.Props["DEVUID"]); err == nil {
		uid = v
	}
	if v, err := strconv.Atoi(dev.Props["DEVGID"]); err == nil {
		gid = v
	}
	if out.Owner != "" {
		uid = d.lookupUser(out.Owner)
	}
	if out.Group != "" {
		gid = d.lookupGroup(out.Group)
	}
	mode := -1
	if v, err := strconv.ParseUint(dev.Props["DEVMODE"], 8, 32); err == nil {
		mode = int(v)
	}
	if out.ModeSet {
		mode = int(out.Mode)
	} else if out.Group != "" && gid > 0 {
		mode = 0o660
	}
	var st unix.Stat_t
	if err := unix.Lstat(dev.DevNode, &st); err == nil {
		wantType := uint32(unix.S_IFCHR)
		if dev.IsBlock() {
			wantType = unix.S_IFBLK
		}
		if st.Mode&unix.S_IFMT == wantType && int(unix.Major(uint64(st.Rdev))) == dev.Major && int(unix.Minor(uint64(st.Rdev))) == dev.Minor {
			if mode >= 0 && st.Mode&0o7777 != uint32(mode) {
				unix.Chmod(dev.DevNode, uint32(mode))
			}
			if (uid >= 0 && int(st.Uid) != uid) || (gid >= 0 && int(st.Gid) != gid) {
				unix.Lchown(dev.DevNode, uid, gid)
			}
		}
	}
	for _, link := range dev.SortedLinks() {
		d.link(link, dev.DevNode, out.LinkPriority)
	}
}

func (d *Daemon) link(link, node string, priority int) {
	if !strings.HasPrefix(link, d.cfg.Dev+"/") || strings.Contains(link, "/../") {
		return
	}
	rel, err := filepath.Rel(filepath.Dir(link), node)
	if err != nil {
		return
	}
	if current, err := os.Readlink(link); err == nil {
		if current == rel {
			return
		}
		existing := filepath.Join(filepath.Dir(link), current)
		if existing != node && priority <= linkPriority(existing) {
			if _, err := os.Stat(existing); err == nil {
				return
			}
		}
	}
	os.MkdirAll(filepath.Dir(link), 0o755)
	tmp := link + ".sinty-devd"
	os.Remove(tmp)
	if err := os.Symlink(rel, tmp); err != nil {
		return
	}
	os.Rename(tmp, link)
}

func linkPriority(node string) int {
	var st unix.Stat_t
	if err := unix.Stat(node, &st); err != nil {
		return -1 << 31
	}
	kind := "c"
	if st.Mode&unix.S_IFMT == unix.S_IFBLK {
		kind = "b"
	}
	data, err := os.ReadFile(filepath.Join(db.Root, "data", fmt.Sprintf("%s%d:%d", kind, unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev)))))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "L:") {
			n, _ := strconv.Atoi(line[2:])
			return n
		}
	}
	return 0
}

func (d *Daemon) lookupGroup(name string) int {
	if v, ok := d.groups[name]; ok {
		return v
	}
	id := -1
	if n, err := strconv.Atoi(name); err == nil {
		id = n
	} else if g, err := user.LookupGroup(name); err == nil {
		id, _ = strconv.Atoi(g.Gid)
	} else {
		log.Printf("unknown group %q", name)
	}
	d.groups[name] = id
	return id
}

func (d *Daemon) lookupUser(name string) int {
	if v, ok := d.users[name]; ok {
		return v
	}
	id := -1
	if n, err := strconv.Atoi(name); err == nil {
		id = n
	} else if u, err := user.Lookup(name); err == nil {
		id, _ = strconv.Atoi(u.Uid)
	} else {
		log.Printf("unknown user %q", name)
	}
	d.users[name] = id
	return id
}

func monotonicUsec() int64 {
	var ts unix.Timespec
	unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return ts.Sec*1000000 + ts.Nsec/1000
}

func renameInterface(from, to string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var req struct {
		name    [unix.IFNAMSIZ]byte
		newname [unix.IFNAMSIZ]byte
		pad     [8]byte
	}
	if len(to) >= unix.IFNAMSIZ {
		return fmt.Errorf("name too long")
	}
	copy(req.name[:], from)
	copy(req.newname[:], to)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCSIFNAME, uintptr(unsafe.Pointer(&req))); errno != 0 {
		return errno
	}
	return nil
}

// Trigger asks the kernel to replay "add" events for every device, so the
// devices present at boot are processed like hotplugged ones. Subsystems go
// first, then devices, each in a stable order.
func Trigger(sys, action string) (int, error) {
	var paths []string
	for _, root := range []string{"bus", "class"} {
		filepath.WalkDir(filepath.Join(sys, root), func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if e.Name() == "uevent" && strings.Count(strings.TrimPrefix(path, sys), "/") == 3 {
				paths = append(paths, path)
			}
			return nil
		})
	}
	var devices []string
	filepath.WalkDir(filepath.Join(sys, "devices"), func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.Name() == "uevent" && e.Type().IsRegular() {
			devices = append(devices, path)
		}
		return nil
	})
	sort.Strings(devices)
	paths = append(paths, devices...)
	count := 0
	for _, p := range paths {
		if err := os.WriteFile(p, []byte(action), 0); err == nil {
			count++
		}
	}
	return count, nil
}
