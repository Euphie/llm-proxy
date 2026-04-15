FROM golang:1.23-alpine AS builder
WORKDIR /app
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /anthropic-proxy ./cmd/anthropic-proxy

FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /anthropic-proxy /usr/local/bin/anthropic-proxy
COPY config.yaml /app/config.yaml
WORKDIR /app
EXPOSE 8080
ENV CONFIG_FILE=/app/config.yaml
ENTRYPOINT ["anthropic-proxy"]
