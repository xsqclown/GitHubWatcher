# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/bot ./cmd/bot

RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/bot /app/bot
COPY --from=build --chown=65532:65532 /out/data /data

USER 65532:65532
ENTRYPOINT ["/app/bot"]
