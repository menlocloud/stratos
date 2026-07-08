# syntax=docker/dockerfile:1
# stratos-notifier: the OpenStack RabbitMQ -> Stratos webhook bridge (cmd/notifier).
# A pure network bridge — no psql, no shell — so it runs on distroless/static (nonroot).

FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/stratos-notifier ./cmd/notifier

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratos-notifier /usr/local/bin/stratos-notifier
EXPOSE 7476
ENTRYPOINT ["/usr/local/bin/stratos-notifier"]
