# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/node:22-alpine AS front
RUN npm install -g pnpm@9.15.4
WORKDIR /web
COPY ./web .
ENV NODE_OPTIONS="--max-old-space-size=4096"
RUN pnpm install --frozen-lockfile && pnpm build


FROM public.ecr.aws/docker/library/golang:1.26.4-alpine AS back
RUN apk add --no-cache git
WORKDIR /go/src/hanzo-cloud
# Private cross-org modules (hanzoai/*, zap-proto/*) are fetched via authenticated
# git, bypassing the public proxy. gh_token is the shared docker-build.yml BuildKit
# secret; no-op when absent (local/dev).
#
# luxfi/* is PUBLIC and published to the Go module proxy, but its git tags are
# transient — the remote re-tags, so a pinned tag (geth v1.17.12) later 404s on a
# direct git fetch ("unknown revision"). Resolve luxfi through the proxy, which
# holds each version immutably; GONOSUMDB skips the transparency log (whose stale
# pre-re-tag hashes would mismatch), and go.sum still verifies the download.
ENV GOPRIVATE=github.com/hanzoai/*,github.com/zap-proto/* \
    GONOSUMDB=github.com/hanzoai/*,github.com/luxfi/*,github.com/zap-proto/*
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -tags metrics -ldflags="-w -s" -o server ./cmd/aid


FROM public.ecr.aws/docker/library/alpine:3.21 AS standard
LABEL maintainer="https://hanzo.ai/"
ARG USER=hanzo

RUN apk add --no-cache ca-certificates curl sudo \
    && update-ca-certificates \
    && adduser -D $USER -u 1000 \
    && echo "$USER ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/$USER \
    && chmod 0440 /etc/sudoers.d/$USER \
    && mkdir logs files \
    && chown -R $USER:$USER logs files

USER 1000
WORKDIR /
COPY --from=back --chown=$USER:$USER /go/src/hanzo-cloud/server ./server
COPY --from=back --chown=$USER:$USER /go/src/hanzo-cloud/data ./data
COPY --from=back --chown=$USER:$USER /go/src/hanzo-cloud/conf/app.conf ./conf/app.conf
COPY --from=back --chown=$USER:$USER /go/src/hanzo-cloud/conf/models.yaml ./conf/models.yaml
COPY --from=front --chown=$USER:$USER /web/build ./web/build
ENV RUNNING_IN_DOCKER=true

ENTRYPOINT ["/server"]


FROM public.ecr.aws/docker/library/debian:latest AS db
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        mariadb-server \
        mariadb-client \
    && rm -rf /var/lib/apt/lists/*


FROM db AS allinone
LABEL maintainer="https://hanzo.ai/"

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /
COPY --from=back /go/src/hanzo-cloud/server ./server
COPY --from=back /go/src/hanzo-cloud/data ./data
COPY --from=back /go/src/hanzo-cloud/docker-entrypoint.sh /docker-entrypoint.sh
COPY --from=back /go/src/hanzo-cloud/conf/app.conf ./conf/app.conf
COPY --from=back /go/src/hanzo-cloud/conf/models.yaml ./conf/models.yaml
COPY --from=front /web/build ./web/build
ENV RUNNING_IN_DOCKER=true

ENTRYPOINT ["/bin/bash"]
CMD ["/docker-entrypoint.sh"]
