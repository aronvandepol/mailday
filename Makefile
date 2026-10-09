.PHONY: build test check cross demo install install-syncd install-launchd install-tz clean

PREFIX ?= $(HOME)/.local
BIN := $(PREFIX)/bin

build:
	mkdir -p bin
	go build -o bin/mailday ./cmd/mailday
	go build -o bin/mailday-syncd ./cmd/mailday-syncd

test:
	go test ./...

check: test
	go vet ./...
	go build -o /dev/null ./cmd/mailday
	go build -o /dev/null ./cmd/mailday-syncd

# Everything is pure Go, so every Unix target cross-compiles from one machine.
cross:
	for target in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 freebsd/amd64 openbsd/amd64; do \
		GOOS=$${target%/*} GOARCH=$${target#*/} go build -o /dev/null ./cmd/mailday && \
		GOOS=$${target%/*} GOARCH=$${target#*/} go build -o /dev/null ./cmd/mailday-syncd && \
		echo "ok $$target" || exit 1; \
	done

# Copy to a new file and rename it into place: overwriting a binary macOS has
# already run leaves a stale code-signature cache, and the kernel then kills
# it on launch (exit 137).
install: build
	mkdir -p "$(BIN)"
	for name in mailday mailday-syncd; do \
		install -m755 bin/$$name "$(BIN)/$$name.new" && mv -f "$(BIN)/$$name.new" "$(BIN)/$$name"; \
	done
	ln -sf mailday "$(BIN)/md"
	install -m755 scripts/mailday-calsync scripts/mailday-exchange scripts/mailday-calendar "$(BIN)/"

# Linux: run mailday-syncd as a systemd user service. It runs mbsync itself,
# so an mbsync timer you had before should be turned off.
install-syncd: install
	install -Dm644 scripts/systemd/mailday-syncd.service "$(HOME)/.config/systemd/user/mailday-syncd.service"
	systemctl --user daemon-reload
	systemctl --user enable mailday-syncd.service
	systemctl --user restart mailday-syncd.service
	@systemctl --user is-enabled mbsync.timer >/dev/null 2>&1 && echo 'note: mbsync.timer is enabled; mailday-syncd replaces it (systemctl --user disable --now mbsync.timer)' || true

# macOS: the same as a launchd agent.
install-launchd: install
	mkdir -p "$(HOME)/Library/LaunchAgents" "$(HOME)/Library/Logs"
	sed 's#@HOME@#$(HOME)#g' scripts/launchd/mailday-syncd.plist.in > "$(HOME)/Library/LaunchAgents/local.mailday.syncd.plist"
	-launchctl bootout gui/$$(id -u)/local.mailday.syncd 2>/dev/null
	launchctl bootstrap gui/$$(id -u) "$(HOME)/Library/LaunchAgents/local.mailday.syncd.plist"

# Linux: let NetworkManager run tzupdate when a network comes up, so the system
# zone follows you. Needs root, so the command is printed for you to run.
install-tz:
	@echo 'Run this once (it needs root):'
	@echo '  sudo install -m755 $(CURDIR)/scripts/linux/90-mailday-tzupdate /etc/NetworkManager/dispatcher.d/'

# Fictional home directory for screenshots; see scripts/demo/make-demo.
demo: build
	scripts/demo/make-demo --force "$(CURDIR)/demo-home"
	@echo '  or with this build: (source demo-home/env.sh && bin/mailday)'

clean:
	rm -rf bin coverage.out
