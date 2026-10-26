# Criterion 09: live evidence

Criterion 09 says that a person's direction releases the held action, and that
a denial and a timeout do not. Each of the three outcomes was run once against
the deployed stack in `us-west-2` on 26 October 2026, under the deployed
Heart-Map policy, after commit `cf81e68` was deployed. Every receipt below was
written by that code. All three chains verify against the published key with
the independent verifier.

| Outcome   | Escalation id                          | Terminal state | Terminal receipt | `released` |
|-----------|----------------------------------------|----------------|------------------|------------|
| Direction | `01a17829-dfaf-7418-8022-4d7124bc9c64` | `Release`      | `RESOLVED`       | `true`     |
| Denial    | `01a17825-4314-73ea-8300-60838de1e3bc` | `Denied`       | `DENIED`         | `false`    |
| Timeout   | `01a17849-b46a-72c6-b83b-10b10475055b` | `TimedOut`     | `TIMED_OUT`      | `false`    |

No chain contains a release receipt other than the direction run's `RESOLVED`.
The denial chain and the timeout chain contain no `RESOLVED` at all.

## Tier 1 takes both of its paths

Tier 1 (WhatsApp) has no provider, so every run fails there and moves on to
tier 2. How it fails depends on the breaker, which is kept in the table and
holds across invocations:

- **`FAILED`, breaker `OPEN`** (denial, timeout). The breaker was past its
  cooldown, so the attempt was let through, found no provider, wrote the
  broken promise, and opened the breaker again.
- **`ESCALATED`, breaker `OPEN`** (direction). It was raised five minutes after
  the denial re-opened the breaker, inside the cooldown, so the tier was
  skipped without an attempt and the skip was written with its reason.

The receipts differ on purpose, and the outcome does not depend on which path
tier 1 took. Together with `c05-breaker-01a16e4e-76fb.json`, a timeout on the
skip path, the committed set shows both tier-1 paths across different outcomes.

## Reproducing the verification

The exported chains are committed next to this file. Anyone can check them
without access to our account:

```
PUB=https://d1bskent938hm6.cloudfront.net/keys/reachee-ed25519.pub
python3 -I verify/verify_chain.py docs/evidence/c09-direction-01a17829-dfaf.json --pubkey $PUB
python3 -I verify/verify_chain.py docs/evidence/c09-denial-01a17825-4314.json --pubkey $PUB
python3 -I verify/verify_chain.py docs/evidence/c09-timeout-01a17849-b46a.json --pubkey $PUB
```

The verifying key is
`85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e`, the same as
`demo/keys/reachee-ed25519.pub`, which `make criteria` and the output below
use. Each bundle carries the full event: hashes, `prev_hash` links and
signatures. The tables below show the receipt sequence; the hashes and
signatures they omit are in the bundles.

## Conditions common to all three

- State machine: `arn:aws:states:us-west-2:335019467476:stateMachine:reachee-escalation`
  (Step Functions Standard). The execution name is the escalation id.
- Recipient: Heart-Map contact `justin-kaplan`.
- Ladder: tier 1 WhatsApp, tier 2 email (Amazon SES v2), tier 3 SMS. Tier 3 is
  never reached: tier 2 delivers, and its timeout ends the escalation.
- The tier 2 answer window is the Heart-Map's `timeout_seconds` of 900.
  `AwaitAnswer` read it from the attempt's output through `TimeoutSecondsPath`,
  and each execution history shows `timeoutInSeconds: 900`. The reply link
  expires at the same moment.
- Each answer was given through the emailed reply link.
- Priority `P1` and agent `rpm-vitals-monitor-07` throughout. Each run was
  raised with a correlation id, which every receipt carries only as
  `correlation_hash`, its SHA-256.

Times are UTC. The deployment's Heart-Map time zone is America/Chicago (CDT, UTC−5).

## 1. Direction releases

- Escalation id: `01a17829-dfaf-7418-8022-4d7124bc9c64`
- Bundle: `docs/evidence/c09-direction-01a17829-dfaf.json`
- Execution ARN: `arn:aws:states:us-west-2:335019467476:execution:reachee-escalation:01a17829-dfaf-7418-8022-4d7124bc9c64`
- Status: `SUCCEEDED`
- Started: 2026-10-26T12:22:52.814Z
- Stopped: 2026-10-26T12:23:22.967Z
- Path: `Attempt → RouteAttempt → NextTier → Attempt → RouteAttempt → AwaitAnswer → DecideAnswer → Release`
- Reply slot: armed 12:22:53.603Z, link expiry 12:37:53.255Z, nonce spent 12:23:22.673Z, decision `direction`.
- The clinician pressed Give direction and wrote a note. The classifier
  checked the note against its word list for a refusal and found none, which
  is all its confidence value claims. The chain holds only the note's hash.

| # | Type           | Timestamp                    | Event id                               | Metadata |
|---|----------------|------------------------------|----------------------------------------|----------|
| 1 | `PROMISED`     | 2026-10-26T12:22:52.730853Z  | `01a17829-e03a-7929-8484-09aebe0f9ace` | promise "attempt delivery on the declared ladder in order and report each outcome"; agent `rpm-vitals-monitor-07`; correlation_hash `694ad9fc4b88…` |
| 2 | `ESCALATED`    | 2026-10-26T12:22:53.135982Z  | `01a17829-e1cf-7ee4-ac64-f7082015185e` | tier 1, whatsapp, breaker `OPEN`, "circuit breaker is OPEN, tier skipped" |
| 3 | `SENT`         | 2026-10-26T12:22:53.468123Z  | `01a17829-e31c-7519-9c92-daa2428d4d5d` | tier 2, email, provider `ses-v2`, provider_msg_id `010101a17829e29e-f51d27ce-9f93-4825-8f52-dd56ce419d9b-000000`, empathy_source `fallback` |
| 4 | `RECEIVED`     | 2026-10-26T12:23:22.683749Z  | `01a1782a-553b-762e-8318-21eb82fab9cb` | via reply link |
| 5 | `ACKNOWLEDGED` | 2026-10-26T12:23:22.703936Z  | `01a1782a-554f-76dd-992d-31208c5e73ae` | intent `direction`, confidence "button; note contains no contradicting term", classifier "button and text, rules v1" |
| 6 | `RESOLVED`     | 2026-10-26T12:23:22.912063Z  | `01a1782a-5620-7591-8157-3a179a6769bb` | **released `true`**, intent `direction`, confidence "button; note contains no contradicting term" |

```
verifying key: 85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e
key source:    demo/keys/reachee-ed25519.pub
events:        6

  01a17829-e03 PROMISED       signature MATCH  chain LINKED
  01a17829-e1c ESCALATED      signature MATCH  chain LINKED
  01a17829-e31 SENT           signature MATCH  chain LINKED
  01a1782a-553 RECEIVED       signature MATCH  chain LINKED
  01a1782a-554 ACKNOWLEDGED   signature MATCH  chain LINKED
  01a1782a-562 RESOLVED       signature MATCH  chain LINKED

no field in this bundle is long enough to hold a message body

CHAIN VERIFIED: 6 events, every signature and every link
```

## 2. Denial does not release

- Escalation id: `01a17825-4314-73ea-8300-60838de1e3bc`
- Bundle: `docs/evidence/c09-denial-01a17825-4314.json`
- Execution ARN: `arn:aws:states:us-west-2:335019467476:execution:reachee-escalation:01a17825-4314-73ea-8300-60838de1e3bc`
- Status: `SUCCEEDED`. The execution completes, but the held action is not released.
- Started: 2026-10-26T12:17:50.512Z
- Stopped: 2026-10-26T12:20:45.203Z
- Path: `Attempt → RouteAttempt → NextTier → Attempt → RouteAttempt → AwaitAnswer → DecideAnswer → Denied`
- Reply slot: armed 12:17:51.510Z, link expiry 12:32:51.205Z, nonce spent 12:20:44.957Z, decision `denial`.
- The clinician pressed Deny and wrote a note. The classifier checked the
  note against its word list for an approval and found none. The answer is
  classified from the button, and the receipt names exactly that check.

| # | Type           | Timestamp                    | Event id                               | Metadata |
|---|----------------|------------------------------|----------------------------------------|----------|
| 1 | `PROMISED`     | 2026-10-26T12:17:50.427876Z  | `01a17825-435b-7d0a-9f31-6cb2d104f003` | promise "attempt delivery on the declared ladder in order and report each outcome"; agent `rpm-vitals-monitor-07`; correlation_hash `230fb7ed8bee…` |
| 2 | `FAILED`       | 2026-10-26T12:17:51.079780Z  | `01a17825-45e7-72f0-b2e8-36740538ee3c` | tier 1, whatsapp, breaker `OPEN`, "promise broken: no provider registered for channel whatsapp" |
| 3 | `SENT`         | 2026-10-26T12:17:51.384560Z  | `01a17825-4718-7917-b6e5-35cc1368874b` | tier 2, email, provider `ses-v2`, provider_msg_id `010101a1782546ad-0675f766-a2b2-48fe-87fd-9c24ceca36e5-000000`, empathy_source `fallback` |
| 4 | `RECEIVED`     | 2026-10-26T12:20:44.964927Z  | `01a17827-ed24-7388-8c98-3316c5472149` | via reply link |
| 5 | `ACKNOWLEDGED` | 2026-10-26T12:20:44.970792Z  | `01a17827-ed2a-7048-95dd-ff5b90f7b5d0` | intent `denial`, confidence "button; note contains no contradicting term", classifier "button and text, rules v1" |
| 6 | `DENIED`       | 2026-10-26T12:20:45.158704Z  | `01a17827-ede6-78c1-a817-ed422ad45ea0` | **released `false`**, intent `denial`, confidence "button; note contains no contradicting term", "the person denied the held action" |

```
verifying key: 85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e
key source:    demo/keys/reachee-ed25519.pub
events:        6

  01a17825-435 PROMISED       signature MATCH  chain LINKED
  01a17825-45e FAILED         signature MATCH  chain LINKED
  01a17825-471 SENT           signature MATCH  chain LINKED
  01a17827-ed2 RECEIVED       signature MATCH  chain LINKED
  01a17827-ed2 ACKNOWLEDGED   signature MATCH  chain LINKED
  01a17827-ede DENIED         signature MATCH  chain LINKED

no field in this bundle is long enough to hold a message body

CHAIN VERIFIED: 6 events, every signature and every link
```

## 3. Timeout does not release

- Escalation id: `01a17849-b46a-72c6-b83b-10b10475055b`
- Bundle: `docs/evidence/c09-timeout-01a17849-b46a.json`
- Execution ARN: `arn:aws:states:us-west-2:335019467476:execution:reachee-escalation:01a17849-b46a-72c6-b83b-10b10475055b`
- Status: `SUCCEEDED`. The execution completes, but the held action is not released.
- Started: 2026-10-26T12:57:38.794Z
- Stopped: 2026-10-26T13:12:40.837Z
- Path: `Attempt → RouteAttempt → NextTier → Attempt → RouteAttempt → AwaitAnswer → TimedOut`
- Reply slot: armed 2026-10-26T12:57:40.577Z, expiry 2026-10-26T13:12:39.671Z; the nonce was never spent, and the slot closed at 13:12:40.708Z with reason `timeout`.

| # | Type        | Timestamp                    | Event id                               | Metadata |
|---|-------------|------------------------------|----------------------------------------|----------|
| 1 | `PROMISED`  | 2026-10-26T12:57:38.708927Z  | `01a17849-b494-79af-8bc2-957c5b75c45c` | promise "attempt delivery on the declared ladder in order and report each outcome"; agent `rpm-vitals-monitor-07`; correlation_hash `cac9c2f33352…` |
| 2 | `FAILED`    | 2026-10-26T12:57:39.578073Z  | `01a17849-b7fa-7b17-b54a-b6650ce35bf6` | tier 1, whatsapp, breaker `OPEN`, "promise broken: no provider registered for channel whatsapp" |
| 3 | `SENT`      | 2026-10-26T12:57:40.062384Z  | `01a17849-b9de-77ed-a410-e3a66366a62a` | tier 2, email, provider `ses-v2`, provider_msg_id `010101a17849b8a4-5fa81135-eabd-44f7-a95c-374252dfe5bb-000000`, empathy_source `fallback` |
| 4 | `TIMED_OUT` | 2026-10-26T13:12:40.773170Z  | `01a17857-7845-7c88-9ad9-50cb6648113a` | **released `false`**, "no answer within the declared window; a timeout is not consent" |

There is no `RECEIVED`, `ACKNOWLEDGED` or `RESOLVED` in this chain.

```
verifying key: 85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e
key source:    demo/keys/reachee-ed25519.pub
events:        4

  01a17849-b49 PROMISED       signature MATCH  chain LINKED
  01a17849-b7f FAILED         signature MATCH  chain LINKED
  01a17849-b9d SENT           signature MATCH  chain LINKED
  01a17857-784 TIMED_OUT      signature MATCH  chain LINKED

no field in this bundle is long enough to hold a message body

CHAIN VERIFIED: 4 events, every signature and every link
```

## Reading the evidence

- `SENT` records only that SES accepted the message. The `provider_msg_id` is
  the id SES returned, not one we generated.
- `RECEIVED` records that a person answered through the link. `ACKNOWLEDGED`
  records how that answer was classified. Delivery receipts stand for neither.
- `confidence` names the check the classifier made, not a judgment it cannot
  make: `button` when there is no note, and "button; note contains no
  contradicting term" when a note was checked against the opposite button's
  word list.
- `released` appears only on terminal receipts, and only the direction run has
  `true`.
- Metadata values are strings in the signed canonical form, so `released` is
  the string `"true"` or `"false"`.
- No receipt carries the message body, the answer text, or the caller's
  correlation id. `message_hash` is a SHA-256 content hash, and the verifier
  checks that no field is long enough to hold a body.
