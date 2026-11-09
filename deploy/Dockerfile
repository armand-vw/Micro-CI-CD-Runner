# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27
FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

# git + CA certificates are only needed while compiling modules.
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/runner \
    ./cmd/runner

# Distroless keeps the runtime image tiny and free of a shell. The binary needs
# access to the Docker socket, which is mounted by docker-compose.
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/runner /runner

WORKDIR /data
EXPOSE 8080

ENTRYPOINT ["/runner"]
