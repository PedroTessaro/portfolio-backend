# Vercel builds and runs cmd/server directly. This image is for
# self-hosting the same binary anywhere else.

# --- build -------------------------------------------------------------------
FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.buildVersion=${VERSION}" \
    -o /out/server ./cmd/server

# --- runtime -----------------------------------------------------------------
# distroless static: no shell, no package manager, and the root certificates the
# GitHub and Redis clients need. The config is embedded in the binary, so there
# is nothing else to copy.
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/server /server

ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
