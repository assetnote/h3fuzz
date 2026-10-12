ARG CADDY_IMAGE=docker.io/library/caddy:2.11.4-alpine@sha256:6aeddd44c3078b0f9a35206472a11420648a79c184603ef95957d0a20044cb2b
FROM ${CADDY_IMAGE}

COPY Caddyfile.instrumented Caddyfile.ceiling harness-routes.caddy /etc/caddy/
COPY server/generate-fixtures.sh /usr/local/bin/generate-h3fuzz-fixtures

RUN chmod 0755 /usr/local/bin/generate-h3fuzz-fixtures \
    && /usr/local/bin/generate-h3fuzz-fixtures /srv

WORKDIR /srv

