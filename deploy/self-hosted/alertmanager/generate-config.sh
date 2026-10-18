#!/bin/sh
set -eu

# Keep the secret out of generated YAML: Alertmanager reads it from a file.
# The URL is operator-controlled and is never accepted from a request or user.
url="${ALERTMANAGER_WEBHOOK_URL:-}"
if [ -n "$url" ]; then
  case "$url" in
    https://*|http://*) ;;
    *) echo "ALERTMANAGER_WEBHOOK_URL must start with http:// or https://" >&2; exit 1 ;;
  esac
  case "$url" in
    *[[:space:]]*) echo "ALERTMANAGER_WEBHOOK_URL must not contain whitespace" >&2; exit 1 ;;
  esac
  printf '%s' "$url" > /tmp/alertmanager-webhook-url
  cat <<'YAML'
global:
  resolve_timeout: 5m
route:
  receiver: testagram-operator-webhook
  group_by: [alertname, severity]
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 4h
receivers:
  - name: testagram-operator-webhook
    webhook_configs:
      - url_file: /tmp/alertmanager-webhook-url
        send_resolved: true
        max_alerts: 20
        timeout: 10s
YAML
else
  rm -f /tmp/alertmanager-webhook-url
  cat <<'YAML'
global:
  resolve_timeout: 5m
route:
  receiver: notifications-not-configured
  group_by: [alertname, severity]
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 4h
receivers:
  - name: notifications-not-configured
YAML
fi
