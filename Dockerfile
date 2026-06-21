# ── Frontend (React / Vite) ──────────────────────────────────────────────────
FROM node:20-alpine AS frontend
WORKDIR /app/client
COPY client/package.json client/package-lock.json* ./
RUN npm install
COPY client/ ./
RUN npm run build

# ── Backend (Go) ─────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS backend
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=frontend /app/client/dist ./client/dist
# Pure-Go static binary — no CGO needed.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o photoshare .

# ── Runtime ──────────────────────────────────────────────────────────────────
FROM alpine:3.20
# ffmpeg → video thumbnails/playback; libheif-tools → heif-convert for HEIC
# decode; mailcap → /etc/mime.types so static files get correct Content-Type
RUN apk add --no-cache ffmpeg libheif-tools mailcap ca-certificates tzdata
WORKDIR /app
COPY --from=backend /app/photoshare .

# Container-friendly defaults (override in compose / `docker run -e`)
ENV PHOTO_DIR=/photos \
    DATA_DIR=/config \
    PORT=8080 \
    HTTP_ONLY=true

# /photos = your library (read/write), /config = persisted config/log/cert
VOLUME ["/photos", "/config"]
EXPOSE 8080

CMD ["./photoshare"]
