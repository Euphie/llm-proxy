FROM golang:1.25-alpine AS builder
WORKDIR /app
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /llm-proxy ./cmd/llm-proxy

FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /llm-proxy /usr/local/bin/llm-proxy
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["llm-proxy"]
