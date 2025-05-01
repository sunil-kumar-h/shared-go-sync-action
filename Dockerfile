FROM golang:1.21-alpine

WORKDIR /app
COPY . .

RUN go build -o /usr/local/bin/shared-go-sync shared-go-sync.go

ENTRYPOINT ["shared-go-sync"]
