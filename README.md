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
| `internal/empathy` | Humanizes the message, and discloses in the payload and the audit log when it could not. |
| `internal/audit` | Hash-chained, Ed25519-signed events. Content hashes only, never content. |
| `internal/dispatcher` | Walks the ladder in order and emits an event for every promise kept, broken, or skipped. |

## Design commitments

1. Message content never enters the audit log. Only its SHA-256 hash does, so the chain is safe to hand to a party who should not read the messages.
2. The chain verifies offline against a published public key, with no operator cooperation required.
3. A skipped tier, a suppressed escalation, and a deferred one each produce an audit event. Silence is never an outcome.
4. Failover is tiered, not active-active. Reaching a human twice for one event costs their trust.
5. The empathy layer marks its own degradation. Nothing claims a humanized message it did not produce.

## Not yet built

- AWS CDS providers: `socialmessaging`, `sesv2`, `pinpoint-sms-voice-v2` (week 3)
- Bedrock AgentCore orchestrator and the Bedrock-backed empathy model (week 5)
- DynamoDB-backed stores and the S3 Object Lock archive (week 4)
- Inbound response ingestion and classification (week 5)
- Terraform under `deploy/` (weeks 3 to 5)
