# Reachee: MVP acceptance criteria

Status: criteria of record. Carried on the demo page, `demo/index.html`, deployed at
https://d1bskent938hm6.cloudfront.net/. The page lights a criterion only from receipts
in the chain on screen, so it does not show all ten; the note at the end of this file
says which, and why.

Scope: one escalation, one recipient, one channel provisioned. Everything below is
pass or fail. Nothing below is a roadmap item.

---

## 01. Handoff returns a receipt, not a wait

A calling agent raises an escalation in one call and gets a correlation receipt back
in milliseconds, rather than holding the connection open while a person decides. The
decision is not pushed back: there is no callback, so the caller reads it later with
`GET /escalation/{id}`, polling until the escalation closes.

## 02. Contact policy is resolved before any provider is touched

Who is on call, on which channels, in what order, inside which quiet hours, and against
what attention budget: all resolved from the declared Heart-Map before any provider is
touched. A value the Heart-Map declares is the value used. Where it omits one, the loader
supplies a stated default: a tier waits 300 seconds, and the attention budget is 6 reaches
an hour, a burst of 3, and a 90-second coalescing window. One fallback is a defect, not a
default: a declared time zone that fails to load is read as UTC rather than refused. The
README lists it under Known gaps.

## 03. Attention guardrails run first, and each decision writes a receipt

Coalescing, rate limiting, and quiet hours evaluate before a provider is called. A
suppressed or deferred reach is recorded as a decision, not as silence.

## 04. The ladder is walked in order, never in parallel

One tier at a time. The next tier is tried only when the current one fails or is
skipped. A tier that delivers waits for an answer for its own declared timeout, and when
that wait runs out the escalation ends as `TimedOut`; no further tier is tried. Under the
deployed Heart-Map, tier 2 (email) delivers and waits 900 seconds, so tier 3's 60 seconds
never runs. No broadcast, no simultaneous pages, no second message for one event.

## 05. A broken promise is named as broken, and a skipped tier is signal

Delivery failure is reported rather than retried quietly. When a provider's breaker is
open, the skip is written into the record with its reason.

## 06. A message is delivered on a live AWS Connected Devices and Services channel

Amazon SES v2 carries the reach. The WhatsApp and SMS/RCS tiers are configured and
unprovisioned by choice, not by limitation.

## 07. The message is enough to decide on, by itself

The recipient answers without opening a console, a dashboard, or a ticket. Nothing
needed for the decision sits behind a login.

## 08. The answer returns to the caller, classified by intent

The reply is ingested, classified, and bound to the originating request. The decision
comes back, not the fact that a button was pressed.

## 09. Only an answer releases the held action

No timeout, absence, or system default lets the held action proceed. A denial is a
decision, and it is not a release.

## 10. The chain verifies cold, with no message body stored

Every step is signed and hash-chained. A third party recomputes the chain against the
published key. The log holds no patient identifier, no reading, and no message body:
content enters only as a hash, and so does the caller's correlation id. The recipient is
named, deliberately, by the contact id declared in the Heart-Map (`recipient_id`), so a
reader of the chain can see who was reached.

---

## Where each one is proven

| # | Proven by |
| --- | --- |
| 01 | `PROMISED` receipt, emitted before any provider call |
| 02 | Heart-Map resolution, rendered before the walk begins |
| 03 | Coalescing, attention budget, and quiet-hours receipts |
| 04 | Tier 1 attempted alone, tier 2 only after tier 1 resolves |
| 05 | `FAILED` and `ESCALATED` receipts, breaker state recorded |
| 06 | Delivery on SES v2 in the deployed prototype |
| 07 | The delivered body, read in full during the demo |
| 08 | `RECEIVED` and `ACKNOWLEDGED` receipts, intent and confidence |
| 09 | `RESOLVED` receipt, released by the answer and nothing else |
| 10 | Chain recomputed in the verifier's own browser |

The demo page reads real chains: a live escalation raised against the deployed stack, or
one of the committed evidence runs. It lights a criterion only from receipts in the chain
it is showing. 06 is shown by a real `SENT` receipt from SES v2. 10 is shown when the
browser recomputes every hash link and, where WebCrypto supports Ed25519, checks every
signature against the published key; where it does not, the page checks the links and
prints the `verify_chain.py` command for the signatures. 02, 03, and 07 are not shown on
the page: resolving the policy writes no receipt, a gate that lets a reach through writes
none, and the delivered body is never served.

## Not claimed

- A generally available product
- A service level or delivery commitment
- Multi-tenant operation
- More than one provisioned channel
