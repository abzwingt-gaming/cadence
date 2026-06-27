# syntax=docker/dockerfile:1
# Pure-Go build: CGO_ENABLED=0, no gcc required.
# modernc.org/sqlite is used instead of go-sqlite3 to avoid CGO.

FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:1.22-alpine AS builder
ARG TARGETPLATFORM BUILDPLATFORM TARGETOS TARGETARCH
WORKDIR /cadence
COPY ./server ./
RUN go mod download
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-w -s" -o /cadence-server ./...

FROM alpine:3.20
LABEL maintainer="abzwingt-gaming"
LABEL source="github.com/abzwingt-gaming/cadence"

# ca-certificates for outbound HTTPS (Icecast status)
RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /cadence/public /cadence/server/public
COPY --from=builder /cadence-server  /cadence/cadence-server

# Ensure custom.css placeholder exists even if volume not mounted
RUN mkdir -p /cadence/server/public/css && \
    touch /cadence/server/public/css/custom.css

# Non-root user
RUN adduser -D -H cadence
RUN chown -R cadence /cadence

EXPOSE 8080
USER cadence
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1
CMD ["/cadence/cadence-server"]
