# syntax=docker/dockerfile:1
# Pure-Go build: CGO_ENABLED=0, no gcc required.
# modernc.org/sqlite used instead of go-sqlite3 (no CGO).

FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:1.22-alpine AS builder
ARG TARGETPLATFORM BUILDPLATFORM TARGETOS TARGETARCH
# Version injected at build time by CI (e.g. --build-arg CSERVER_VERSION=v1.2.3)
ARG CSERVER_VERSION=dev
WORKDIR /cadence
COPY ./server ./
RUN go mod download
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -ldflags="-w -s -X main.buildVersion=${CSERVER_VERSION}" \
      -o /cadence-server ./...

FROM alpine:3.20
LABEL maintainer="abzwingt-gaming"
LABEL source="github.com/abzwingt-gaming/cadence"

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /cadence/public /cadence/server/public
COPY --from=builder /cadence-server  /cadence/cadence-server

RUN mkdir -p /cadence/server/public/css && \
    touch /cadence/server/public/css/custom.css

RUN adduser -D -H cadence
RUN chown -R cadence /cadence

EXPOSE 8080
USER cadence
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/readyz || exit 1
CMD ["/cadence/cadence-server"]
