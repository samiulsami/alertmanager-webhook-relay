# alertmanager-relay

Receives Alertmanager webhooks, renders them into a simple `{"text":"..."}` payload, and fans them out to one or more configured downstream URLs.

## Endpoints

```text
Alertmanager -> alertmanager-relay -> <webhook endpoint(s)>
```

- `POST /v1/ingest/webhook`
- `GET /healthz`
- `GET /metrics`

It only returns `2xx` after every downstream webhook returns `2xx` or the target was already successfully delivered for the same payload.

## Why

Alertmanager's generic webhook sends an Alertmanager-specific JSON envelope. Several chat webhook products accept a much simpler JSON body with a top-level `text` field instead.

Docs:

- Slack incoming webhooks: <https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks>
- Google Chat incoming webhooks: <https://developers.google.com/workspace/chat/quickstart/webhooks>
- Mattermost incoming webhooks: <https://developers.mattermost.com/integrate/webhooks/incoming/>
- Rocket.Chat incoming integrations: <https://docs.rocket.chat/docs/integrations>
- Webex incoming webhooks: <https://apphub.webex.com/applications/incoming-webhooks-cisco-systems-38054-23307-75252>
- Alertmanager webhook receiver config: <https://prometheus.io/docs/alerting/latest/configuration/#webhook_config>
- Alertmanager notification data / webhook payload fields: <https://prometheus.io/docs/alerting/latest/notifications/>

This repo is a workaround until official support for `AlertmanagerConfig.webhookConfigs.payload` is merged in Prometheus Operator here:

- <https://github.com/prometheus-operator/prometheus-operator/pull/8507>

After that, the payload should be rendered directly in Alertmanager and this repo should be archived.

## Scope

Included:

- standard Alertmanager webhook input
- plain text `{"text":"..."}` output
- multiple downstream webhook URLs
- per-target deduplication for partially successful fanout retries
- no success response before downstream success

Not included:

- cards or rich formatting
- queues
- internal retries
- production deployment wiring

## Config

Required:

- `WEBHOOK_URLS`

Optional:

- `LISTEN_ADDR` default `:8080`
- `REQUEST_TIMEOUT` default `5s`
- `SEND_RESOLVED` default `true`
- `DEDUPE_CACHE_SIZE` default `10000`

`WEBHOOK_URLS` accepts one or more absolute URLs separated by commas or newlines.

- `WEBHOOK_URL` still works for a single URL

Default namespace for deployment: `monitoring`

## Alertmanager Webhook Configuration

Alertmanager should send to the relay service, not to the downstream webhook URL.

Default in-cluster URL:

```text
http://alertmanager-relay.monitoring.svc.cluster.local:8080/v1/ingest/webhook
```

Same-namespace short form:

```text
http://alertmanager-relay:8080/v1/ingest/webhook
```

## Delivery Semantics

For each incoming Alertmanager payload, the relay:

- renders one plain text message body
- attempts delivery to every configured downstream URL
- remembers successful deliveries per downstream URL using a SHA-512 hash of the original Alertmanager payload
- returns `502` if any downstream target fails

This means a later retry from Alertmanager only re-sends to the targets that did not previously succeed.

## Commands

```bash
make build
make test
make fmt
make lint
REGISTRY=sami7786 make push
REGISTRY=sami7786 make deploy
```

`deploy` uses the secret `alertmanager-relay-webhook-urls` in `$(K8S_NAMESPACE)`.

To create or update that secret during deploy:

```bash
REGISTRY=sami7786 WEBHOOK_URLS="https://chat.googleapis.com/...,https://hooks.slack.com/services/..." make deploy
```
