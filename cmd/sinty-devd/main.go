// Command sinty-devd manages device nodes and device events for Sinty OS.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/singularityos-lab/sinty-devd/internal/daemon"
	"github.com/singularityos-lab/sinty-devd/internal/uevent"
)

// ControlSocket is where sinty-devadm talks to the daemon.
const ControlSocket = "/run/sinty-devd/control"

func main() {
	coldplug := flag.Bool("coldplug", true, "replay add events for the devices present at start")
	flag.Parse()
	log.SetFlags(0)
	log.SetPrefix("sinty-devd: ")

	cfg := daemon.DefaultConfig()
	d, err := daemon.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	kernel, err := uevent.Listen()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() {
		if err := d.Run(ctx); err != nil {
			log.Fatal(err)
		}
	}()
	go serveControl(ctx, d, cfg.Sys)
	go func() {
		for {
			env, err := kernel.Receive()
			if err != nil {
				log.Printf("receiving uevents: %v", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			d.Enqueue(env)
		}
	}()
	if *coldplug {
		n, _ := daemon.Trigger(cfg.Sys, "add")
		log.Printf("replayed %d devices", n)
	}
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	for {
		select {
		case <-ctx.Done():
			return
		case <-reload:
			if err := d.Reload(); err != nil {
				log.Printf("reload: %v", err)
			}
		}
	}
}

func serveControl(ctx context.Context, d *daemon.Daemon, sys string) {
	os.MkdirAll(filepath.Dir(ControlSocket), 0o755)
	os.Remove(ControlSocket)
	l, err := net.Listen("unix", ControlSocket)
	if err != nil {
		log.Printf("control socket: %v", err)
		return
	}
	os.Chmod(ControlSocket, 0o600)
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go handle(conn, d, sys)
	}
}

func handle(conn net.Conn, d *daemon.Daemon, sys string) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Minute))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	switch fields[0] {
	case "settle":
		timeout := 120 * time.Second
		if len(fields) > 1 {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				timeout = time.Duration(n) * time.Second
			}
		}
		if d.Settle(timeout) {
			fmt.Fprintln(conn, "ok")
		} else {
			fmt.Fprintln(conn, "timeout")
		}
	case "reload":
		if err := d.Reload(); err != nil {
			fmt.Fprintln(conn, "error", err)
			return
		}
		fmt.Fprintln(conn, "ok")
	case "trigger":
		action := "change"
		if len(fields) > 1 {
			action = fields[1]
		}
		n, _ := daemon.Trigger(sys, action)
		fmt.Fprintln(conn, "ok", n)
	case "ping":
		fmt.Fprintln(conn, "ok")
	default:
		fmt.Fprintln(conn, "error unknown command")
	}
}
