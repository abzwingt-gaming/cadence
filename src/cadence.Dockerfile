# syntax=docker/dockerfile:1
# Pure-Go build: CGO_ENABLED=0, no gcc required.
# modernc.org/sqlite used instead of go-sqlite3 (no CGO).

FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:1.25-alpine AS builder
ARG TARGETPLATFORM BUILDPLATFORM TARGETOS TARGETARCH
# Version injected at build time by CI (e.g. --build-arg CSERVER_VERSION=v1.2.3)
ARG CSERVER_VERSION=dev
# git is required by go mod download to fetch modules not cached on the proxy
RUN apk add --no-cache git
WORKDIR /build
COPY ./server/go.mod ./server/go.sum ./
RUN go mod download
COPY ./server ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -ldflags="-w -s -X main.buildVersion=${CSERVER_VERSION}" \
      -o /cadence-server ./...

FROM alpine:3.20
LABEL maintainer="abzwingt-gaming"
LABEL source="github.com/abzwingt-gaming/cadence"

RUN apk add --no-cache ca-certificates tzdata wget

# Static files served from /app/public/ — must match CSERVER_ROOTPATH default
COPY --from=builder /build/public /app/public/
COPY --from=builder /cadence-server /app/cadence-server

# Ensure custom.css placeholder exists so volume mount works without rebuild
RUN mkdir -p /app/public/css && \
    touch /app/public/css/custom.css

RUN adduser -D -H cadence && \
    chown -R cadence /app

EXPOSE 8080
USER cadence
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/readyz || exit 1
CMD ["/app/cadence-server"]
