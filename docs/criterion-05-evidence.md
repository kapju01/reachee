# Criterion 05: live evidence

Criterion 05 says that a broken promise is named as broken, and that a skipped
tier is signal: when a provider's breaker is open, the skip is written into the
record with its reason. It was run against the deployed stack in `us-west-2` on
24 October 2026 as two escalations 5 min 40 s apart, each starting in a fresh
execution environment.

| Run | Escalation id                          | Raised (UTC)             | Tier 1 receipt | `breaker_state` | Tier 1 cold start          |
|-----|----------------------------------------|--------------------------|----------------|-----------------|----------------------------|
| 1   | `01a16e49-4484-7c7d-b958-baa9fb242ea1` | 2026-10-24T14:20:58.004Z | `FAILED`       | `OPEN`          | `Init Duration: 170.78 ms` |
| 2   | `01a16e4e-76fb-7fde-9a7a-515083ae23c2` | 2026-10-24T14:26:38.605Z | `ESCALATED`    | `OPEN`          | `Init Duration: 164.60 ms` |

Escalation 1 found WhatsApp with no provider, said so in a `FAILED` receipt,
and opened the breaker. Escalation 2 arrived inside the fifteen-minute cooldown.
It did not touch WhatsApp, and wrote an `ESCALATED` receipt that names the
skip and its reason. Both escalations then reached the person on tier 2.

The breaker's position did not survive in memory. Each tier-1 attempt ran in a
newly initialized execution environment, so nothing from escalation 1's
environment was there for escalation 2. Escalation 2 found the breaker open
because it read the breaker record from the escalations table.

## Reproducing the verification

Escalation 2's chain is committed next to this file:

```
PUB=https://d1bskent938hm6.cloudfront.net/keys/reachee-ed25519.pub
python3 -I verify/verify_chain.py docs/evidence/c05-breaker-01a16e4e-76fb.json --pubkey $PUB
```

The status endpoint serves the same chain live:

```
curl "$API/escalation/01a16e4e-76fb-7fde-9a7a-515083ae23c2?format=bundle" \
  | python3 -I verify/verify_chain.py /dev/stdin --pubkey $PUB
```

The committed file was written by the chain exporter, reading DynamoDB
directly. The endpoint returns the same JSON, pretty-printed: the two parse to
identical objects.

## Conditions common to both

- State machine: `arn:aws:states:us-west-2:335019467476:stateMachine:reachee-escalation`
  (Step Functions Standard). The execution name is the escalation id.
- Recipient: Heart-Map contact `justin-kaplan`. Ladder: tier 1 WhatsApp, tier 2
  email (Amazon SES v2), tier 3 SMS. Tier 1 has no provider.
- Priority `P1`, raised in daylight (09:20 and 09:26 CDT), outside the
  Heart-Map's 22:00–07:00 quiet hours. Agent `rpm-vitals-monitor-07`, demo
  blood-pressure payload.
- Breaker cooldown: 900 s (`REACHEE_BREAKER_COOLDOWN_MS=900000`). A channel
  with no provider trips its breaker on the first attempt, with no failure
  threshold to wait for.
- Breaker record: `escalation_id = "#breaker/unprovisioned:whatsapp"`, `seq = 0`
  in `reachee-escalations`. Every write is conditional on the version read.
- Cold starts were forced before each raise by changing the `reachee-attempt`
  function's description. That makes Lambda discard its execution
  environments. Tier 1 was the first invocation after each change. Tier 2 ran
  within a second of it, in the same warm environment, so its REPORT line has
  no `Init Duration`.
- Neither escalation was answered. Both ended in `TimedOut` with `TIMED_OUT`,
  `released "false"`, after the 900 s window.

Times are UTC. The deployment's Heart-Map time zone is America/Chicago (CDT, UTC−5).

## Breaker record, before and after

| When | Read at (approximate) | Record |
|------|---------|--------|
| Before escalation 1 | about 14:20:40Z, consistent read | absent (version 0) |
| After escalation 1 | about 14:21:15Z, consistent read | version `1`, state `OPEN`, updated_at `2026-10-24T14:20:58.610589836Z` |
| After escalation 2 | about 14:42Z, consistent read | version `1`, state `OPEN`, updated_at `2026-10-24T14:20:58.610589836Z` |

Escalation 1 created the record with a conditional first write
(`attribute_not_exists(seq)`). Escalation 2 read it, found it open with
cooldown left, and wrote nothing, because reading an open breaker is not a
transition. The record as of the last read:

```
{"state":"OPEN","consecutive":0,"opened_at":"2026-10-24T14:20:58.610524529Z","probing":false,"probe_at":"0001-01-01T00:00:00Z","observations":[{"at":"2026-10-24T14:20:58.61052407Z","success":false}]}
```

Escalation 2's skip came 5 min 40.5 s after `opened_at`, well inside the 15 min
cooldown. An earlier attempt at this run, `01a16c10-b270-76c4-b318-d3855266d7ce`
at 22:59 CDT on 23 October, fell in quiet hours. It was `DEFERRED` before any
provider or breaker was touched. That is why no record existed before
escalation 1.

## 1. The broken promise is named and opens the breaker

- Escalation id: `01a16e49-4484-7c7d-b958-baa9fb242ea1`
- Execution ARN: `arn:aws:states:us-west-2:335019467476:execution:reachee-escalation:01a16e49-4484-7c7d-b958-baa9fb242ea1`
- Status: `SUCCEEDED`
- Started: 2026-10-24T14:20:58.004Z
- Stopped: 2026-10-24T14:35:59.820Z
- Path: `Attempt → RouteAttempt → NextTier → Attempt → RouteAttempt → AwaitAnswer → TimedOut`
- Tier 1 attempt returned `{"status":"FAILED","next_tier":2,"timeout_seconds":300}`.
- Reply slot:
  - Armed 14:20:59.491Z.
  - Link expiry 14:35:58.778Z.
  - Closed 14:35:59.695Z with reason `timeout`.
  - Never spent.

| # | Type        | Timestamp                    | Event id                               | Metadata |
|---|-------------|------------------------------|----------------------------------------|----------|
| 1 | `PROMISED`  | 2026-10-24T14:20:57.899439Z  | `01a16e49-44ab-7d45-9fdb-d9b4455df1e7` | promise "attempt delivery on the declared ladder in order and report each outcome"; agent `rpm-vitals-monitor-07` |
| 2 | `FAILED`    | 2026-10-24T14:20:58.630836Z  | `01a16e49-4786-7421-a606-ea6372f7472e` | tier 1, whatsapp, **breaker `OPEN`**, "promise broken: no provider registered for channel whatsapp" |
| 3 | `SENT`      | 2026-10-24T14:20:58.976846Z  | `01a16e49-48e0-7655-b034-433b26efc79e` | tier 2, email, provider `ses-v2`, provider_msg_id `010101a16e494863-37b6f615-1039-474c-8ab8-9946ee3beb34-000000` |
| 4 | `TIMED_OUT` | 2026-10-24T14:35:59.770708Z  | `01a16e57-079a-709c-8f8e-0284b76e13b9` | released `false`, "no answer within the declared window; a timeout is not consent" |

Every receipt also carries `priority P1` and `correlation_hash
867409b320d99da83b521818321c3d845149610c618dfa62d863b0ae6b3c2354`, except
`TIMED_OUT`. `SENT` also carries `empathy_source fallback` and `empathy_reason
"no model configured"`.

Tier 1 invocation `6b098f03-3009-4543-838e-c04c9064dd14`, REPORT line verbatim
from `/aws/lambda/reachee-attempt`:

```
REPORT RequestId: 6b098f03-3009-4543-838e-c04c9064dd14	Duration: 150.41 ms	Billed Duration: 322 ms	Memory Size: 128 MB	Max Memory Used: 35 MB	Init Duration: 170.78 ms
```

## 2. The open breaker skips the tier and says why

- Escalation id: `01a16e4e-76fb-7fde-9a7a-515083ae23c2`
- Execution ARN: `arn:aws:states:us-west-2:335019467476:execution:reachee-escalation:01a16e4e-76fb-7fde-9a7a-515083ae23c2`
- Status: `SUCCEEDED`
- Started: 2026-10-24T14:26:38.605Z
- Stopped: 2026-10-24T14:41:40.040Z
- Path: `Attempt → RouteAttempt → NextTier → Attempt → RouteAttempt → AwaitAnswer → TimedOut`
- Tier 1 attempt returned `{"status":"SKIPPED","next_tier":2,"timeout_seconds":300}`.
  No provider was called.
- Reply slot:
  - Armed 14:26:39.626Z.
  - Link expiry 14:41:39.278Z.
  - Closed 14:41:39.626Z with reason `timeout`.
  - Never spent.

| # | Type        | Timestamp                    | Event id                               | Metadata |
|---|-------------|------------------------------|----------------------------------------|----------|
| 1 | `PROMISED`  | 2026-10-24T14:26:38.488001Z  | `01a16e4e-7717-7927-b33a-3aa2a8c87911` | promise "attempt delivery on the declared ladder in order and report each outcome"; agent `rpm-vitals-monitor-07` |
| 2 | `ESCALATED` | 2026-10-24T14:26:39.156301Z  | `01a16e4e-79b4-7208-b953-fccf6cc2b5fa` | tier 1, whatsapp, **breaker `OPEN`**, "circuit breaker is OPEN, tier skipped" |
| 3 | `SENT`      | 2026-10-24T14:26:39.439350Z  | `01a16e4e-7acf-7ce0-aadb-6233e0c80270` | tier 2, email, provider `ses-v2`, provider_msg_id `010101a16e4e7a6b-d0693b86-aac2-47f0-813a-fb7bbaa088c2-000000` |
| 4 | `TIMED_OUT` | 2026-10-24T14:41:39.968846Z  | `01a16e5c-3880-7e64-b43b-5aa27d8a942e` | released `false`, "no answer within the declared window; a timeout is not consent" |

Every receipt also carries `priority P1` and `correlation_hash
026224c78727a35825138089249b569dcfebca7a4a6486274e4bd41e48ab9c7f`, except
`TIMED_OUT`. `SENT` also carries `empathy_source fallback` and `empathy_reason
"no model configured"`.

The tier 1 receipt, verbatim as signed:

```json
{
  "event_id": "01a16e4e-79b4-7208-b953-fccf6cc2b5fa",
  "event_type": "ESCALATED",
  "message_hash": "bcd63ab199fc9a403c5f439b588747b0ef7972685ee13b17f11736993fcf24eb",
  "metadata": {
    "breaker_state": "OPEN",
    "channel": "whatsapp",
    "correlation_hash": "026224c78727a35825138089249b569dcfebca7a4a6486274e4bd41e48ab9c7f",
    "priority": "P1",
    "reason": "circuit breaker is OPEN, tier skipped",
    "tier": "1"
  },
  "prev_hash": "fa1ef9728e4517f16dd98ae951f423dc86299323cb815a00ac58d3c8bd61e9ca",
  "provider_name": "",
  "recipient_id": "justin-kaplan",
  "request_id": "01a16e4e-76fb-7fde-9a7a-515083ae23c2",
  "signature": "857173a763708976b97f5f94f1ee349576e034e0c633200f7aac0f8966619aa71355713af90c6166bb7ee4633e83092d868d41a76f9ee88051f3b4c4b6744700",
  "timestamp": "2026-10-24T14:26:39.156301Z"
}
```

`provider_name` is empty because no provider was called.

Tier 1 invocation `f0efb319-9a98-4fbc-951c-95767d14712d`, REPORT line verbatim
from `/aws/lambda/reachee-attempt`:

```
REPORT RequestId: f0efb319-9a98-4fbc-951c-95767d14712d	Duration: 67.80 ms	Billed Duration: 233 ms	Memory Size: 128 MB	Max Memory Used: 39 MB	Init Duration: 164.60 ms
```

Verifier output for the committed bundle:

```
verifying key: 85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e
key source:    https://d1bskent938hm6.cloudfront.net/keys/reachee-ed25519.pub
events:        4

  01a16e4e-771 PROMISED       signature MATCH  chain LINKED
  01a16e4e-79b ESCALATED      signature MATCH  chain LINKED
  01a16e4e-7ac SENT           signature MATCH  chain LINKED
  01a16e5c-388 TIMED_OUT      signature MATCH  chain LINKED

no field in this bundle is long enough to hold a message body

CHAIN VERIFIED: 4 events, every signature and every link
```

## Reading the evidence

- `FAILED` and `ESCALATED` are different claims. `FAILED` means the tier was
  attempted and the promise broke. `ESCALATED` means the tier was not attempted
  because its breaker was open. Neither is retried into silence, and neither is
  reported as a delivery.
- `Init Duration` appears in a Lambda REPORT line only for the first
  invocation in a newly initialized execution environment. Both tier 1
  invocations carry one, so neither could have inherited breaker state from
  memory.
- What the chain proves and what the logs show are separate. The chain proves
  the receipts, and verifies without our cooperation. The cold starts and the
  breaker record come from CloudWatch Logs and DynamoDB in our account, so
  they are reported here, not verifiable from the bundle.
