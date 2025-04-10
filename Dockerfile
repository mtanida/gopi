FROM --platform=$BUILDPLATFORM golang:1.24.0 AS builder
ARG TARGETOS TARGETARCH

WORKDIR /app
COPY go.mod ./
RUN \
  --mount=type=cache,target=/root/.cache/go-build \
  go mod download -x

COPY main.go ./
RUN \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-w -s" -o /go/bin/gopi .

FROM cgr.dev/chainguard/static:latest
COPY --from=builder /go/bin/gopi /usr/local/bin/gopi

EXPOSE 8080
ENTRYPOINT ["gopi"]
