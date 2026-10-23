# Pre-existing work disclosure

The hackathon rules require that projects be newly created during the
submission period, and that any pre-existing code incorporated into a project
be disclosed. This file is that disclosure.

## Written before the AWS build

The escalation engine and the local CLI were written in September 2026:

- `internal/audit` — hash-chained, Ed25519-signed event log
- `internal/circuit` — three-state circuit breaker
- `internal/dignity` — coalescing, rate limiting, quiet hours
- `internal/dispatcher` — tiered failover walk
- `internal/empathy` — message translation
- `internal/heartmap` — contact policy parsing and resolution
- `internal/providers` — the provider contract and an in-memory mock
- `internal/config`, `cmd/reachee` — configuration and the local CLI
- `demo/index.html` — the single-file demo page, originated September 2026

## Written for this submission

Everything that makes it run on AWS: the SES v2 provider, the Lambda handlers,
the Terraform root module under `infra/`, the independent chain verifier under
`verify/`, the preflight and criteria harnesses under `scripts/`, and the
rewiring of the demo page into a live client against the deployed API.

## Tools

Standard development tools were used throughout, including AI coding
assistants, as the rules permit.
