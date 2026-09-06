# Multi-stage Dockerfile for Toxitoken Edge Gateway (~15MB static image)

# Stage 1: Build static binaries
FROM golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/toxi ./cmd/toxi

# Stage 2: Minimal distroless scratch runtime
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/server /bin/server
COPY --from=builder /bin/toxi /bin/toxi

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/bin/server"]
