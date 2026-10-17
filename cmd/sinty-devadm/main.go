// Command sinty-devadm controls sinty-devd. Installed as udevadm it accepts
// the trigger, settle, control and info forms that system scripts use.
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/singularityos-lab/sinty-devd/internal/db"
	"github.com/singularityos-lab/sinty-devd/internal/device"
)

const controlSocket = "/run/sinty-devd/control"

func usage() {
	fmt.Fprintln(os.Stderr, `usage: sinty-devadm COMMAND [OPTIONS]

  trigger [--action=ACTION] [--type=devices|subsystems] [--subsystem-match=NAME]
          [--attr-match=ATTR=VALUE] [--sysname-match=NAME]
  settle  [--timeout=SECONDS]
  control --reload
  info    [--query=property] DEVICE`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "trigger":
		err = trigger(args)
	case "settle":
		err = settle(args)
	case "control":
		err = control(args)
	case "info":
		err = info(args)
	case "hwdb":
		return
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "sinty-devadm: unknown command %q\n", cmd)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sinty-devadm:", err)
		os.Exit(1)
	}
}

func option(args []string, i *int, long, short string) (string, bool) {
	a := args[*i]
	if strings.HasPrefix(a, long+"=") {
		return strings.TrimPrefix(a, long+"="), true
	}
	if a == long || (short != "" && a == short) {
		if *i+1 < len(args) {
			*i++
			return args[*i], true
		}
		return "", true
	}
	if short != "" && strings.HasPrefix(a, short) && len(a) > len(short) {
		return a[len(short):], true
	}
	return "", false
}

func request(line string, timeout time.Duration) (string, error) {
	conn, err := net.DialTimeout("unix", controlSocket, 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("sinty-devd is not running: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	fmt.Fprintln(conn, line)
	reply, err := bufio.NewReader(conn).ReadString('\n')
	return strings.TrimSpace(reply), err
}

func settle(args []string) error {
	timeout := "120"
	for i := 0; i < len(args); i++ {
		if v, ok := option(args, &i, "--timeout", "-t"); ok {
			timeout = v
		}
	}
	var secs int
	fmt.Sscanf(timeout, "%d", &secs)
	if secs <= 0 {
		secs = 120
	}
	reply, err := request("settle "+fmt.Sprint(secs), time.Duration(secs+5)*time.Second)
	if err != nil {
		if strings.Contains(err.Error(), "not running") {
			return nil
		}
		return err
	}
	if reply != "ok" {
		return fmt.Errorf("timed out waiting for events")
	}
	return nil
}

func control(args []string) error {
	for _, a := range args {
		switch a {
		case "--reload", "-R", "--reload-rules":
			reply, err := request("reload", 30*time.Second)
			if err != nil {
				return err
			}
			if reply != "ok" {
				return fmt.Errorf("%s", reply)
			}
		}
	}
	return nil
}

type filter struct {
	subsystems []string
	sysnames   []string
	attrs      [][2]string
}

func (f filter) match(sys, dir string) bool {
	if len(f.subsystems) > 0 {
		target, err := os.Readlink(filepath.Join(dir, "subsystem"))
		if err != nil {
			return false
		}
		ok := false
		for _, s := range f.subsystems {
			if m, _ := filepath.Match(s, filepath.Base(target)); m {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.sysnames) > 0 {
		ok := false
		for _, s := range f.sysnames {
			if m, _ := filepath.Match(s, filepath.Base(dir)); m {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	for _, a := range f.attrs {
		data, err := os.ReadFile(filepath.Join(dir, a[0]))
		if err != nil {
			return false
		}
		if m, _ := filepath.Match(a[1], strings.TrimSpace(string(data))); !m {
			return false
		}
	}
	return true
}

func trigger(args []string) error {
	action, kind := "change", "devices"
	var f filter
	for i := 0; i < len(args); i++ {
		if v, ok := option(args, &i, "--action", "-c"); ok {
			action = v
		} else if v, ok := option(args, &i, "--type", "-t"); ok {
			kind = v
		} else if v, ok := option(args, &i, "--subsystem-match", "-s"); ok {
			f.subsystems = append(f.subsystems, v)
		} else if v, ok := option(args, &i, "--sysname-match", "-y"); ok {
			f.sysnames = append(f.sysnames, v)
		} else if v, ok := option(args, &i, "--attr-match", "-a"); ok {
			k, val, _ := strings.Cut(v, "=")
			if val == "" {
				val = "*"
			}
			f.attrs = append(f.attrs, [2]string{k, val})
		}
	}
	sys := "/sys"
	var paths []string
	if kind == "subsystems" {
		for _, root := range []string{"bus", "class"} {
			entries, _ := os.ReadDir(filepath.Join(sys, root))
			for _, e := range entries {
				if len(f.subsystems) > 0 {
					ok := false
					for _, s := range f.subsystems {
						if m, _ := filepath.Match(s, e.Name()); m {
							ok = true
						}
					}
					if !ok {
						continue
					}
				}
				paths = append(paths, filepath.Join(sys, root, e.Name(), "uevent"))
			}
		}
	} else {
		filepath.WalkDir(filepath.Join(sys, "devices"), func(path string, e os.DirEntry, err error) error {
			if err != nil || e.Name() != "uevent" || !e.Type().IsRegular() {
				return nil
			}
			if f.match(sys, filepath.Dir(path)) {
				paths = append(paths, path)
			}
			return nil
		})
	}
	sort.Strings(paths)
	for _, p := range paths {
		os.WriteFile(p, []byte(action), 0)
	}
	return nil
}

func info(args []string) error {
	var target string
	for i := 0; i < len(args); i++ {
		if v, ok := option(args, &i, "--name", "-n"); ok {
			target = v
		} else if v, ok := option(args, &i, "--path", "-p"); ok {
			target = v
		} else if !strings.HasPrefix(args[i], "-") {
			target = args[i]
		}
	}
	if target == "" {
		return fmt.Errorf("no device given")
	}
	devpath := target
	if strings.HasPrefix(target, "/dev/") || !strings.Contains(target, "/") {
		node := target
		if !strings.HasPrefix(node, "/dev/") {
			node = "/dev/" + node
		}
		majmin, kind, err := deviceNumber(node)
		if err != nil {
			return err
		}
		var found string
		if link, err := os.Readlink(filepath.Join("/sys/dev", kind, majmin)); err == nil {
			found = "/" + strings.TrimPrefix(filepath.Clean(filepath.Join("/sys/dev", kind, link)), "/sys/")
		}
		if found == "" {
			return fmt.Errorf("%s is not a device node", node)
		}
		devpath = found
	}
	devpath = "/" + strings.TrimPrefix(strings.TrimPrefix(devpath, "/sys"), "/")
	d, err := device.FromSyspath("/sys", devpath)
	if err != nil {
		return err
	}
	props, tags, links, initialized, _ := db.Read(d.ID())
	all := map[string]string{}
	for k, v := range d.Props {
		all[k] = v
	}
	for k, v := range props {
		all[k] = v
	}
	if len(links) > 0 {
		all["DEVLINKS"] = strings.Join(links, " ")
	}
	if len(tags) > 0 {
		all["TAGS"] = ":" + strings.Join(tags, ":") + ":"
	}
	if initialized > 0 {
		all["USEC_INITIALIZED"] = fmt.Sprint(initialized)
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s=%s\n", k, all[k])
	}
	return nil
}

func deviceNumber(node string) (string, string, error) {
	var st unix.Stat_t
	if err := unix.Stat(node, &st); err != nil {
		return "", "", err
	}
	kind := "char"
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFBLK:
		kind = "block"
	case unix.S_IFCHR:
	default:
		return "", "", fmt.Errorf("%s is not a device node", node)
	}
	return fmt.Sprintf("%d:%d", unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev))), kind, nil
}
