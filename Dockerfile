# --- build -------------------------------------------------------------------
FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
# The SQLite driver is pure Go, so with cgo off the binary is static and runs on
# an image without libc.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/server ./cmd/server

# --- runtime -----------------------------------------------------------------
# distroless static: no shell, no package manager, ~2 MB, and it carries the root
# certificates the GitHub API client needs.
#
# Runs as root on purpose: Fly mounts the volume owned by root, and an
# unprivileged user could not write the SQLite file under /data. The isolation
# boundary here is the Firecracker microVM, not the container user.
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/server /server
COPY --from=build /src/config.yaml /config.yaml

ENV CONFIG_PATH=/config.yaml \
    DB_PATH=/data/portfolio.db \
    PORT=8080

EXPOSE 8080
ENTRYPOINT ["/server"]
