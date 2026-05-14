FROM golang:1.25 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/alertmanager-relay ./cmd/alertmanager-relay

FROM alpine:3.22

RUN adduser -D -H -u 10001 app

USER 10001

COPY --from=builder /out/alertmanager-relay /usr/local/bin/alertmanager-relay

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/alertmanager-relay"]
