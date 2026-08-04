# hanzoai/ai — the `aid` AI control plane. One make vocabulary across every
# Hanzo repo: help / build / test / lint / clean.

GO          ?= go
LDFLAGS     ?= -w -s

# CGO_ENABLED=0 is the shipped setting in all three build paths (Dockerfile,
# Dockerfile.kaniko, .goreleaser.yaml): the image is alpine and the binary links
# no libc.
CGO_ENABLED ?= 0

# The two Dockerfiles disagree about this tag — ./Dockerfile builds `-tags
# metrics`, Dockerfile.kaniko builds without it — and for the BINARY the
# disagreement is empty: no non-test file in this module carries that
# constraint. The only `//go:build metrics` in the tree is
# object/prometheus_handler_metrics_test.go, so both images ship the same
# program and Dockerfile.kaniko's header is right to call itself an "identical
# artifact". Followed here because ./Dockerfile is the canonical build (kaniko
# exists only because kaniko cannot do --mount=type=secret), and because the tag
# is NOT empty for `test`: it is the switch that turns that suite on, and that
# suite exists specifically to run under the shipping build.
TAGS        ?= metrics

.PHONY: help build test lint clean

help: ## Show this help.
	@awk 'BEGIN{FS=":.*##";printf "\nUsage: make <target>\n\nTargets:\n"} /^[a-zA-Z_-]+:.*##/{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# The artifact this repo ships is ONE binary: cmd/aid — copied into the image as
# /server and its ENTRYPOINT, released as `aid` by goreleaser. cmd/openapi,
# cmd/routerdoc, cmd/check_ddl and cmd/pg2sqlite are developer tools nothing
# deploys, so this does not link them; `go build ./...` still compiles them and
# `make lint` still vets them.
build: ## Build the shipped daemon into ./bin/aid.
	@mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o bin/aid ./cmd/aid

# `-tags skipCi` is deliberately NOT set. Those suites (object/, video/) need a
# live MySQL — LLM.md documents the tag as exactly that opt-in — so they stay off
# and everything that needs no service runs.
test: ## Run every test that needs no live service.
	$(GO) test -tags "$(TAGS)" ./...

lint: ## go vet across the module.
	$(GO) vet -tags "$(TAGS)" ./...

# ./bin and nothing else, because nothing else here is ours. hanzo.db is the
# local dev database, not an artifact: conf/app.conf points the sqlite driver at
# `file:hanzo.db`, so deleting it throws away local state (it is untracked and
# matched by .gitignore's `*.db`, so git would not even warn you). `make test`
# drops copies of that same name in object/, storage/ and contest/; they are
# left too, because no glob that catches them is worth the chance of catching
# the one at the root. web/build is the Dockerfile front stage's output, built
# by pnpm in a container and by no target here. data/ is tracked source.
clean: ## Remove built artifacts.
	rm -rf bin
