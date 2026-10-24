# Reachee

Human-in-the-loop escalation fabric for regulated agentic AI.

When an autonomous agent hits a decision it should not make alone, Reachee reaches the right human, on the right channel, in the right register, at the right time. Every attempt produces cryptographic evidence that survives audit.

*Every Reachee is respected. Every promise is kept.*

## Status

Week 2 skeleton. The pipeline runs end to end against mock providers. The three AWS CDS providers land in week 3.

## Quick start

    make test
    make run

`make run` dispatches one escalation with tier 1 forced down, walks the ladder, prints the audit chain, and verifies that chain against a freshly generated public key.

## What is here

| Package | Responsibility |
| --- | --- |
| `internal/heartmap` | Per-contact policy: ladder, channels, quiet hours, rate limits. Loads YAML with no third-party dependency. |
| `internal/dignity` | Coalescing, rate limiting, and quiet hours. Runs before any provider is touched. |
| `internal/circuit` | Three-state breaker per provider. Skips a broken provider instead of retrying it into the ground. |
| `internal/providers` | The `Messenger` contract plus a recording mock. WhatsApp, SES, and SMS implement this interface. |
| `internal/empathy` | Humanizes the message. When it could not, the SENT receipt says so (`empathy_source`, `empathy_reason`); the recipient's message carries no marker. |
| `internal/audit` | Hash-chained, Ed25519-signed events. Content hashes only, never content. |
| `internal/dispatcher` | Walks the ladder in order and emits an event for every promise kept, broken, or skipped. |

## Design commitments

1. Message content never enters the audit log. Only its SHA-256 hash does, so the chain is safe to hand to a party who should not read the messages.
2. The chain verifies offline against a published public key, with no operator cooperation required.
3. A skipped tier, a suppressed escalation, and a deferred one each produce an audit event. Silence is never an outcome.
4. Failover is tiered, not active-active. Reaching a human twice for one event costs their trust.
5. The empathy layer marks its own degradation. Nothing claims a humanized message it did not produce.

## Reading an escalation's chain

Anyone holding an escalation id can read its chain:

    curl "$API/escalation/<id>"                  # receipts, decision, released
    curl "$API/escalation/<id>?format=bundle" \
      | python3 verify/verify_chain.py /dev/stdin --pubkey "$PUBKEY_URL"

The default response lists each receipt with its event type, hashes,
timestamp, and provider. It also gives the decision, taken from the
`ACKNOWLEDGED` receipt, and whether the action was released, taken from the
terminal receipt. `?format=bundle` returns the public key, the genesis hash,
and every event exactly as it was signed. That is the input
`verify/verify_chain.py` reads, so a live escalation verifies in one pipe.

This exposure is deliberate. The endpoint has no authentication; the id is
the only thing a caller needs.

- **Ids are hard to guess.** Escalation ids are UUIDv7: a millisecond
  timestamp followed by 74 random bits from `crypto/rand`. The timestamp
  reveals when an escalation was raised. The random bits put enumeration out of
  reach.
- **The chain carries no message content.** It holds content hashes, never
  the payload, the subject, or the clinician's note. The verifier fails a
  bundle in which any string field, metadata values included, runs past 200
  characters. That detects something large enough to be a message body. It
  cannot detect a short identifier, and nothing in the verifier can: a name or
  a record number is just another short string. That limitation is why the
  content rule is enforced where receipts are written, not where they are
  read. Every value a receipt carries is a fixed string, a value from the
  Heart-Map, a hash, or an id or error code an AWS service returned, with two
  exceptions:
  - `agent_id` is the calling agent's own string, written verbatim.
  - `empathy_reason` carries a model's error text when the model fails. No
    model is configured yet, so today it always reads "no model configured".
- **Correlation ids enter as hashes.** A receipt carries `correlation_hash`,
  the SHA-256 (lowercase hex) of the caller's `correlation_id` exactly as sent.
  The plaintext stays on the request record, where coalescing reads it. A
  caller confirms its own correlation by hashing its own id. With no
  correlation id there is no hash.
- **The role enforces what is withheld.** The request record and the reply
  slot sit in the same DynamoDB partition as the chain, so the status
  Lambda's role does more than grant `dynamodb:Query`. Its conditions refuse
  any query that asks for more than the `event` attribute. They also refuse
  any query whose partition key is not shaped like a UUIDv7, which keeps the
  reserved `#breaker/` partitions out of reach. The handler cannot return
  what it cannot read.
- **The contact is named.** Each receipt carries `recipient_id`, the
  Heart-Map contact handle, which is already in the committed Heart-Map.
  Knowing that a clinician was reached is part of what the chain proves.

## Circuit breakers

Each provider's breaker is kept in the escalations table, one reserved
partition per breaker (`#breaker/ses-v2`, `#breaker/unprovisioned:whatsapp`).
`attempt` reads a breaker before it touches a provider and records every
outcome after. Every write is conditional on the version it read. When two
invocations race, the loser reapplies its outcome to the winner's position
rather than overwriting it. A breaker opened by one escalation is therefore
open for the next, whether or not the next lands on the same execution
environment.

- **Trip opens the breaker on the spot.** A channel with no provider registered
  trips its breaker on the first attempt, without waiting for a failure
  threshold.
- **Cooldown is fifteen minutes, set by the attention budget.** On a
  provisioned channel, a half-open probe sends a real message to a real
  person. Fifteen minutes allows four probes an hour per breaker, and the
  Heart-Map permits six reaches an hour, so re-probing a broken channel can
  never spend the budget on its own. One probe is admitted per cooldown, and
  the claim is itself a conditional write. A probe whose outcome never arrives
  lapses after one cooldown.
- **Breaker failures are written into the receipt.** If a breaker cannot be
  read, its tier is not attempted, and a `FAILED` receipt says why. If an
  outcome cannot be recorded, the receipt is written anyway with
  `breaker_recorded: false`.

Known gap: the dignity pipeline (coalescing, the attention budget, quiet
hours) still keeps its state in memory per execution environment. Its
decisions hold across warm invocations of `attempt`, not across cold starts.

## Throttling and failed stages

The account allows ten concurrent Lambda executions, shared by every
function. Two limits keep any one caller from taking them all, and the state
machine is written so that running out of them is never silent.

- **`POST /raise` is limited to about three a minute** (0.05 per second,
  burst 1). Every raise that gets through becomes a real email to a real
  person. This bounds the rate, not the volume: three a minute sustained is
  still a great deal of email. Nothing caps the total today, because the
  attention budget is kept per execution environment, as stated above. The
  real cap arrives when that budget is kept in the table.
- **`GET /escalation/{id}` is limited to about one a second** (burst 2), so
  polling and refreshing cannot take the slots a clinician's `POST /reply`
  needs. API Gateway enforces these limits approximately. Under test it let
  through more than the burst, sometimes all of a burst of thirty.
- **A throttled invocation is retried, and nothing else is.** Every Lambda
  task in the state machine retries `Lambda.TooManyRequestsException`, about
  two minutes of backoff in all. That error means Lambda refused the call
  before the function ran, so the retry repeats an invocation, not a
  delivery. `Lambda.ServiceException` is not retried, because it can arrive
  after the function ran, and a re-run attempt would page the person twice.
- **A stage that still fails writes a receipt.** The state machine catches
  the error, and `reply` writes `FAILED` with the stage, the AWS error name,
  and `released: false`. The execution then ends failed. The reply link
  stops working, rather than leaving the person on "not ready yet".
- **One limit the chain cannot cover.** That receipt is written by a Lambda,
  because only a Lambda holds the signing key. If that Lambda cannot be
  invoked either, nothing can be signed. The execution fails on the Lambda
  error itself rather than on `PromiseBroken`, the chain ends with no
  receipt saying why, and the only record is the Step Functions execution
  history.

## Not yet built

- AWS CDS providers: `socialmessaging`, `sesv2`, `pinpoint-sms-voice-v2` (week 3)
- Bedrock AgentCore orchestrator and the Bedrock-backed empathy model (week 5)
- DynamoDB-backed stores and the S3 Object Lock archive (week 4)
- Inbound response ingestion and classification (week 5)
- Terraform under `infra/` (weeks 3 to 5)
