# alertmanager-deadman-notifier

An alertmanager receiver that only create notifications & tickets when an previously known alert stops reporting.

## Quickstart

Deadman-receiver should be deployed next to its alertmanager.

On Prometheus, create a "always-on" rule that will constantly raise alerts.

```yaml
- name: general.rules
  rules:
  - alert: Watchdog
    annotations:
      message: |
        This is an alert meant to ensure that the entire alerting pipeline is functional.
        This alert is always firing, therefore it should always be firing in Alertmanager
        and always fire against a receiver. There are integrations with various notification
        mechanisms that send a notification when this alert is not firing. For example the
        "DeadMansSnitch" integration in PagerDuty.
    expr: vector(1)
    labels:
      severity: none
```

On Alertmanager, route this watchdog alert to the deadman-receiver

```yaml
      config:
        route:
          routes:
          - match:
              alertname: Watchdog
              receiver: deadman-receiver

[...]

        receivers:
        - name: deadman-receiver
          webhook_configs:
          - url: http://deadman-receiver:8080/webhook
            send_resolved: false
```

On Deadman-receiver, configure your notification endpoints in ENV.

Be warned that deadman-receiver does not persist already known alerts through restart.
So there's still a chance of missing the loss of a watchdog signal after a restart. Feel free to submit a PR to add any sort of persistence (clustering, datastore, ...) if you need it.

## Configuration

## Details
