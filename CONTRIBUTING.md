# Contributing to sinty-devd

Thanks for your interest in contributing!

## Quick Start

```bash
git clone https://github.com/singularityos-lab/sinty-devd
cd sinty-devd
go build ./...
go test ./...
```

## Guidelines

- Keep changes additive and tested. sinty-devd runs as root and owns /dev;
  prefer small, verifiable commits over large rewrites.
- Match the surrounding style. Comments explain WHY, not WHAT.
- By submitting a contribution you agree to the [CLA](CLA.md).

## License

GPL-3.0-only.
