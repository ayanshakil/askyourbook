# Build stage: a static, CGO-free binary.
FROM golang:1.26.9-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/askyourbook ./cmd/server

# Runtime stage: distroless static has CA certificates (for the LLM APIs),
# no shell, and runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/askyourbook /askyourbook
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/askyourbook"]
