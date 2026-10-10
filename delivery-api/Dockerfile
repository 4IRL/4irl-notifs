# delivery-api image (build context is the repo root: `docker build -f delivery-api/Dockerfile .`,
# so the root .dockerignore applies).
#
# LOCKSTEP: keep the golang and alpine tags equal to the root Dockerfile.
#
# Two stages:
#   1. build — compiles the statically-linked delivery-api Go binary from delivery-api/. Its
#      migrations are embedded in the binary (go:embed), so the same image runs both the
#      `migrate` subcommand (delivery-migrate) and the server (delivery-api).
#   2. final — a minimal, non-root Alpine image.

FROM golang:1.26.0-alpine3.23 AS build
WORKDIR /src
COPY delivery-api/go.mod delivery-api/go.sum ./
RUN go mod download
COPY delivery-api/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/delivery-api .

FROM alpine:3.23.0
# ca-certificates: needed later for outbound HTTPS sinks.
RUN apk add --no-cache ca-certificates
RUN addgroup -g 1000 notifs && adduser -D -u 1000 -G notifs notifs
COPY --from=build /out/delivery-api /usr/local/bin/delivery-api
EXPOSE 8080
USER notifs
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD wget -q -O - http://localhost:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/delivery-api"]
