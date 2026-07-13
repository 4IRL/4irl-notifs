# provisioning-api image
#
# Three stages:
#   1. build — compiles the statically-linked provisioning-api Go binary from
#      provisioning-api/ (stdlib only, no external deps).
#   2. ntfy  — sources nothing but the upstream ntfy binary at /usr/bin/ntfy;
#      never run, just used as a COPY --from source.
#   3. final — a minimal, non-root Alpine image bundling both binaries.
#
# The ntfy binary is bundled directly into this image (rather than the API
# reaching a separate ntfy container over the Docker socket) so provisioning-api
# can shell out to `ntfy` and edit the shared auth.db over a mounted volume
# WITHOUT any Docker-socket access. A docker-exec-into-the-ntfy-container
# approach was considered and explicitly rejected for that reason.

FROM golang:1.26.0-alpine3.23 AS build
WORKDIR /src
COPY provisioning-api/go.mod provisioning-api/go.sum ./
RUN go mod download
COPY provisioning-api/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/provisioning-api .

FROM binwiederhier/ntfy:v2.26.0 AS ntfy

FROM alpine:3.23.0
RUN apk add --no-cache ca-certificates
RUN addgroup -g 1000 notifs && adduser -D -u 1000 -G notifs notifs
COPY --from=build /out/provisioning-api /usr/local/bin/provisioning-api
COPY --from=ntfy /usr/bin/ntfy /usr/bin/ntfy
ENV NTFY_BIN=/usr/bin/ntfy
EXPOSE 8080
USER notifs
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD wget -q -O - http://localhost:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/provisioning-api"]
