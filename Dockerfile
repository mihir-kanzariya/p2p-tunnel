FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o p2p-tunnel ./cmd/client

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/p2p-tunnel /usr/local/bin/p2p-tunnel
EXPOSE 4443 8080
CMD ["p2p-tunnel", "relay", "-control", ":4443", "-http", ":8080", "-d", "placeholder.railway.app"]
