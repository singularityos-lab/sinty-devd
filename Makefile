BIN := sinty-devadm
DAEMON := sinty-devd
PREFIX ?= /usr

.PHONY: all build static test vet install clean

all: build

build:
	go build -o $(BIN) ./cmd/sinty-devadm
	go build -o $(DAEMON) ./cmd/sinty-devd

static:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/sinty-devadm
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(DAEMON) ./cmd/sinty-devd

test:
	go test ./...

vet:
	go vet ./...

install: static
	install -Dm755 $(BIN) $(DESTDIR)$(PREFIX)/bin/$(BIN)
	install -Dm755 $(DAEMON) $(DESTDIR)$(PREFIX)/libexec/$(DAEMON)
	install -Dm644 packaging/sinty-devd.service $(DESTDIR)$(PREFIX)/lib/systemd/system/sinty-devd.service

clean:
	rm -f $(BIN) $(DAEMON)
