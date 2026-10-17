# sinty-devd

A native device manager for SintyOS. It takes the place of `udevd`: it listens
for kernel device events, runs the udev rules installed on the system, sets the
owner, group and mode of device nodes, creates the `/dev/disk/by-*`,
`/dev/input/by-*` and other symlinks, loads kernel modules, names network
interfaces, writes the udev database and announces every processed device to
libudev clients. Programs built against libudev (libinput, wlroots, pipewire,
upower, udisks, NetworkManager clients) keep working unchanged.

## Why it exists

SintyOS does not boot with systemd. PID 1 is `sinit` and services are driven by
`atomctl`. The udev daemon shipped with the base system assumes a systemd boot
for its startup order, and devices present at boot were only processed when a
service remembered to run `udevadm trigger` first: network cards and touchpads
could come up without the properties their users wait for.

`sinty-devd` replays the devices present when it starts on its own, so every
device is processed exactly as if it had been plugged in, and
`sinty-devadm settle` returns once that is done.

## What it implements

- the udev rules language: every match and assignment key used by the rules
  of the image, `GOTO`/`LABEL`, `PROGRAM`/`RESULT`, `IMPORT{program,builtin,
  file,db,parent,cmdline}`, `RUN{program,builtin}`, `OPTIONS` (link
  priority, string escaping, static nodes, database persistence), the `%k`
  and `$attr{}` substitutions, including `$attr{[dmi/id]modalias}`, and udev
  wildcards with `|` alternatives and `[!...]` classes;
- the builtins the rules call: `input_id`, `path_id`, `usb_id`, `net_id`,
  `hwdb` (reads the compiled `hwdb.bin`), `keyboard` (scancode remapping and
  axis overrides), `kmod`, `blkid` and `btrfs`;
- the udev database in `/run/udev/data` and `/run/udev/tags`, in the format
  libudev reads, with the initialization time of each device;
- the libudev netlink protocol, including the subsystem filter hash and the
  tag bloom filter, so monitors with filters receive exactly their events.

Helper programs such as `ata_id`, `scsi_id` and `cdrom_id` are the ones the
rules already reference in `/usr/lib/udev`.

## Usage

    sinty-devd [--coldplug=false]            run the daemon (as root)

    sinty-devadm settle [--timeout=SECONDS]  wait until every device is processed
    sinty-devadm trigger [--action=ACTION] [--type=devices|subsystems]
                         [--subsystem-match=NAME] [--attr-match=ATTR=VALUE]
    sinty-devadm control --reload            reread the rules
    sinty-devadm info DEVICE                 show the properties of a device

Installed as `udevadm`, `sinty-devadm` accepts these same forms, so scripts
that call `udevadm trigger`, `udevadm settle` or `udevadm control --reload`
keep working.

## Compatibility

The builtins and the rules engine are checked against a running systemd udev:
the tests compare, device by device, the database a real system produced with
the one `sinty-devd` computes from the same sysfs, rules and hardware database.

    SINTY_DEVD_HOST_DB=/path/to/copy/of/run/udev/data \
    SINTY_DEVD_HOST_RULES=/path/to/rules.d \
    SINTY_DEVD_HWDB=/path/to/hwdb.bin \
    go test ./internal/...

## Build

    make build            # ./sinty-devd and ./sinty-devadm
    make static           # stripped static binaries for the image
    make test
    go build ./...        # plain build

Requires Go >= 1.25 and Linux. The only non-stdlib dependency is
`golang.org/x/sys/unix`.
