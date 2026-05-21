FROM golang:1.25-alpine AS builder
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /zai2api ./cmd/main.go

FROM golang:1.25-alpine
WORKDIR /app
COPY --from=builder /zai2api .
EXPOSE 8000
CMD ["./zai2api"]
