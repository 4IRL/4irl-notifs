# Build context is the repo root (`docker build -f delivery-api/Dockerfile .`).
# LOCKSTEP: keep the golang and alpine tags equal to the root Dockerfile.
# Migrations are embedded, so this one image runs both `migrate` and the server.

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
