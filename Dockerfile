# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -o /build/daemon . && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -o /build/auth_server ./util/

FROM gcr.io/distroless/base-debian12:latest

COPY --from=builder /build/daemon       /usr/local/bin/daemon
COPY --from=builder /build/auth_server  /usr/local/bin/auth_server

EXPOSE 2333 7684

ENTRYPOINT ["/usr/local/bin/daemon"]
