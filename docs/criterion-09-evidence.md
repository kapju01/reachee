# Criterion 09: live evidence

Criterion 09 says that a person's direction releases the held action, and that a
denial and a timeout do not. Each of the three outcomes was run once against the deployed
stack in `us-west-2` on 23 October 2026, under the production Heart-Map policy.
All three chains verify against the published key with the independent
verifier.

| Outcome   | Escalation id                          | Terminal state | Terminal receipt | `released` |
|-----------|----------------------------------------|----------------|------------------|------------|
| Direction | `01a16b5f-f8a7-747a-a48b-48300767bc42` | `Release`      | `RESOLVED`       | `true`     |
| Denial    | `01a16b4b-259b-770b-b6ad-252d80b2b577` | `Denied`       | `DENIED`         | `false`    |
| Timeout   | `01a16a5a-10cc-73d2-8241-a34d3bbfc712` | `TimedOut`     | `TIMED_OUT`      | `false`    |

No chain contains a release receipt other than the direction run's `RESOLVED`.
The denial chain and the timeout chain contain no `RESOLVED` at all.

The direction run uses the demo scenario, because it is the run the walkthrough
and the video show. A remote patient monitoring agent saw a single home
blood-pressure reading far outside the patient's thirty-day band, and the
readings after it were back inside it. The agent asked the clinician to direct
patient outreach. The denial and timeout runs prove properties where the
scenario does not matter: a denial does not release, and a timeout does not
release. They were raised with a test payload.

## Reproducing the verification

The exported chains are committed next to this file. Anyone can check them
without access to our account:

```
PUB=https://d1bskent938hm6.cloudfront.net/keys/reachee-ed25519.pub
python3 -I verify/verify_chain.py docs/evidence/c09-direction-01a16b5f-f8a7.json --pubkey $PUB
python3 -I verify/verify_chain.py docs/evidence/c09-denial-01a16b4b-259b.json    --pubkey $PUB
python3 -I verify/verify_chain.py docs/evidence/c09-timeout-01a16a5a-10cc.json   --pubkey $PUB
```

The verifying key is
`85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e`, the same as
`demo/keys/reachee-ed25519.pub`. Each bundle carries the full event: hashes,
`prev_hash` links and signatures. The tables below show the receipt sequence;
the hashes and signatures they omit are in the bundles.

All three runs predate the change that replaced `correlation_id` in receipts
with `correlation_hash`, the SHA-256 of the caller's id. Their receipts carry
the plaintext correlation ids shown in the tables below. The bundles are left
exactly as exported; regenerating them would replace the evidence, not fix it.
They still verify, because the verifier recomputes the canonical form over
whatever metadata keys an event carries, in sorted order. Receipts written
after the change carry `correlation_hash` and no `correlation_id`.

## Conditions common to all three

- State machine: `arn:aws:states:us-west-2:335019467476:stateMachine:reachee-escalation`
  (Step Functions Standard). The execution name is the escalation id.
- Recipient: Heart-Map contact `justin-kaplan`.
- Ladder: tier 1 WhatsApp, tier 2 email (Amazon SES v2), tier 3 SMS.
- Tier 1 has no provider. Its attempt writes `FAILED` and opens the breaker.
  The execution then moves to tier 2, where SES accepts the message.
  Every run below took this path.
- The tier 2 answer window is the Heart-Map's `timeout_seconds` of 900.
  `AwaitAnswer` read it from the attempt's output through `TimeoutSecondsPath`,
  and each execution history shows `timeoutInSeconds: 900`. The reply link
  expires at the same moment.
- Each answer was given through the emailed reply link, on the recipient's phone.
- Priority `P1` throughout. The agent id is `rpm-vitals-monitor-07` on the
  direction run and `scout-meds` on the other two.

Times are UTC. The deployment's Heart-Map time zone is America/Chicago (CDT, UTC−5).

## 1. Direction releases

- Escalation id: `01a16b5f-f8a7-747a-a48b-48300767bc42`
- Execution ARN: `arn:aws:states:us-west-2:335019467476:execution:reachee-escalation:01a16b5f-f8a7-747a-a48b-48300767bc42`
- Status: `SUCCEEDED`
- Started: 2026-10-24T00:46:54.274Z
- Stopped: 2026-10-24T00:52:55.532Z
- Path: `Attempt → RouteAttempt → NextTier → Attempt → RouteAttempt → AwaitAnswer → DecideAnswer → Release`
- Reply slot: armed 00:46:55.697Z, link expiry 01:01:55.025Z, nonce spent
  00:52:55.222Z, decision `direction`.
- The clinician pressed Give direction and wrote a note. The classifier found
  that the note agreed with the button. The chain holds only the note's hash.

| # | Type           | Timestamp                    | Event id                               | Metadata |
|---|----------------|------------------------------|----------------------------------------|----------|
| 1 | `PROMISED`     | 2026-10-24T00:46:54.174608Z  | `01a16b5f-f8de-7a82-a507-ce3faea4c4d5` | promise "attempt delivery on the declared ladder in order and report each outcome"; agent `rpm-vitals-monitor-07`; correlation `rpm-bp-1792802813` |
| 2 | `FAILED`       | 2026-10-24T00:46:54.901190Z  | `01a16b5f-fbb5-760d-a5e5-614ad8ee3f7a` | tier 1, whatsapp, breaker `OPEN`, "promise broken: no provider registered for channel whatsapp" |
| 3 | `SENT`         | 2026-10-24T00:46:55.191180Z  | `01a16b5f-fcd7-7ea8-93e4-1751b234aa32` | tier 2, email, provider `ses-v2`, provider_msg_id `010101a16b5ffc72-1dff85a8-4a58-4827-a5d3-ee3067e175ca-000000` |
| 4 | `RECEIVED`     | 2026-10-24T00:52:55.246028Z  | `01a16b65-7b4e-7d65-b6b6-84b3aa03c820` | via reply link |
| 5 | `ACKNOWLEDGED` | 2026-10-24T00:52:55.262143Z  | `01a16b65-7b5e-7352-8931-08a64ec160b4` | intent `direction`, confidence "button and note agree", classifier "button and text, rules v1" |
| 6 | `RESOLVED`     | 2026-10-24T00:52:55.479177Z  | `01a16b65-7c37-7837-a971-ef6dbe581b42` | **released `true`**, intent `direction` |

```
verifying key: 85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e
key source:    https://d1bskent938hm6.cloudfront.net/keys/reachee-ed25519.pub
events:        6

  01a16b5f-f8d PROMISED       signature MATCH  chain LINKED
  01a16b5f-fbb FAILED         signature MATCH  chain LINKED
  01a16b5f-fcd SENT           signature MATCH  chain LINKED
  01a16b65-7b4 RECEIVED       signature MATCH  chain LINKED
  01a16b65-7b5 ACKNOWLEDGED   signature MATCH  chain LINKED
  01a16b65-7c3 RESOLVED       signature MATCH  chain LINKED

no field in this bundle is long enough to hold a message body

CHAIN VERIFIED: 6 events, every signature and every link
```

## 2. Denial does not release

- Escalation id: `01a16b4b-259b-770b-b6ad-252d80b2b577`
- Execution ARN: `arn:aws:states:us-west-2:335019467476:execution:reachee-escalation:01a16b4b-259b-770b-b6ad-252d80b2b577`
- Status: `SUCCEEDED`. The execution completes, but the held action is not
  released.
- Started: 2026-10-24T00:24:09.417Z
- Stopped: 2026-10-24T00:25:43.757Z
- Path: `Attempt → RouteAttempt → NextTier → Attempt → RouteAttempt → AwaitAnswer → DecideAnswer → Denied`
- Reply slot: armed 00:24:10.717Z, link expiry 00:39:10.121Z, nonce spent
  00:25:43.535Z, decision `denial`.

| # | Type           | Timestamp                    | Event id                               | Metadata |
|---|----------------|------------------------------|----------------------------------------|----------|
| 1 | `PROMISED`     | 2026-10-24T00:24:09.382292Z  | `01a16b4b-25a6-7ef6-a827-5ab5c66c434b` | promise "attempt delivery on the declared ladder in order and report each outcome"; correlation `test-D-1792801448` |
| 2 | `FAILED`       | 2026-10-24T00:24:09.999079Z  | `01a16b4b-280f-7faf-b270-553a5d63e653` | tier 1, whatsapp, breaker `OPEN`, "promise broken: no provider registered for channel whatsapp" |
| 3 | `SENT`         | 2026-10-24T00:24:10.284822Z  | `01a16b4b-292c-7887-9be7-aeee3784ccfe` | tier 2, email, provider `ses-v2`, provider_msg_id `010101a16b4b28c6-beabafa1-ffcd-444e-a29e-0b74ff903954-000000` |
| 4 | `RECEIVED`     | 2026-10-24T00:25:43.543860Z  | `01a16b4c-9577-7d9b-b16b-dd4903ef07b4` | via reply link |
| 5 | `ACKNOWLEDGED` | 2026-10-24T00:25:43.559473Z  | `01a16b4c-9587-798d-aba1-36153c070bd1` | intent `denial`, confidence "button and note agree", classifier "button and text, rules v1" |
| 6 | `DENIED`       | 2026-10-24T00:25:43.696681Z  | `01a16b4c-9610-788a-afae-2cc31d2b876b` | **released `false`**, intent `denial`, "the person denied the held action" |

```
verifying key: 85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e
key source:    https://d1bskent938hm6.cloudfront.net/keys/reachee-ed25519.pub
events:        6

  01a16b4b-25a PROMISED       signature MATCH  chain LINKED
  01a16b4b-280 FAILED         signature MATCH  chain LINKED
  01a16b4b-292 SENT           signature MATCH  chain LINKED
  01a16b4c-957 RECEIVED       signature MATCH  chain LINKED
  01a16b4c-958 ACKNOWLEDGED   signature MATCH  chain LINKED
  01a16b4c-961 DENIED         signature MATCH  chain LINKED

no field in this bundle is long enough to hold a message body

CHAIN VERIFIED: 6 events, every signature and every link
```

## 3. Timeout does not release

- Escalation id: `01a16a5a-10cc-73d2-8241-a34d3bbfc712`
- Execution ARN: `arn:aws:states:us-west-2:335019467476:execution:reachee-escalation:01a16a5a-10cc-73d2-8241-a34d3bbfc712`
- Status: `SUCCEEDED`. The execution completes, but the held action is not
  released.
- Started: 2026-10-23T20:00:50.024Z
- Stopped: 2026-10-23T20:15:51.654Z
- Path: `Attempt → RouteAttempt → NextTier → Attempt → RouteAttempt → AwaitAnswer → TimedOut`
- `AwaitAnswer` ended with `TaskTimedOut`, error `States.Timeout`, at
  20:15:50.977Z. The timeout was caught and routed to `TimedOut`.
- Reply slot:
  - Armed 20:00:51.450Z, with a task token present.
  - The link expired at 20:15:50.667Z, about 0.3 s before the state machine
    stopped waiting.
  - Closed 20:15:51.517Z with reason `timeout`.
  - The nonce was never spent: no `spent_at`, no decision, and no answer stored.

| # | Type        | Timestamp                    | Event id                               | Metadata |
|---|-------------|------------------------------|----------------------------------------|----------|
| 1 | `PROMISED`  | 2026-10-23T20:00:49.924590Z  | `01a16a5a-1104-7831-8c0d-13b0442721b3` | promise "attempt delivery on the declared ladder in order and report each outcome"; correlation `bed-4-k-1792785649` |
| 2 | `FAILED`    | 2026-10-23T20:00:50.551114Z  | `01a16a5a-1377-73d0-93c1-cc0a416e342b` | tier 1, whatsapp, breaker `OPEN`, "promise broken: no provider registered for channel whatsapp" |
| 3 | `SENT`      | 2026-10-23T20:00:50.853442Z  | `01a16a5a-14a5-7361-aac1-c375d618d5d4` | tier 2, email, provider `ses-v2`, provider_msg_id `010101a16a5a1444-826cf88a-0c54-4418-a6bb-756f59e42d04-000000` |
| 4 | `TIMED_OUT` | 2026-10-23T20:15:51.599063Z  | `01a16a67-d32f-79a0-96a5-e85a36fc3f03` | **released `false`**, "no answer within the declared window; a timeout is not consent" |

There is no `RECEIVED`, `ACKNOWLEDGED` or `RESOLVED` in this chain.

```
verifying key: 85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e
key source:    https://d1bskent938hm6.cloudfront.net/keys/reachee-ed25519.pub
events:        4

  01a16a5a-110 PROMISED       signature MATCH  chain LINKED
  01a16a5a-137 FAILED         signature MATCH  chain LINKED
  01a16a5a-14a SENT           signature MATCH  chain LINKED
  01a16a67-d32 TIMED_OUT      signature MATCH  chain LINKED

no field in this bundle is long enough to hold a message body

CHAIN VERIFIED: 4 events, every signature and every link
```

**Demonstrated, not proven:** the recipient opened the link after it expired, and
the page refused. By design, a refused request writes nothing, and the API keeps
no access logs. So the stored state shows the result of that refusal (an unspent
nonce and no answer receipts after expiry) but not the request itself.

## Reading the evidence

- `SENT` records only that SES accepted the message. The `provider_msg_id` is
  the id SES returned, not one we generated.
- `RECEIVED` records that a person answered through the link. `ACKNOWLEDGED`
  records how that answer was classified. Delivery receipts stand for neither.
- `released` appears only on terminal receipts, and only the direction run has
  `true`.
- Metadata values are strings in the signed canonical form, so `released` is
  the string `"true"` or `"false"`.
- No receipt carries the message body or the answer text. `message_hash` is a
  SHA-256 content hash, and the verifier checks that no field is long enough to
  hold a body.
