GO ?= go
VERSION ?= $(shell cat VERSION)
PYTHON ?= python3
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=
export GO PYTHON VERSION BINDIR DESTDIR

.PHONY: all build test race vet release check-release source install completions man clean

all: build

build:
	mkdir -p build
	CGO_ENABLED=0 "$$GO" build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$$VERSION" -o build/bupt ./cmd/bupt

test:
	"$$GO" test ./...

race:
	"$$GO" test -race ./...

vet:
	"$$GO" vet ./...

release:
	"$$PYTHON" scripts/package.py

check-release:
	"$$PYTHON" scripts/check_release.py

source:
	@set -eu; commit=$$(git rev-parse --verify HEAD); version=$$(git show "$$commit:VERSION"); \
	case "$$version" in ''|*[!A-Za-z0-9._+-]*) exit 2 ;; esac; \
	mkdir -p dist; temp=$$(mktemp dist/.source.XXXXXXXX); \
	trap 'rm -f "$$temp"' EXIT HUP INT TERM; \
	git archive --format=zip --prefix="bupt-cli-$$version/" -o "$$temp" "$$commit"; \
	path="dist/bupt-cli-$$version-source.zip"; mv -f "$$temp" "$$path"; \
	printf '%s\n' "$$path"

install: build
	install -d "$${DESTDIR}$${BINDIR}"
	@set -eu; temp=$$(mktemp "$${DESTDIR}$${BINDIR}/.bupt-install.XXXXXXXX"); \
	trap 'rm -f "$$temp"' EXIT HUP INT TERM; \
	install -m 0755 build/bupt "$$temp"; \
	mv -f "$$temp" "$${DESTDIR}$${BINDIR}/bupt"

completions: build
	mkdir -p build/completions
	build/bupt completion bash > build/completions/bupt.bash
	build/bupt completion zsh > build/completions/_bupt
	build/bupt completion fish > build/completions/bupt.fish

man:
	mkdir -p build/man
	gzip -n -c docs/bupt.1 > build/man/bupt.1.gz

clean:
	rm -rf build dist
