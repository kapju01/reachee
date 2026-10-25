# Reachee

Human-in-the-loop escalation for agentic AI. An agent that reaches the edge of
its own authority stops, Reachee reaches a person through a declared contact
policy, and the person's decision is what releases the held action.

Submission to the AWS CDS Agentic AI Partner Hackathon. Deadline 28 October
2026, 1:00pm PDT. Judged 40% technical execution, 20% demo presentation, 20%
potential value, 10% creativity, 10% functionality.

## Scope of this build

This is an MVP for judging, not a production system. The boundary is stated
once here and the principles below are read inside it.

In scope: one escalation type, one verified recipient, one provisioned channel
(Amazon SES v2), a three-tier policy where tier 1 fails because it is not
provisioned, one reply path, two outcomes, and ten acceptance criteria that
pass or fail.

Not in scope, and not to be quietly added: multi-tenancy, a service level,
high availability, key rotation, SSO, VPC isolation, WAF, observability
beyond CloudWatch defaults, or a second provisioned channel.

The principles below are not aspirations for a later version. They are
constraints on this one. Where a principle cannot be fully honored inside the
MVP boundary, the gap is named in the README rather than papered over.

## Principles

These six shape what gets built and what gets refused. A change that violates
one is wrong even if it passes its tests.

### Promise theory

Mark Burgess's framing, applied literally. An autonomous agent can promise
only its own behavior, never another's, and a promise is assessed by the
observer rather than asserted by the promiser.

- Name every promise from the promiser's own capability. Reachee promises to
  attempt delivery in declared order and to report each outcome. It cannot
  promise that a human will answer, because that promise is the human's to
  make.
- A provider's acceptance is not a person's attention. `SENT` means a provider
  took the message. `RECEIVED` means a human replied. `ACKNOWLEDGED` means the
  reply was classified. Never collapse those, and never write a receipt
  asserting something the component could not know.
- A broken promise is reported out loud, never retried into silence and never
  upgraded into a better-sounding one. An open breaker produces a receipt
  saying the tier was skipped and why.
- Impositions are not promises. A timeout is not consent, absence is not
  agreement, and a default is not a decision.

### Zero trust

No implicit trust from position, possession, or prior step. Verify every
transaction on its own merits.

- The reply endpoint trusts nothing about its caller except a signed,
  single-use, expiring token, which it validates on every request. Possession
  of that link authorizes exactly one decision on one escalation. It is not
  proof of identity, and the README says so rather than implying more.
- Evidence is trusted because it verifies against a published key, not because
  it came from our own database. An auditor runs `verify/` without us.
- Least privilege where it is cheap: a separate execution role per Lambda,
  scoped to the table, secret, and action it actually needs. The build user
  holding `AdministratorAccess` is a deliberate MVP exception, stated in the
  README, not hidden.
- Assume breach. No secrets in the repo, no message bodies in the log. A
  leaked chain should disclose nothing about a patient, a clinician, or what
  was said.
- Deferred and disclosed: mTLS, device posture, SSO on the reply page, network
  isolation.

### Effectiveness

Does it achieve the intended outcome, which is that a person is genuinely
reached, genuinely decides, and the action genuinely waits.

- `MVP_CRITERIA.md` is the definition. A change either moves a criterion from
  fail to pass or protects one already passing. If it does neither, it is out
  of scope, and say so before building it.
- Reaching nobody is a failure even when every component reports success.
  Measure the outcome, never the activity.

### Ease of implementation and use

Two audiences, both of whom give up quickly.

- The clinician decides from the message alone. No login, no account, no
  console, no ticket, and nothing needed for the decision sitting behind a
  click they must think about. Under thirty seconds to read and answer.
- The judge clones, reads the README, and runs one command. Every capability
  ships with the single command that exercises it. If reproducing a claim
  takes more than ten minutes from a cold clone, it scores as if it does not
  work.
- Prefer one obvious way over two configurable ones. Configuration surface is
  a cost paid by the reader.

### Efficiency

Human attention is the scarce resource here, not compute.

- The dignity pipeline exists to spend attention sparingly: coalescing, the
  attention budget, and quiet hours all run before any provider is touched. A
  second page for the same event is a defect, not a retry.
- Compute follows: serverless, arm64, on-demand billing, no idle cost. The
  whole build should stay under a couple of dollars.
- No polling where an event will do. No provider call before policy has
  decided. No work in a Lambda that the state machine should be holding.

### Efficacy

Efficacy is whether the thing demonstrably works under the conditions
claimed. It is shown, never asserted.

- Every claim in the README, the demo page, the video, or a receipt has an
  assertion behind it in `make criteria`. If it cannot be asserted, it is not
  claimed.
- Criterion 09 needs three runs, not one: a direction releases, a denial does
  not, and a timeout does not. Two of those three are the ones a judge probes.
- A verifier that accepts everything proves nothing. The tamper test is part
  of the evidence, not a nicety.
- When something is demonstrated rather than proven, label it that way on the
  spot and remove the label when it becomes true.

## Ground rules

These follow from the principles above and are not negotiable in a hurry.

- Region is `us-west-2`. Never change it, never parameterize it away.
- `./scripts/preflight.sh` must stay green. Run it before and after any change
  under `internal/audit`.
- `testdata/canonical_golden.txt` is the contract between the Go encoder and
  the independent Python verifier. Do not regenerate it to make a test pass.
  If the canonical form genuinely must change, change both implementations and
  say so out loud.
- `verify/` shares no code with Go and uses only the Python standard library.
  That independence is the product claim, not a style preference. Do not add a
  dependency there, and do not import anything from the Go side's logic.
- No message bodies in the audit log, ever. Content hashes only. A field long
  enough to hold a message body fails the verifier's own check.
- Retry, backoff, and failover belong to the dispatcher and the circuit
  breaker, never inside a provider. A provider reports its failure and stops.
- `env.sh`, `terraform.tfvars`, and `*.tfstate` are gitignored and carry real
  addresses and credentials. Never commit them, and never echo their contents
  into a file that is committed.
- `DISCLOSURE.md` is a compliance document for the submission. Do not edit it
  without being asked.

## Acceptance criteria

`MVP_CRITERIA.md` holds ten criteria. Each is pass or fail.

## Known defects and gaps, in priority order

These are the same four, in the same order, as the README's "Known gaps".

1. A deferred escalation is dropped and nothing re-queues it. The `DEFERRED`
   receipt is written, but the reach never happens, which contradicts the
   intent of criterion 03.
2. The dignity pipeline (coalescing, the attention budget, quiet hours) keeps
   its state in memory per execution environment, so its decisions do not
   hold across cold starts and nothing caps the total number of raises.
3. Coalesced joiners never receive the resolution. Out of scope for the
   submission, documented as a known gap.
4. No empathy model is configured. Every `SENT` receipt says so with
   `empathy_source: fallback`.

## Handler requirements

Binding on `cmd/raise`, `cmd/attempt`, and `cmd/reply`. All three meet them
today; a change that breaks either is wrong.

- The signing key is fetched from Secrets Manager once, at init, into a
  package-level variable, and never per invocation. Criterion 01 allows under
  a second, and a Secrets Manager call on a cold start will not make that.
- `reply` refuses out loud when it finds no armed task token for the
  escalation. It returns an explicit error to the caller and never silently
  waits, retries, or records the decision for later.

## Layout

```
cmd/reachee      local CLI and the chain exporter
cmd/raise        Lambda: returns a receipt, starts the execution
cmd/attempt      Lambda: walks exactly one tier
cmd/reply        Lambda: validates the token, classifies, releases
cmd/status       Lambda: returns one escalation's chain, or its bundle
internal/        the escalation engine
infra/           Terraform root module
verify/          independent chain verifier, no shared code
scripts/         preflight and the criteria harness
demo/            single-file demo page, becomes the live client
testdata/        the canonical golden file
```

## Environment

`source ./env.sh` before any AWS work. It sets `AWS_PROFILE`, both region
variables, `TF_VAR_region`, `REACHEE_SENDER`, and `REACHEE_RECIPIENT`.

Lambda builds cross-compile from this Mac: `CGO_ENABLED=0 GOOS=linux
GOARCH=arm64`, runtime `provided.al2023`, handler `bootstrap`.

## Code voice

Comments explain why, not what. Godoc on every exported identifier. No filler
comments restating the line below them. Match the existing packages, which
document the reasoning behind a decision rather than narrating the syntax.

## Working style

Verdict first. Push back directly when something is wrong rather than finding
a way to agree. Do not add scope that was not asked for. There are five days
and ten criteria, and everything else is a cut.
