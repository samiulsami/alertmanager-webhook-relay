# alertmanager-relay

Receives Alertmanager webhooks, translates them into the format required by unsupported endpoints, and forwards them to the configured URL.

***Note: Currently only supports Google Chat.***

## Endpoints

```text
Alertmanager -> alertmanager-relay -> <unsupported endpoint>
```

- `POST /v1/ingest/google-chat`
- `GET /healthz`
- `GET /metrics`

It only returns `2xx` after the downstream endpoint returns `2xx`.

## Why

Google Chat expects a different request format than Alertmanager's generic webhook sends.

Docs:

- Google Chat incoming webhooks: <https://developers.google.com/workspace/chat/quickstart/webhooks>
- Google Chat `Message` schema: <https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces.messages>
- Alertmanager webhook receiver config: <https://prometheus.io/docs/alerting/latest/configuration/#webhook_config>
- Alertmanager notification data / webhook payload fields: <https://prometheus.io/docs/alerting/latest/notifications/>

This repo is a workaround until official support for `AlertmanagerConfig.webhookConfigs.payload` is merged in Prometheus Operator here:

- <https://github.com/prometheus-operator/prometheus-operator/pull/8507>

After that, the payload should be rendered directly in Alertmanager and this repo should be archived.

## Scope

Included:

- standard Alertmanager webhook input
- plain text Google Chat output
- no success response before downstream success

Not included:

- cards or rich formatting
- queues
- internal retries
- production deployment wiring

## Config

Required:

- `GOOGLE_CHAT_WEBHOOK_URL`

Optional:

- `LISTEN_ADDR` default `:8080`
- `REQUEST_TIMEOUT` default `5s`
- `SEND_RESOLVED` default `true`

Default namespace for deployment: `monitoring`

## Alertmanager Webhook Configuration

Alertmanager should send to the relay service, not to the Google Chat webhook URL.

Default in-cluster URL:

```text
http://alertmanager-relay.monitoring.svc.cluster.local:8080/v1/ingest/google-chat
```

Same-namespace short form:

```text
http://alertmanager-relay:8080/v1/ingest/google-chat
```

## Commands

```bash
make build
make test
make fmt
make lint
REGISTRY=sami7786 make push
REGISTRY=sami7786 make deploy
```

`deploy` uses the existing secret `alertmanager-relay-google-chat` in `$(K8S_NAMESPACE)`.

To create or update that secret during deploy:

```bash
REGISTRY=sami7786 GOOGLE_CHAT_WEBHOOK_URL="https://chat.googleapis.com/..." make deploy
```
