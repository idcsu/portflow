# syntax=docker/dockerfile:1
FROM golang:1.23-alpine

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
ENTRYPOINT ["go", "test", "-tags", "integration", "-count=1", "-v", "./internal/store/postgres"]
