# Baker image: same binary in dev (compose run) and cloud (ECS one-off task).
# tippecanoe gets vendored here at M4 for the PMTiles stage (DEV-2).
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -o /baker ./cmd/baker

FROM debian:bookworm-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates libstdc++6 && rm -rf /var/lib/apt/lists/*
COPY --from=build /baker /usr/local/bin/baker
USER nobody:nogroup
ENTRYPOINT ["baker"]
