## Build stage
FROM golang:1.22 AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/app ./cmd/server

## Runtime stage
FROM alpine:3.20
RUN apk --no-cache add ca-certificates poppler-utils
WORKDIR /app
COPY --from=builder /out/app /app/app
EXPOSE 8080
CMD ["/app/app"]