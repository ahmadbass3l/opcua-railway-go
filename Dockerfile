# ── Stage 1: build ───────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o opcua-railway-go .

# ── Stage 2: minimal runtime ──────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12

COPY --from=builder /app/opcua-railway-go /opcua-railway-go

EXPOSE 8080

ENTRYPOINT ["/opcua-railway-go"]
