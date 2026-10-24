#!/usr/bin/env bash
#
# Reachee preflight. Run this every morning before you build anything.
#
# It answers one question: is the ground underneath today's work still solid?
# Identity, region, SES, and the cross-language agreement that criterion 10
# depends on. It does not deploy and it does not send unless you ask it to.
#
#   ./scripts/preflight.sh            checks 1 through 4
#   ./scripts/preflight.sh --send     adds a real SES send, check 5
#
# Requires REACHEE_SENDER and REACHEE_RECIPIENT in the environment. Put them
# in env.sh alongside the AWS variables.

set -euo pipefail

REGION_EXPECTED="${AWS_REGION:-us-west-2}"
SEND=0
[ "${1:-}" = "--send" ] && SEND=1

pass() { printf '  ok    %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1" >&2; exit 1; }
head() { printf '\n%s\n' "$1"; }

cd "$(dirname "$0")/.."

# -- 1. identity and region ------------------------------------------------
head "1. identity and region"

arn="$(aws sts get-caller-identity --query Arn --output text)" \
  || fail "cannot reach AWS, check AWS_PROFILE and your credentials"
case "$arn" in
  *:root) fail "running as root: create an IAM user and switch to it" ;;
  *)      pass "identity $arn" ;;
esac

profile_region="$(aws configure get region || true)"
[ "$profile_region" = "$REGION_EXPECTED" ] \
  || fail "profile region is '$profile_region', expected '$REGION_EXPECTED'"
pass "profile region $profile_region"

[ "${AWS_REGION:-}" = "${AWS_DEFAULT_REGION:-}" ] \
  || fail "AWS_REGION and AWS_DEFAULT_REGION disagree, the CLI and Terraform will target different regions"
pass "AWS_REGION and AWS_DEFAULT_REGION agree"

# -- 2. SES ----------------------------------------------------------------
head "2. SES identities"

: "${REACHEE_SENDER:?set REACHEE_SENDER in env.sh}"
: "${REACHEE_RECIPIENT:?set REACHEE_RECIPIENT in env.sh}"

enforcement="$(aws sesv2 get-account --query EnforcementStatus --output text)"
[ "$enforcement" = "HEALTHY" ] || fail "SES enforcement status is '$enforcement'"
pass "account enforcement HEALTHY"

for address in "$REACHEE_SENDER" "$REACHEE_RECIPIENT"; do
  status="$(aws sesv2 get-email-identity --email-identity "$address" \
            --query VerifiedForSendingStatus --output text 2>/dev/null || echo MISSING)"
  [ "$status" = "True" ] || fail "$address is not verified (status: $status)"
  pass "verified $address"
done

# -- 3. canonicalization parity --------------------------------------------
# The check people skip, and then lose criterion 10 to on camera. Go and
# Python are each pinned to testdata/canonical_golden.txt rather than to one
# another, so drift in either shows up here.
head "3. canonicalization parity"

go test ./internal/audit/ -run TestCanonical -count=1 >/dev/null \
  || fail "the Go encoder no longer matches the golden canonical form"
pass "Go encoder matches the golden form"

python3 verify/verify_chain.py --selftest testdata/canonical_golden.txt >/dev/null \
  || fail "the Python verifier no longer matches the golden canonical form"
pass "Python verifier matches the golden form"

# -- 4. a chain that a stranger can verify ---------------------------------
head "4. round trip, signed in Go and verified in Python"

tmp="$(mktemp -t reachee-bundle)"
trap 'rm -f "$tmp"' EXIT

go run ./cmd/reachee export "$tmp" >/dev/null || fail "chain export failed"
pass "exported a signed chain"

python3 verify/verify_chain.py "$tmp" >/dev/null \
  || fail "the independent verifier rejected a freshly signed chain"
pass "independent verifier accepted it"

# Tampering must be caught. A verifier that accepts everything proves nothing.
tampered="$(mktemp -t reachee-tampered)"
trap 'rm -f "$tmp" "$tampered"' EXIT
python3 - "$tmp" "$tampered" <<'PY'
import json, sys
bundle = json.load(open(sys.argv[1]))
bundle["events"][0].setdefault("metadata", {})["reason"] = "nothing was wrong"
json.dump(bundle, open(sys.argv[2], "w"))
PY
if python3 verify/verify_chain.py "$tampered" >/dev/null 2>&1; then
  fail "the verifier accepted a tampered chain, which makes it decoration"
fi
pass "tampering rejected"

# A body hidden in metadata must be named as such, not merely fail as a bad
# signature, so the check is shown to look inside metadata.
leaky="$(mktemp -t reachee-leaky)"
trap 'rm -f "$tmp" "$tampered" "$leaky"' EXIT
python3 - "$tmp" "$leaky" <<'PY'
import json, sys
b = json.load(open(sys.argv[1]))
b["events"][0].setdefault("metadata", {})["note"] = "x" * 201
json.dump(b, open(sys.argv[2], "w"))
PY
leak_report="$(python3 verify/verify_chain.py "$leaky" 2>&1 || true)"
if ! grep -q '\.metadata\.note$' <<<"$leak_report"; then
  fail "the verifier did not flag a body-sized metadata value"
fi
pass "body-sized metadata value flagged"

# -- 5. a real send --------------------------------------------------------
if [ "$SEND" -eq 1 ]; then
  head "5. live SES send"
  body="$(mktemp -t reachee-content)"
  trap 'rm -f "$tmp" "$tampered" "$leaky" "$body"' EXIT
  cat > "$body" <<JSON
{ "Simple": {
    "Subject": { "Data": "Reachee preflight", "Charset": "UTF-8" },
    "Body": { "Text": { "Data": "Preflight send at $(date -u +%FT%TZ).", "Charset": "UTF-8" } } } }
JSON
  message_id="$(aws sesv2 send-email \
    --from-email-address "$REACHEE_SENDER" \
    --destination ToAddresses="$REACHEE_RECIPIENT" \
    --content "file://$body" \
    --query MessageId --output text)" || fail "SES rejected the send"
  pass "sent, MessageId $message_id"
fi

printf '\npreflight green\n'
