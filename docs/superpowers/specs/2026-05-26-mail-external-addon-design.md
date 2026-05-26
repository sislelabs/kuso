# mail-external addon (gap 7)

## Status

**Functionally available today** via `KusoAddon.spec.external.secretName`. Users can BYO Resend / SES / Postmark by creating a Secret with `SMTP_*` keys and pointing an addon at it. **Dedicated `kind=mail-external` UX is a follow-up.**

## What works today

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: tickero-resend
stringData:
  SMTP_HOST: smtp.resend.com
  SMTP_PORT: "465"
  SMTP_USERNAME: resend
  SMTP_PASSWORD: re_abc123…
---
apiVersion: application.kuso.sislelabs.com/v1alpha1
kind: KusoAddon
spec:
  project: tickero
  kind: mailpit          # placeholder kind
  external:
    secretName: tickero-resend
```

The server clones `tickero-resend` into `tickero-mail-conn` and every service in the project gets the SMTP vars via envFrom.

## What `kind=mail-external` would add

1. **A real enum value** so users don't have to put `mailpit` in their spec when they mean Resend.
2. **Provider-aware UI**: dropdown for `resend | ses | smtp`, conditional fields (Resend just wants `RESEND_API_KEY`; SES wants `AWS_*`).
3. **Connection test button**: POSTs a test email through the configured provider, surfaces success/failure.
4. **Quota visibility**: if `provider=resend`, surface monthly send-count from Resend's API on the addon card.

## Why deferred

The schema work is small. The UI work — provider switcher, test-send flow, quota fetcher — is real. And users *can* deploy Tickero today using the external-secret mechanism above. Shipping the kind=mail-external addon properly = 1 focused session.

## Migration when it ships

`external` addons authored with `kind=mailpit` continue to work — the new kind is purely additive. Server-side, the conn-secret synthesis is already provider-agnostic (it just clones whatever keys the source Secret has).
