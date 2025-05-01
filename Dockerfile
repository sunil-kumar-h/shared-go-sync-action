FROM golang:1.21-alpine

WORKDIR /app

# Install git (needed for go mod download)
RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o shared-go-sync shared-go-sync.go

ENTRYPOINT ["/app/shared-go-sync"]
