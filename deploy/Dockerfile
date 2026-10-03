# syntax=docker/dockerfile:1
# Сборка из корня репозитория: docker build -f deploy/Dockerfile .

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/listok ./cmd/listok \
 && mkdir -p /out/data

# distroless/static: без шелла и пакетов, пользователь nonroot (65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/listok /listok
# Каталог данных принадлежит nonroot: именованный том при первом создании унаследует владельца.
COPY --from=build --chown=65532:65532 /out/data /data
ENV LISTOK_ADDR=:8080 \
    LISTOK_DATA=/data
EXPOSE 8080
VOLUME /data
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/listok", "healthcheck"]
ENTRYPOINT ["/listok"]
