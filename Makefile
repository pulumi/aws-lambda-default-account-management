VERSION ?= $(shell git rev-parse --short HEAD)$(shell git diff --quiet || echo -dirty)
ZIP     := deployment.zip

.PHONY: ensure test buildapp verify deploy

ensure:
	cd application && \
	go mod tidy && \
	go mod download

test:
	cd application && \
	go test ./...

# The binary must be named `bootstrap` for the provided.al2023 runtime, and
# VERSION is stamped in because the artifact is untracked: the log line it
# produces is the only way to tell which commit a region is running.
buildapp:
	cd application && \
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags lambda.norpc \
		-ldflags "-X main.version=$(VERSION)" -o bootstrap . && \
	rm -f ../$(ZIP) && \
	zip -j ../$(ZIP) bootstrap && \
	rm bootstrap
	$(MAKE) verify

# Each check names a way the artifact can be wrong while still zipping cleanly.
verify:
	@# the runtime looks for an entry with exactly this name
	unzip -Z1 $(ZIP) | grep -qx bootstrap
	@# a stale member from an interrupted build would ship alongside it
	test "$$(unzip -Z1 $(ZIP) | wc -l | tr -d ' ')" -eq 1
	@# Lambda will not exec it without the executable bit
	unzip -Z $(ZIP) bootstrap | grep -q '^-rwx'
	@# linux/amd64 ELF64 — catches a host-arch binary zipped by mistake
	unzip -p $(ZIP) bootstrap | od -An -tx1 -N20 | tr -d ' \n' | grep -q '^7f454c46020101.*3e00'

# `pulumi up` consumes ../deployment.zip by path, so nothing otherwise stops a
# stale artifact being deployed against a changed handler contract.
deploy: buildapp
	cd lambda && pulumi up
