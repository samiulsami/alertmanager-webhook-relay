FROM golang:1.26 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/alertmanager-webhook-relay ./cmd/alertmanager-webhook-relay

FROM alpine:3.22.4

USER 10001:10001

COPY --from=builder /out/alertmanager-webhook-relay /usr/local/bin/alertmanager-webhook-relay

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/alertmanager-webhook-relay"]
