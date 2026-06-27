# syntax=docker/dockerfile:1
# Liquidsoap 2.x — uses savonet/liquidsoap official image for correct API
# Liquidsoap 1.x is end-of-life and has incompatible syntax.

FROM savonet/liquidsoap:v2.2.5
LABEL maintainer="abzwingt-gaming"
LABEL source="github.com/abzwingt-gaming/cadence"

# Switch to root to create log directory, then drop back to liquidsoap user
USER root
RUN mkdir -p /var/log/liquidsoap && \
    chown -R liquidsoap:liquidsoap /var/log/liquidsoap
USER liquidsoap

EXPOSE 1234 8001
CMD ["liquidsoap", "/etc/liquidsoap/cadence.liq"]
