# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM node:20.18.0 AS front
RUN npm install -g pnpm@10
WORKDIR /web
COPY ./web .
ENV NODE_OPTIONS="--max-old-space-size=4096"
RUN pnpm install --frozen-lockfile && pnpm build


FROM golang:1.26-alpine AS back
RUN apk add --no-cache git
WORKDIR /go/src/hanzo-cloud
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-w -s" -o server .


FROM alpine:3.21 AS standard
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


FROM debian:latest AS db
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
