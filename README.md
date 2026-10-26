# Reachee

Human-in-the-loop escalation for agentic AI. An agent that reaches the edge of
its own authority stops. Reachee reaches a person through a declared contact
policy, and only that person's decision releases the held action.

*Every Reachee is respected. Every promise is kept.*

## Status

The stack is deployed and live in `us-west-2`. The demo page is
**https://d1bskent938hm6.cloudfront.net/**.

The central claim is that only a person's answer releases the held action, and
that the record of it verifies without us. A judge can confirm it from a cold
clone in under ten minutes: the page, then one stdlib Python command against a
committed bundle and the published key.

`MVP_CRITERIA.md` holds the ten pass-or-fail criteria. Criterion 09 (a
direction releases, a denial and a timeout do not) is evidenced in
`docs/criterion-09-evidence.md`, and criterion 05 (an open breaker skips a
tier and says so) in `docs/criterion-05-evidence.md`. `make criteria` asserts
all ten.

There are two tiers of reproducibility, and they need different things:

- **The central claim needs stock Python and nothing else.** One
  `python3 -I verify/verify_chain.py` command verifies a committed bundle
  against the published key (step 2 below).
- **The full criteria harness needs a Go toolchain and its modules.**
  `make criteria` runs offline, but the Go tests it cites import the AWS SDK.
  So it needs Go 1.24 or later and one `go mod download` beforehand (step 3).
  Without the modules, those checks print NOT ASSERTED naming that command,
  and their criteria are counted as not asserted, never as met.

## Quick start

### 1. Open the live page

https://d1bskent938hm6.cloudfront.net/

- **Inspect a past run** loads a committed evidence bundle, checks every hash
  link in the browser, checks every Ed25519 signature against the published
  key where the browser's WebCrypto supports Ed25519, and lights each
  criterion only from receipts in that chain. Each run downloads as the
  bundle the verifier reads.
- **Raise a live escalation** calls `POST /raise` on the deployed API. It
  reaches the project's on-call clinician by real email. Unless that person
  happens to answer, the honest outcome is a timeout after fifteen minutes,
  with nothing released. Between 22:00 and 07:00 America/Chicago, quiet hours
  defer a P1 raise, and no email is sent.

### 2. Verify a bundle cold

This needs only Python 3 and its standard library. `-I` keeps the
interpreter from importing anything from the working directory.

    PUB=https://d1bskent938hm6.cloudfront.net/keys/reachee-ed25519.pub
    python3 -I verify/verify_chain.py docs/evidence/c09-denial-01a16b4b-259b.json --pubkey $PUB

Expected output ends:

    no field in this bundle is long enough to hold a message body

    CHAIN VERIFIED: 6 events, every signature and every link

With `--pubkey`, the key embedded in the bundle is ignored. A bundle checked
against its own key proves only that it is internally consistent. The
verifier shares no code with the Go side. `--selftest
testdata/canonical_golden.txt` checks its serializer against the golden file
that the Go encoder is also tested against.

A live escalation verifies the same way, straight from the API:

    curl -s "https://un5ibyx5a1.execute-api.us-west-2.amazonaws.com/escalation/<id>?format=bundle" \
      | python3 -I verify/verify_chain.py /dev/stdin --pubkey $PUB

### 3. Check the criteria

Prerequisites: Python 3, Go 1.24 or later, and the Go modules, fetched once:

    go mod download
    make criteria

`scripts/criteria.sh` has one block per criterion, named by its number and
its own wording in `MVP_CRITERIA.md`. It checks the committed bundles, the Go
tests, the Terraform source, and the page source. It verifies signatures
against `demo/keys/reachee-ed25519.pub` and never reaches the network. A
criterion is met only if every check in its block passes. A criterion with no
assertion behind it prints NOT ASSERTED and the reason, and is counted in the
summary line. Any failed check makes the run exit nonzero.

### 4. Run the tests

    make test

Go 1.24. Run `make run` to walk one escalation locally against mock
providers, with tier 1 forced down. It signs the chain with a key generated
for that run and verifies against it. That proves the pipeline holds
together; it is not evidence about the deployed stack.

`./scripts/preflight.sh` additionally checks the AWS identity and the SES
identities, so it needs AWS credentials. It then runs four more checks:

- canonicalization parity across Go, Python, and the demo page;
- a round trip signed in Go and verified in Python;
- the tamper test;
- the body-length check.

Under Node, it also runs the demo page's own verifier over every committed
bundle. Without Node, that check prints `SKIP`, and the summary line counts
it.

### 5. Deploy your own

Terraform 1.6 or later, the AWS CLI, and an account you can create IAM roles
in. The region is fixed at `us-west-2`.

1. Verify a sender and a recipient address in SES v2 in `us-west-2`. A
   sandbox account delivers only to verified addresses.
2. Run `go run ./cmd/reachee keygen`. It prints an Ed25519 seed and public key
   as hex and writes nothing to disk. Put the public half in
   `demo/keys/reachee-ed25519.pub`.
3. Run `cp infra/terraform.tfvars.example infra/terraform.tfvars`, fill in the
   sender, recipient, and a bucket name, then `terraform -chdir=infra init`
   and `make apply`.
4. Put the seed into the secret Terraform created:
   `aws secretsmanager put-secret-value --secret-id reachee/signing-key
   --secret-string <seed>`. Terraform creates the container only, so the key
   never passes through Terraform state.

`terraform -chdir=infra output` gives the API endpoint, the demo URL, and the
public key URL. Your page reads `config.json`, which Terraform writes beside
it from your own API URL and Heart-Map, with no addresses.

The committed evidence bundles were signed with this project's key. On your
own deploy, the past runs therefore fail signature checks against your key.
Verify them against the project's published key, as in step 2.

## Evidence bundles

Five bundles are committed in `docs/evidence/`. All five were raised against
the deployed stack in `us-west-2` and delivered by Amazon SES v2: each carries
a `SENT` receipt from `ses-v2` with the message id SES returned. All five
verify against the published key with the command above.

| Bundle | Raised (UTC) | Receipts | Shows |
| --- | --- | --- | --- |
| `c09-direction-01a16b5f-f8a7.json` | 2026-10-24 00:46 | `PROMISED` `FAILED` `SENT` `RECEIVED` `ACKNOWLEDGED` `RESOLVED` | A direction releases (`released: true`) |
| `c09-denial-01a16b4b-259b.json` | 2026-10-24 00:24 | `PROMISED` `FAILED` `SENT` `RECEIVED` `ACKNOWLEDGED` `DENIED` | A denial releases nothing |
| `c09-timeout-01a16a5a-10cc.json` | 2026-10-23 20:00 | `PROMISED` `FAILED` `SENT` `TIMED_OUT` | A timeout releases nothing |
| `c05-breaker-01a16e4e-76fb.json` | 2026-10-24 14:26 | `PROMISED` `ESCALATED` `SENT` `TIMED_OUT` | An open breaker skips tier 1 and says why |
| `c09-live-ses-direction-01a172f0-9204.json` | 2026-10-25 12:02 | `PROMISED` `FAILED` `SENT` `RECEIVED` `ACKNOWLEDGED` `RESOLVED` | A second direction, fetched from `?format=bundle` |

The three `c09-` runs from 23 and 24 October predate the change that put
`correlation_hash` in receipts in place of `correlation_id`. Their receipts
carry the caller's correlation id in plaintext, signed as recorded. They are
left exactly as exported, because regenerating them would replace the
evidence, not fix it. The 25 October run and the breaker run carry only the
hash.

## What is here

| Path | Responsibility |
| --- | --- |
| `cmd/raise` | Lambda behind `POST /raise`. Writes the `PROMISED` receipt, starts the execution, returns. Never touches a provider. |
| `cmd/attempt` | Lambda the state machine invokes to walk exactly one tier: attention gates, breaker, provider, receipt. |
| `cmd/reply` | Lambda for the person's answer. `GET /reply` renders the choice and changes nothing; `POST /reply` records and classifies the answer and hands it to the waiting execution. It also writes the terminal receipts for release, denial, timeout, and a failed stage. |
| `cmd/status` | Lambda behind `GET /escalation/{id}`: one escalation's receipts, or with `?format=bundle` the bundle the verifier reads. |
| `cmd/reachee` | Local CLI: a mock-provider run, the chain exporter, and `keygen`. |
| `internal/audit` | Hash-chained, Ed25519-signed events, and the bundle format. Content hashes only, never content. |
| `internal/signing` | Loads the signing key from Secrets Manager once, at init. |
| `internal/replytoken` | Mints and checks the reply link: escalation, recipient, expiry, and nonce under an HMAC. |
| `internal/ddb` | The escalations table: request record, reply slot, chain, and the persisted circuit breakers. `ddbtest` is an in-memory stand-in for tests. |
| `internal/config` | Runtime configuration from the environment. |
| `internal/heartmap` | Per-contact policy: ladder, channels, quiet hours, rate limits. Loads YAML with no third-party dependency. |
| `internal/dignity` | Coalescing, the attention budget, and quiet hours. Runs before any provider is touched. |
| `internal/circuit` | Three-state breaker per provider. Skips a broken provider instead of retrying it into the ground. |
| `internal/dispatcher` | Walks the ladder in order and emits a receipt for every promise kept, broken, or skipped. |
| `internal/empathy` | Wraps the agent's words for the recipient. When no model ran, the `SENT` receipt says so (`empathy_source`, `empathy_reason`). |
| `internal/providers` | The `Messenger` contract. `ses` is the one provisioned channel; `mock` is for local runs and tests. |
| `verify/` | The independent verifier. Python standard library only, no shared code. |
| `infra/` | Terraform: one table, one secret container, four Lambdas, one Standard state machine, one HTTP API, and the demo bucket behind CloudFront. |
| `demo/` | The single-file demo page and the published public key. |
| `testdata/` | `canonical_golden.txt`, the contract between the Go encoder and the Python verifier. |
| `scripts/` | `preflight.sh`. |

## How a hold ends

The escalation is a Step Functions Standard state machine. `Release` has
exactly one way in, from `DecideAnswer`. `DecideAnswer` runs only after
`AwaitAnswer` has been completed by a person's answer, and it sends only an
explicit `direction` to `Release`. Everything else goes to `Denied`: a
denial, or an answer that is malformed or unclassified. A timeout routes to
`TimedOut`. An attention-gate verdict and an exhausted ladder end in their own
states. None of these release anything, and each writes a receipt. `reply`
also re-checks the stored decision before it writes `RESOLVED`, so a
definition edited to route wrongly fails rather than releasing.

`AwaitAnswer` waits on a task token. The reply link cannot carry that token,
because the email goes out before the execution is holding. It carries a
signed claim instead, and `reply` refuses out loud when no token is armed.

## Reading an escalation's chain

Anyone holding an escalation id can read its chain:

    curl "$API/escalation/<id>"                  # receipts, decision, released
    curl "$API/escalation/<id>?format=bundle" \
      | python3 -I verify/verify_chain.py /dev/stdin --pubkey "$PUB"

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
  reveals when an escalation was raised. The random bits put enumeration out
  of reach.
- **The chain carries no message content.** It holds content hashes, never
  the payload, the subject, or the clinician's note. The verifier fails a
  bundle in which any string field runs past 200 characters, metadata values
  included. That detects something large enough to be a message body. It
  cannot detect a short identifier, and nothing in the verifier can: a name or
  a record number is just another short string. That limitation is why the
  content rule is enforced where receipts are written, not where they are
  read. Every value a receipt carries is one of four things: a fixed string,
  a value from the Heart-Map, a hash, or an id or error code an AWS service
  returned. There are two exceptions:
  - `agent_id` is the calling agent's own string, written verbatim.
  - `empathy_reason` carries a model's error text when the model fails. No
    model is configured, so today it always reads "no model configured".
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

## Throttling and failed stages

The account allows ten concurrent Lambda executions, shared by every
function. Two limits keep any one caller from taking them all, and the state
machine is written so that running out of them is never silent.

- **`POST /raise` is limited to about three a minute** (0.05 per second,
  burst 1). Every raise that gets through becomes a real email to a real
  person. This bounds the rate, not the volume: three a minute sustained is
  still a great deal of email. Nothing caps the total today, because the
  attention budget is kept per execution environment (see known gaps below).
  The real cap arrives when that budget is kept in the table.
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

## Deliberate scope

This is an MVP built for judging. Its boundary is a decision, not a backlog:

- one escalation type;
- one verified recipient;
- one provisioned channel, Amazon SES v2. Tier 1 (WhatsApp) and tier 3 (SMS)
  are declared in the Heart-Map with no provider behind them. Tier 1 fails on
  every run because it is not provisioned, which is how the failover and the
  breaker are exercised.

It does not have, and is not meant to have:

- multi-tenancy;
- a service level or delivery commitment;
- high availability;
- key rotation;
- SSO, or any login on the reply page;
- VPC isolation;
- a WAF;
- observability beyond CloudWatch defaults;
- a second provisioned channel.

## MVP exceptions, disclosed

- **The build user holds `AdministratorAccess`.** Terraform runs as an IAM
  user with that managed policy. The deployed functions do not: each Lambda
  has its own execution role, scoped to the table, secret, and actions it
  needs.
- **SES is in the sandbox.** Production access is not enabled, so SES
  delivers only to verified addresses. That is why this build reaches exactly
  one recipient.
- **The reply link proves possession, not identity.** A valid link
  authorizes one decision on one escalation, once, until it expires. Whoever
  holds it can make that decision. Nothing on the reply path checks who they
  are.
- **API access logging is off on purpose.** The reply token rides in the
  query string of `GET /reply`, so an access log would store a live bearer
  credential. The cost is that a refused request leaves no record. The
  criterion 09 evidence calls out the one place this matters.

## Known gaps

- **A deferred escalation is not re-queued.** When quiet hours defer a reach,
  the `DEFERRED` receipt is written and the execution ends. Nothing raises it
  again when the window closes. This contradicts criterion 03's intent and is
  the open defect.
- **Attention-gate state is per execution environment.** Coalescing, the
  attention budget, and quiet hours keep their state in memory in `attempt`.
  Their decisions hold across warm invocations, not across cold starts. This
  is also why nothing caps the total number of raises.
- **Coalesced joiners never receive the resolution.** An escalation coalesced
  into another ends with its `COALESCED` receipt and is not told how the
  first one ended.
- **No empathy model is configured.** The recipient gets the agent's own
  words, and every `SENT` receipt says so with `empathy_source: fallback`.
