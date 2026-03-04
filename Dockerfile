FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -o /bin/tailbus-coord ./cmd/tailbus-coord && \
    CGO_ENABLED=1 go build -o /bin/tailbusd ./cmd/tailbusd && \
    CGO_ENABLED=1 go build -o /bin/tailbus ./cmd/tailbus && \
    CGO_ENABLED=1 go build -o /bin/tailbus-relay ./cmd/tailbus-relay

# --- relay image ---
FROM alpine:3.21 AS relay
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/tailbus-relay /usr/local/bin/tailbus-relay
ENTRYPOINT ["tailbus-relay"]

# --- daemon image ---
FROM alpine:3.21 AS daemon
RUN apk add --no-cache ca-certificates python3
COPY --from=builder /bin/tailbusd /usr/local/bin/tailbusd
COPY --from=builder /bin/tailbus /usr/local/bin/tailbus
RUN mkdir -p /data /var/run/tailbus
ENTRYPOINT ["tailbusd"]

# --- coord image (last = default for Fly.io) ---
FROM alpine:3.21 AS coord
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/tailbus-coord /usr/local/bin/tailbus-coord
COPY deploy/coord.toml /etc/tailbus/coord.toml
RUN mkdir -p /data
ENTRYPOINT ["tailbus-coord"]
CMD ["-config", "/etc/tailbus/coord.toml", "-health-addr", ":8081"]
