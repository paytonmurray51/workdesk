# syntax=docker/dockerfile:1

# 1. Frontend: Preact + Vite -> web/dist
FROM node:24.22.0-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. Server: static Go binary with the frontend embedded
FROM golang:1.27.2-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN go test ./... \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/workdesk ./cmd/workdesk \
 && mkdir -p /out/data

# 3. Runtime: distroless static, non-root (uid 65532)
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/workdesk /workdesk
COPY --from=server --chown=65532:65532 /out/data /data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/workdesk", "healthcheck"]
ENTRYPOINT ["/workdesk"]
