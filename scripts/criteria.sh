#!/usr/bin/env bash
#
# Reachee acceptance criteria. One block per criterion in MVP_CRITERIA.md,
# named by its number and its own wording.
#
# It asserts against what is in the clone and nothing live: the committed
# evidence bundles in docs/evidence/, the Go tests, the Terraform source, and
# the demo page source. Signatures are checked against the repo's own
# demo/keys/reachee-ed25519.pub, so the run needs no network. The README's
# cold-verify command covers the published-key path.
#
# Needs Python 3 (standard library only). The Go-backed checks also need a Go
# toolchain and its modules, fetched once with `go mod download`. The run
# itself never reaches the network: GOPROXY=off below. A check whose Go
# package cannot build prints NOT ASSERTED naming the remedy, and its
# criterion counts as not asserted, never as met.
#
# A criterion is met only if every check in its block passes. A FAIL in one
# block does not stop the others. There are three outcomes, and only the first
# says green:
#
#   exit 0  every criterion asserted and met: "criteria green"
#   exit 1  any check failed: "criteria FAILED: ..."
#   exit 2  nothing failed, but some criterion was not asserted:
#           "criteria incomplete: ..." with the remedy. A judge's first
#           command is often this one, before the prerequisites, and an
#           unasserted criterion must not read as a pass.
#
# make reports any failed recipe as its own exit 2, so `make criteria` cannot
# carry the 1; the last line still tells the outcomes apart.
#
#   ./scripts/criteria.sh

set -uo pipefail

cd "$(dirname "$0")/.."

export GOPROXY=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly

failed=""
not_asserted=""
met=""
crit=""
crit_failed=0
crit_na=0

ok()  { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n' "$1"; crit_failed=1; }
na()  { printf '  NOT ASSERTED  %s\n' "$1"; crit_na=1; }

begin() {
  crit="$1"; crit_failed=0; crit_na=0
  printf '\n%s. %s\n' "$1" "$2"
}
end() {
  if [ "$crit_failed" -eq 1 ]; then failed="$failed $crit"
  elif [ "$crit_na" -eq 1 ]; then not_asserted="$not_asserted $crit"
  else met="$met $crit"
  fi
}

# gotests PKG TEST... passes only if every named test ran and passed. A -run
# pattern that matches nothing exits 0, so each name must report PASS.
gotests() {
  local pkg="$1"; shift
  local pattern out t
  pattern="^($(IFS='|'; echo "$*"))\$"
  out="$(go test -count=1 -v -run "$pattern" "$pkg" 2>&1)" || {
    printf '%s\n' "$out" | grep -E -- '--- FAIL|^FAIL|cannot|missing' | head -5 | sed 's/^/        /'
    return 1
  }
  for t in "$@"; do
    grep -q -- "--- PASS: $t (" <<<"$out" || { printf '        %s did not run\n' "$t"; return 1; }
  done
}

tmpdir="$(mktemp -d -t reachee-criteria)"
trap 'rm -rf "$tmpdir"' EXIT

# -- what this run can use -------------------------------------------------
printf 'Reachee acceptance criteria, asserted against this clone\n'

command -v python3 >/dev/null || { printf '  FAIL  python3 is required\n'; exit 1; }

have_go=0
have_modules=0
if command -v go >/dev/null && go list ./internal/audit >/dev/null 2>&1; then
  have_go=1
  go list -deps -test ./cmd/... ./internal/... >/dev/null 2>&1 && have_modules=1
fi
NO_GO="needs a Go toolchain (go 1.24 or later, see go.mod)"
NO_MODULES="needs the Go modules: run go mod download once, then re-run"

if [ "$have_go" -eq 0 ]; then
  printf '  note  %s; Go-backed checks will not be asserted\n' "$NO_GO"
elif [ "$have_modules" -eq 0 ]; then
  printf '  note  Go modules are not in the cache; checks in packages that import the AWS SDK will not be asserted\n'
fi

# The ten headings below must be MVP_CRITERIA.md's own, so this file cannot
# drift into checking something the criteria no longer say.
python3 -I - <<'PY' || { printf '  FAIL  the headings in scripts/criteria.sh do not match MVP_CRITERIA.md\n'; exit 1; }
import re
script = open("scripts/criteria.sh", encoding="utf-8").read()
ours = re.findall(r'^begin "(\d\d)" "(.+)"$', script, re.M)
theirs = re.findall(r"^## (\d\d)\. (.+)$", open("MVP_CRITERIA.md", encoding="utf-8").read(), re.M)
raise SystemExit(0 if ours == theirs and len(ours) == 10 else 1)
PY

# -- 01 --------------------------------------------------------------------
begin "01" "Handoff returns a receipt, not a wait"

python3 -I - <<'PY' && ok "every bundle opens with PROMISED, written before any tier was attempted" || bad "a bundle does not open with PROMISED ahead of every tier receipt"
import glob, json, sys
for path in sorted(glob.glob("docs/evidence/*.json")):
    events = json.load(open(path))["events"]
    if events[0]["event_type"] != "PROMISED" or any(e["event_type"] == "PROMISED" for e in events[1:]):
        sys.exit(path)
PY

python3 -I - <<'PY' && ok "raise may start the execution asynchronously and touch nothing else: no provider action, no synchronous start" || bad "raise's role allows more than the table and an asynchronous StartExecution"
import re, sys
tf = open("infra/lambda.tf", encoding="utf-8").read()
block = re.search(r"\n    raise = \[(.*?)\n    \]", tf, re.S).group(1)
actions = set(re.findall(r'"([a-z0-9-]+:[A-Za-z]+)"', block))
sys.exit(0 if actions == {"dynamodb:Query", "dynamodb:PutItem", "states:StartExecution"} else sorted(actions))
PY

if [ "$have_modules" -eq 1 ]; then
  gotests ./cmd/raise TestRaiseRecordsThePromiseAndStartsTheExecution TestFailedStartIsRecordedAsABrokenPromise \
    && ok "raise returns 202 with the PROMISED receipt's hash once the execution has started, and reports a failed start as broken" \
    || bad "cmd/raise tests"
else
  na "cmd/raise tests: $([ "$have_go" -eq 1 ] && echo "$NO_MODULES" || echo "$NO_GO")"
fi
end

# -- 02 --------------------------------------------------------------------
begin "02" "Contact policy is resolved before any provider is touched"

# Each test loads a Heart-Map document with values no default supplies:
# tier timeouts of 900 s and 60 s, a Chicago quiet window, and a budget of
# 11 an hour, burst 5, 240 s. The stated defaults are asserted separately.
if [ "$have_go" -eq 1 ]; then
  gotests ./internal/heartmap TestLoadParsesTheFullPolicy TestQuietHoursRespectTheContactTimezone TestDeclaredRateLimitIsTheOneUsed TestOmittedValuesTakeTheStatedDefaults TestLoadRejectsAZoneThatDoesNotLoad \
    && ok "who, which channels, what order, each tier's timeout, quiet hours, and the attention budget are read from the declared Heart-Map; an omitted value takes the stated default, and a zone that will not load is refused" \
    || bad "internal/heartmap loader tests"
  gotests ./internal/dignity TestDeclaredBurstIsTheBudgetSpent \
    && ok "the gate spends the budget the document declares: a declared burst of 5 delivers five and suppresses the sixth" \
    || bad "internal/dignity budget test"
else
  na "internal/heartmap and internal/dignity tests: $NO_GO"
fi

if [ "$have_modules" -eq 1 ]; then
  gotests ./cmd/attempt TestTierOneBreaksThenTierTwoSends TestQuietHoursDeferBeforeAnyProvider \
    && ok "attempt resolves the tier's channel, address and timeout from the Heart-Map, and the gate decides before any provider" \
    || bad "cmd/attempt policy tests"
else
  na "cmd/attempt policy tests: $([ "$have_go" -eq 1 ] && echo "$NO_MODULES" || echo "$NO_GO")"
fi
end

# -- 03 --------------------------------------------------------------------
begin "03" "Attention guardrails run first, and each decision writes a receipt"

if [ "$have_go" -eq 1 ]; then
  gotests ./internal/dispatcher TestSuppressedEscalationStillProducesAnAuditEvent TestQuietHoursDeferralIsAudited \
    && ok "a reach past the attention budget writes SUPPRESSED with its reason and is not delivered; one inside quiet hours writes DEFERRED" \
    || bad "internal/dispatcher gate receipt tests"
else
  na "internal/dispatcher gate receipt tests: $NO_GO"
fi

if [ "$have_modules" -eq 1 ]; then
  gotests ./cmd/attempt TestQuietHoursDeferBeforeAnyProvider TestCoalescedReachWritesItsReceiptAndTouchesNoProvider \
    && ok "a deferred reach and a coalesced reach each write one receipt, touch no provider, and walk no further tier" \
    || bad "cmd/attempt gate tests"
else
  na "cmd/attempt gate tests: $([ "$have_go" -eq 1 ] && echo "$NO_MODULES" || echo "$NO_GO")"
fi
end

# -- 04 --------------------------------------------------------------------
begin "04" "The ladder is walked in order, never in parallel"

python3 -I - <<'PY' && ok "in every bundle the tier receipts rise one tier at a time, and there is at most one SENT" || bad "a bundle walks tiers out of order or sends more than once"
import glob, json, sys
for path in sorted(glob.glob("docs/evidence/*.json")):
    events = json.load(open(path))["events"]
    tiers = [int(e["metadata"]["tier"]) for e in events if e["event_type"] in ("FAILED", "ESCALATED", "SENT") and "tier" in e["metadata"]]
    sends = sum(e["event_type"] == "SENT" for e in events)
    if tiers != sorted(set(tiers)) or sends > 1:
        sys.exit(path)
PY

python3 -I - <<'PY' && ok "the state machine has no Parallel or Map state, and AwaitAnswer waits the delivering tier's timeout from attempt's output" || bad "infra/state.tf can run tiers together or waits on something other than the tier's timeout"
import re, sys
tf = open("infra/state.tf", encoding="utf-8").read()
if re.search(r'Type\s*=\s*"(Parallel|Map)"', tf):
    sys.exit("parallel state")
if not re.search(r'AwaitAnswer = \{[^}]*TimeoutSecondsPath = "\$\.attempt\.outcome\.timeout_seconds"', tf, re.S):
    sys.exit("timeout path")
PY

python3 -I - <<'PY' && ok "the only error any task retries is Lambda.TooManyRequestsException, refused before the function ran, so no retry is a second message" || bad "a task retries an error that can arrive after a message went out"
import re, sys
tf = open("infra/state.tf", encoding="utf-8").read()
retries = re.findall(r"Retry\s*=\s*(\S+)", tf)
errors = re.findall(r'ErrorEquals\s*=\s*\["([^"]+)"\]\s*\n\s*IntervalSeconds', tf)
sys.exit(0 if retries and set(retries) == {"local.invoke_retry"} and errors == ["Lambda.TooManyRequestsException"] else (retries, errors))
PY

if [ "$have_go" -eq 1 ]; then
  gotests ./internal/dispatcher TestAttemptTierWalksExactlyOneRung TestFailoverWalksTheLadderInOrder \
    && ok "one rung writes one receipt and touches no other tier's provider; failover walks the declared order" \
    || bad "internal/dispatcher ladder tests"
else
  na "internal/dispatcher ladder tests: $NO_GO"
fi

if [ "$have_modules" -eq 1 ]; then
  gotests ./cmd/attempt TestTierOneBreaksThenTierTwoSends \
    && ok "attempt walks one tier per invocation and returns that tier's Heart-Map timeout (300 s, then 900 s)" \
    || bad "cmd/attempt ladder test"
else
  na "cmd/attempt ladder test: $([ "$have_go" -eq 1 ] && echo "$NO_MODULES" || echo "$NO_GO")"
fi
end

# -- 05 --------------------------------------------------------------------
begin "05" "A broken promise is named as broken, and a skipped tier is signal"

python3 -I - <<'PY' && ok "the breaker run skips tier 1 with ESCALATED, breaker OPEN, reason \"circuit breaker is OPEN, tier skipped\", then sends on tier 2" || bad "the breaker run does not record its skip with the breaker state and reason"
import json, sys
events = json.load(open("docs/evidence/c05-breaker-01a16e4e-76fb.json"))["events"]
skip = [e for e in events if e["event_type"] == "ESCALATED"]
ok = (len(skip) == 1
      and skip[0]["metadata"].get("tier") == "1"
      and skip[0]["metadata"].get("breaker_state") == "OPEN"
      and skip[0]["metadata"].get("reason") == "circuit breaker is OPEN, tier skipped"
      and [e["event_type"] for e in events].index("ESCALATED") < [e["event_type"] for e in events].index("SENT"))
sys.exit(0 if ok else 1)
PY

python3 -I - <<'PY' && ok "each c09 run has one tier-1 receipt: a FAILED naming the broken promise and the breaker state, or an ESCALATED skip on an OPEN breaker; at least one is FAILED" || bad "a tier-1 outcome in the c09 runs is unnamed, lacks its breaker state, appears more than once, or no run shows the FAILED path"
import glob, json, sys
failed = 0
for path in sorted(glob.glob("docs/evidence/c09-*.json")):
    events = json.load(open(path))["events"]
    tier1 = [e for e in events if e["metadata"].get("tier") == "1"]
    if len(tier1) != 1:
        sys.exit(path)
    t, m = tier1[0]["event_type"], tier1[0]["metadata"]
    if t == "FAILED" and m.get("reason", "").startswith("promise broken: ") and m.get("breaker_state"):
        failed += 1
    elif not (t == "ESCALATED" and m.get("breaker_state") == "OPEN"
              and m.get("reason") == "circuit breaker is OPEN, tier skipped"):
        sys.exit(path)
if not failed:
    sys.exit("no c09 run takes the FAILED path")
PY

if [ "$have_go" -eq 1 ]; then
  gotests ./internal/dispatcher TestOpenBreakerSkipsTheTierAsSignal TestUnregisteredChannelBreaksThePromiseAndOpensTheBreaker TestUnreadableBreakerSkipsTheProviderOutLoud \
    && ok "an open breaker is skipped as ESCALATED with its state and never dialed; an unregistered channel is a named broken promise" \
    || bad "internal/dispatcher breaker tests"
else
  na "internal/dispatcher breaker tests: $NO_GO"
fi

if [ "$have_modules" -eq 1 ]; then
  gotests ./internal/providers/ses TestSendFailureIsReportedOnceAndDisclosesNothing \
    && ok "the SES provider reports a failed send once and does not retry it" \
    || bad "internal/providers/ses failure test"
  gotests ./cmd/attempt TestOpenBreakerHoldsAcrossColdStarts \
    && ok "a breaker opened by one escalation is skipped, with a receipt, by the next in a cold environment" \
    || bad "cmd/attempt breaker test"
else
  na "SES and attempt breaker tests: $([ "$have_go" -eq 1 ] && echo "$NO_MODULES" || echo "$NO_GO")"
fi
end

# -- 06 --------------------------------------------------------------------
begin "06" "A message is delivered on a live AWS Connected Devices and Services channel"

python3 -I - <<'PY' && ok "every bundle has exactly one SENT, from ses-v2 on email, carrying an id in SES's message-id format" || bad "a bundle's SENT is missing, not from ses-v2, or carries no SES message id"
import glob, json, re, sys
ses_id = re.compile(r"^[0-9a-f]{16}-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-[0-9]{6}$")
for path in sorted(glob.glob("docs/evidence/*.json")):
    sent = [e for e in json.load(open(path))["events"] if e["event_type"] == "SENT"]
    if len(sent) != 1 or sent[0]["provider_name"] != "ses-v2" or sent[0]["metadata"].get("channel") != "email" \
            or not ses_id.match(sent[0]["metadata"].get("provider_msg_id", "")):
        sys.exit(path)
PY

python3 -I - <<'PY' && ok "delivery, not only acceptance: answered runs have a RECEIVED after that SENT, through the link only that message carried" || bad "no bundle shows a person answering the delivered message"
import glob, json, sys
answered = 0
for path in sorted(glob.glob("docs/evidence/*.json")):
    types = [e["event_type"] for e in json.load(open(path))["events"]]
    if "RECEIVED" in types and types.index("SENT") < types.index("RECEIVED"):
        answered += 1
sys.exit(0 if answered >= 1 else 1)
PY

python3 -I - <<'PY' && ok "WhatsApp and SMS are declared on the ladder, and no role in infra/ may send on them: SES is the only messaging action granted" || bad "the ladder does not declare the unprovisioned tiers, or a role can send on a channel other than SES"
import glob, re, sys
hm = open("infra/heartmap.yaml.tftpl", encoding="utf-8").read()
ladder = re.findall(r"channel: (\w+)", hm)
tf = "".join(open(p, encoding="utf-8").read() for p in glob.glob("infra/*.tf"))
messaging = set(re.findall(r'"((?:ses|sns|sms-voice|social-messaging|mobiletargeting|connect):[A-Za-z]+)"', tf))
sys.exit(0 if ladder == ["whatsapp", "email", "sms"] and messaging == {"ses:SendEmail", "ses:GetAccount"} else (ladder, messaging))
PY

python3 -I - <<'PY' && ok "no c09 run sends on tier 1, and the set records it as unprovisioned: \"no provider registered for channel whatsapp\"" || bad "a c09 run sent on tier 1, or no run records tier 1 as unprovisioned"
import glob, json, sys
unprovisioned = False
for path in sorted(glob.glob("docs/evidence/c09-*.json")):
    events = json.load(open(path))["events"]
    if any(e["event_type"] == "SENT" and e["metadata"].get("tier") == "1" for e in events):
        sys.exit(path)
    unprovisioned |= any(e["metadata"].get("reason") == "promise broken: no provider registered for channel whatsapp"
                         for e in events)
if not unprovisioned:
    sys.exit("no c09 run records tier 1 as unprovisioned")
PY

if [ "$have_modules" -eq 1 ]; then
  gotests ./internal/providers/ses TestSendReturnsSESMessageID TestSendRejectsMissingMessageID \
    && ok "the id on SENT is the one SES returned, and a send without one is not reported as sent" \
    || bad "internal/providers/ses message-id tests"
else
  na "SES message-id tests: $([ "$have_go" -eq 1 ] && echo "$NO_MODULES" || echo "$NO_GO")"
fi
end

# -- 07 --------------------------------------------------------------------
begin "07" "The message is enough to decide on, by itself"

python3 -I - <<'PY' && ok "GET and POST /reply have no authorizer: nothing on the answer path sits behind a login" || bad "a reply route has an authorizer"
import re, sys
tf = open("infra/api.tf", encoding="utf-8").read()
if re.search(r"authoriz", tf, re.I):
    sys.exit("authorizer present")
routes = re.search(r"routes = \{(.*?)\n  \}", tf, re.S).group(1)
sys.exit(0 if '"GET /reply"' in routes and '"POST /reply"' in routes else "reply routes missing")
PY

if [ "$have_modules" -eq 1 ]; then
  gotests ./cmd/attempt TestTierOneBreaksThenTierTwoSends TestBodyCarriesNoMarkerOrSubjectAndLocalExpiry TestSentMessageCarriesAVerifiableLink \
    && ok "the email opens with the agent's payload, states the requested response and a local expiry, and carries its own answer link" \
    || bad "cmd/attempt message tests"
  gotests ./cmd/reply TestGetRendersTheDecisionAndConsumesNothing TestPostDirectionReleases \
    && ok "the link alone renders the subject, payload, requested response and both answers, and one POST from it records the decision" \
    || bad "cmd/reply answer-page tests"
else
  na "attempt and reply message tests: $([ "$have_go" -eq 1 ] && echo "$NO_MODULES" || echo "$NO_GO")"
fi
end

# -- 08 --------------------------------------------------------------------
begin "08" "The answer returns to the caller, classified by intent"

python3 -I - <<'PY' && ok "every answered run has RECEIVED then ACKNOWLEDGED with an intent, a classifier, and a confidence naming what was checked" || bad "an answered run lacks its classification, or its confidence claims a check the code does not make"
import glob, json, sys
for path in sorted(glob.glob("docs/evidence/*.json")):
    events = json.load(open(path))["events"]
    types = [e["event_type"] for e in events]
    if "RECEIVED" not in types:
        continue
    i = types.index("RECEIVED")
    ack = events[i + 1] if i + 1 < len(events) else {}
    m = ack.get("metadata", {})
    if ack.get("event_type") != "ACKNOWLEDGED" or m.get("intent") not in ("direction", "denial") \
            or not m.get("classifier") \
            or m.get("confidence") not in ("button", "button; note contains no contradicting term"):
        sys.exit(path)
PY

python3 -I - <<'PY' && ok "every receipt in a bundle names the one originating request, so the answer is bound to it" || bad "a bundle mixes receipts from more than one request"
import glob, json, sys
for path in sorted(glob.glob("docs/evidence/*.json")):
    if len({e["request_id"] for e in json.load(open(path))["events"]}) != 1:
        sys.exit(path)
PY

if [ "$have_modules" -eq 1 ]; then
  gotests ./cmd/reply TestPostDirectionReleases TestContradictoryAnswerIsHandedBack \
    && ok "the decision handed to the execution is the classified intent; a note that contradicts the button is handed back, not taken as the button" \
    || bad "cmd/reply classification tests"
  gotests ./cmd/status TestDirectionReportsReleased TestDenialIsNotReleased TestResponseFieldNamesArePinned \
    && ok "GET /escalation/{id} returns the decision and whether anything was released, under the field names a caller reads" \
    || bad "cmd/status decision tests"
else
  na "reply and status tests: $([ "$have_go" -eq 1 ] && echo "$NO_MODULES" || echo "$NO_GO")"
fi
end

# -- 09 --------------------------------------------------------------------
begin "09" "Only an answer releases the held action"

python3 -I - <<'PY' && ok "the bundles hold a direction that releases, a denial and a timeout that do not, and released is true only after an ACKNOWLEDGED direction" || bad "a release without a direction, or a missing outcome"
import glob, json, sys
seen = set()
for path in sorted(glob.glob("docs/evidence/*.json")):
    events = json.load(open(path))["events"]
    ends = [e for e in events if "released" in e["metadata"]]
    if len(ends) != 1 or ends[0] is not events[-1]:
        sys.exit(path)
    end = ends[0]
    directed = any(e["event_type"] == "ACKNOWLEDGED" and e["metadata"].get("intent") == "direction" for e in events)
    released = end["metadata"]["released"]
    if end["event_type"] == "RESOLVED":
        if released != "true" or not directed:
            sys.exit(path)
        seen.add("direction")
    elif end["event_type"] == "DENIED":
        if released != "false" or directed:
            sys.exit(path)
        seen.add("denial")
    elif end["event_type"] == "TIMED_OUT":
        if released != "false" or any(e["event_type"] in ("RECEIVED", "ACKNOWLEDGED") for e in events):
            sys.exit(path)
        seen.add("timeout")
    elif released != "false":
        sys.exit(path)
sys.exit(0 if seen == {"direction", "denial", "timeout"} else sorted(seen))
PY

python3 -I - <<'PY' && ok "Release has one way in, from DecideAnswer on decision \"direction\"; DecideAnswer defaults to Denied, and a timeout goes to TimedOut" || bad "infra/state.tf can reach Release some other way"
import re, sys
tf = open("infra/state.tf", encoding="utf-8").read()
into_release = re.findall(r'Next\s*=\s*"Release"', tf)
decide = re.search(r"DecideAnswer = \{(.*?)\n      \}", tf, re.S).group(1)
ok = (len(into_release) == 1
      and re.search(r'Variable\s*=\s*"\$\.answer\.decision"\s*\n\s*StringEquals\s*=\s*"direction"\s*\n\s*Next\s*=\s*"Release"', decide)
      and re.search(r'Default\s*=\s*"Denied"', decide)
      and re.search(r'ErrorEquals\s*=\s*\["States\.Timeout"\][^}]*Next\s*=\s*"TimedOut"', tf, re.S))
sys.exit(0 if ok else 1)
PY

if [ "$have_modules" -eq 1 ]; then
  gotests ./cmd/reply TestPostDirectionReleases TestPostDenialReleasesNothing TestTimeoutClosesTheWindow \
    && ok "release refuses unless the stored decision is a direction; a denial and a timeout write released false" \
    || bad "cmd/reply release tests"
else
  na "cmd/reply release tests: $([ "$have_go" -eq 1 ] && echo "$NO_MODULES" || echo "$NO_GO")"
fi
end

# -- 10 --------------------------------------------------------------------
begin "10" "The chain verifies cold, with no message body stored"

all_verify=1
for bundle in docs/evidence/*.json; do
  python3 -I verify/verify_chain.py "$bundle" --pubkey demo/keys/reachee-ed25519.pub >/dev/null || { all_verify=0; printf '        %s\n' "$bundle"; }
done
[ "$all_verify" -eq 1 ] \
  && ok "every bundle verifies with verify/verify_chain.py against demo/keys: every signature, every link, no body-sized field" \
  || bad "a bundle does not verify against demo/keys"

python3 -I - "$tmpdir" <<'PY' && ok "the verifier rejects an edited receipt, a reordered chain, and the wrong key" || bad "the verifier accepted a chain it should have rejected"
import glob, json, subprocess, sys
tmp = sys.argv[1]
(src,) = glob.glob("docs/evidence/c09-denial-*.json")
key = open("demo/keys/reachee-ed25519.pub").read().strip()

def rejects(bundle, pubkey):
    open(tmp + "/b.json", "w").write(json.dumps(bundle))
    open(tmp + "/k.pub", "w").write(pubkey)
    run = subprocess.run([sys.executable, "-I", "verify/verify_chain.py", tmp + "/b.json", "--pubkey", tmp + "/k.pub"],
                         capture_output=True)
    return run.returncode != 0

edited = json.load(open(src)); edited["events"][-1]["metadata"]["released"] = "true"
reordered = json.load(open(src)); reordered["events"][3], reordered["events"][4] = reordered["events"][4], reordered["events"][3]
wrong_key = key[:-1] + ("0" if key[-1] != "0" else "1")
sys.exit(0 if rejects(edited, key) and rejects(reordered, key) and rejects(json.load(open(src)), wrong_key) else 1)
PY

python3 -I - <<'PY' && ok "the demo scenario's subject, payload, and reading appear nowhere in any bundle" || bad "the demo scenario's content appears in a bundle"
import glob, re, sys
page = open("demo/index.html", encoding="utf-8").read()
subject = re.search(r'subject: "([^"]+)"', page).group(1)
payload = re.search(r'payload: "([^"]+)"', page).group(1)
needles = [subject, payload, "214 mmHg", "mmHg"]
for path in sorted(glob.glob("docs/evidence/*.json")):
    text = open(path, encoding="utf-8").read()
    if any(n in text for n in needles):
        sys.exit(path)
PY

python3 -I - <<'PY' && ok "no receipt carries a caller's correlation id in plaintext" || bad "receipts carry the caller's correlation id in plaintext, an identifier in the log"
import glob, json, sys
leaks = [path for path in sorted(glob.glob("docs/evidence/*.json"))
         if any("correlation_id" in e["metadata"] for e in json.load(open(path))["events"])]
for path in leaks:
    print("        " + path)
sys.exit(1 if leaks else 0)
PY

if [ "$have_go" -eq 1 ]; then
  gotests ./internal/audit TestChainVerifies TestModifiedEventBreaksTheChain TestReorderingBreaksTheChain TestWrongKeyFailsVerification TestContentNeverEntersTheLog TestCanonicalMatchesGolden \
    && ok "the Go side signs and chains every event, rejects edits, reordering and the wrong key, keeps content out, and matches the golden form" \
    || bad "internal/audit tests"
  gotests ./internal/dispatcher TestPayloadNeverReachesTheAuditLog TestCorrelationIDEntersTheChainOnlyAsAHash \
    && ok "the payload never reaches a receipt, and a correlation id enters only as its hash" \
    || bad "internal/dispatcher content tests"
else
  na "internal/audit and dispatcher content tests: $NO_GO"
fi
python3 -I verify/verify_chain.py --selftest testdata/canonical_golden.txt >/dev/null \
  && ok "the independent verifier matches the golden form" \
  || bad "the verifier disagrees with testdata/canonical_golden.txt"
end

# -- summary ---------------------------------------------------------------
count() { set -- $1; echo $#; }
if [ -n "$failed" ]; then
  printf '\ncriteria FAILED:%s' "$failed"
  [ -n "$not_asserted" ] && printf '; %d not asserted:%s' "$(count "$not_asserted")" "$not_asserted"
  printf '\n'
  exit 1
fi
if [ -n "$not_asserted" ] || [ "$(count "$met")" -ne 10 ]; then
  printf '\ncriteria incomplete: %d met, %d not asserted:%s\n' "$(count "$met")" "$(count "$not_asserted")" "$not_asserted"
  if [ "$have_go" -eq 0 ]; then
    printf 'install Go 1.24 or later, run go mod download once, then re-run make criteria\n'
  else
    printf 'run go mod download once, then re-run make criteria\n'
  fi
  exit 2
fi
printf '\ncriteria green\n'

