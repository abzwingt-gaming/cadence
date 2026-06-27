# syntax=docker/dockerfile:1
# Icecast 2.4.x on Alpine

FROM alpine:3.20
LABEL maintainer="abzwingt-gaming"
LABEL source="github.com/abzwingt-gaming/cadence"

RUN apk add --no-cache icecast

# Create log directory with correct ownership
RUN mkdir -p /var/log/icecast && \
    chown -R icecast:icecast /var/log/icecast

EXPOSE 8000
USER icecast
CMD ["icecast", "-c", "/etc/icecast/cadence.xml"]
