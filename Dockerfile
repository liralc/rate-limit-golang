# syntax=docker/dockerfile:1

FROM golang:1.24 AS base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Test stage: docker compose run --rm tests
FROM base AS test
CMD ["go", "test", "-race", "-count=1", "-v", "./..."]

FROM base AS build
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
