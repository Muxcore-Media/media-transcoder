FROM golang:1.26-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /build/module ./cmd/module

FROM alpine:3.21
RUN apk add --no-cache ffmpeg && adduser -D -h /data app
USER app
WORKDIR /app
COPY --from=builder /build/module ./media-transcoder
EXPOSE 9520
ENTRYPOINT ["./media-transcoder"]
