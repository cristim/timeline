# Baker image: same binary in dev (compose run) and cloud (ECS one-off task).
# tippecanoe gets vendored here at M4 for the PMTiles stage (DEV-2).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /baker ./cmd/baker

FROM alpine:3.22
COPY --from=build /baker /usr/local/bin/baker
ENTRYPOINT ["baker"]
