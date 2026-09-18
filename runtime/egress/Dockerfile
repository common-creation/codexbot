# syntax=docker/dockerfile:1.7

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        squid \
        tini \
    && rm -rf /var/lib/apt/lists/* \
    && install -d -o proxy -g proxy -m 0750 /var/log/squid

COPY --chown=proxy:proxy --chmod=0444 runtime/egress/squid.conf /etc/squid/squid.conf

USER proxy:proxy

EXPOSE 3128

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["squid", "-k", "parse", "-f", "/etc/squid/squid.conf"]

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["squid", "-N", "-f", "/etc/squid/squid.conf"]
