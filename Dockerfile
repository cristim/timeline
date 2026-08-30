# Baker image: same binary in dev (compose run) and cloud (ECS one-off task).
FROM debian:bookworm AS tippecanoe-build
ARG TIPPECANOE_VERSION=2.79.0
ARG TIPPECANOE_SHA256=b0fd9df49b6efc988288ea48774822c6de19eb48428017f27ee0b3b01d44f05d
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends build-essential ca-certificates curl libsqlite3-dev zlib1g-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN curl -fsSL -o tippecanoe.tar.gz "https://github.com/felt/tippecanoe/archive/refs/tags/${TIPPECANOE_VERSION}.tar.gz" && echo "${TIPPECANOE_SHA256}  tippecanoe.tar.gz" | sha256sum -c - && tar -xzf tippecanoe.tar.gz --strip-components=1 && make -j2 tippecanoe

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -o /baker ./cmd/baker

FROM debian:bookworm-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates libsqlite3-0 libstdc++6 zlib1g && rm -rf /var/lib/apt/lists/*
COPY --from=build /baker /usr/local/bin/baker
COPY --from=tippecanoe-build /src/tippecanoe /usr/local/bin/tippecanoe
USER nobody:nogroup
ENTRYPOINT ["baker"]
