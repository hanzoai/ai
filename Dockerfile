FROM --platform=$BUILDPLATFORM node:20.18.0 AS FRONT
WORKDIR /web
COPY ./web .
ENV NODE_OPTIONS="--max-old-space-size=4096"
RUN yarn install --frozen-lockfile --network-timeout 1000000 && yarn run build


FROM --platform=$BUILDPLATFORM golang:1.23.6 AS BACK
WORKDIR /go/src/hanzo-cloud
COPY . .
RUN chmod +x ./build.sh
RUN ./build.sh


FROM alpine:latest AS STANDARD
LABEL MAINTAINER="https://hanzo.ai/"
ARG USER=hanzo
ARG TARGETOS
ARG TARGETARCH
ENV BUILDX_ARCH="${TARGETOS:-linux}_${TARGETARCH:-amd64}"

RUN sed -i 's/https/http/' /etc/apk/repositories
RUN apk add --update sudo
RUN apk add curl
RUN apk add ca-certificates && update-ca-certificates

RUN adduser -D $USER -u 1000 \
    && echo "$USER ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/$USER \
    && chmod 0440 /etc/sudoers.d/$USER \
    && mkdir logs \
    && mkdir files \
    && chown -R $USER:$USER logs \
    && chown -R $USER:$USER files

USER 1000
WORKDIR /
COPY --from=BACK --chown=$USER:$USER /go/src/hanzo-cloud/server_${BUILDX_ARCH} ./server
COPY --from=BACK --chown=$USER:$USER /go/src/hanzo-cloud/data ./data
COPY --from=BACK --chown=$USER:$USER /go/src/hanzo-cloud/conf/app.conf ./conf/app.conf
COPY --from=BACK --chown=$USER:$USER /go/src/hanzo-cloud/conf/models.yaml ./conf/models.yaml
COPY --from=FRONT --chown=$USER:$USER /web/build ./web/build
ENV RUNNING_IN_DOCKER=true

ENTRYPOINT ["/server"]


FROM debian:latest AS db
RUN apt update \
    && apt install -y \
        mariadb-server \
        mariadb-client \
    && rm -rf /var/lib/apt/lists/*


FROM db AS ALLINONE
LABEL MAINTAINER="https://hanzo.ai/"
ARG TARGETOS
ARG TARGETARCH
ENV BUILDX_ARCH="${TARGETOS:-linux}_${TARGETARCH:-amd64}"

RUN apt update && apt install -y ca-certificates && update-ca-certificates

WORKDIR /
COPY --from=BACK /go/src/hanzo-cloud/server_${BUILDX_ARCH} ./server
COPY --from=BACK /go/src/hanzo-cloud/data ./data
COPY --from=BACK /go/src/hanzo-cloud/docker-entrypoint.sh /docker-entrypoint.sh
COPY --from=BACK /go/src/hanzo-cloud/conf/app.conf ./conf/app.conf
COPY --from=BACK /go/src/hanzo-cloud/conf/models.yaml ./conf/models.yaml
COPY --from=FRONT /web/build ./web/build
ENV RUNNING_IN_DOCKER=true

ENTRYPOINT ["/bin/bash"]
CMD ["/docker-entrypoint.sh"]
