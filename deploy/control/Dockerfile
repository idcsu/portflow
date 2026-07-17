# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS build

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/portflow-control ./cmd/control

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/portflow-control /portflow-control
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/portflow-control"]
CMD ["-listen", "0.0.0.0:8080"]
