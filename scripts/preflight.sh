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

skipped=0
pass() { printf '  ok    %s\n' "$1"; }
skip() { printf '  SKIP  %s\n' "$1"; skipped=$((skipped + 1)); }
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

# The demo page carries a third serializer and verifier, in the browser. Its
# copy of the golden form must be the file itself, and its verifier block,
# run as written, must agree with the golden form, accept every committed
# evidence chain against the published key, and reject a tampered one.
python3 - <<'PY' || fail "demo/index.html carries a golden form that is not testdata/canonical_golden.txt"
import json, re
page = open("demo/index.html", encoding="utf-8").read()
literal = re.search(r"/\* golden:begin \*/\s*const GOLDEN = (.*?);\s*/\* golden:end \*/", page, re.S).group(1)
assert json.loads(literal) == open("testdata/canonical_golden.txt", encoding="utf-8").read()
PY
pass "demo page carries the golden form"

if command -v node >/dev/null 2>&1; then
  node - <<'JS' || fail "the demo page's verifier disagrees with the golden form or the evidence"
const fs = require("fs");
const page = fs.readFileSync("demo/index.html", "utf8");
const golden = fs.readFileSync("testdata/canonical_golden.txt", "utf8");
const block = page.match(/\/\* verifier:begin([\s\S]*?)\/\* verifier:end \*\//)[1].replace(/^[\s\S]*?\*\//, "");
const api = new Function(block + "; return {canonical, verifyBundle, tamperRejected};")();
const pub = fs.readFileSync("demo/keys/reachee-ed25519.pub", "utf8").trim();
(async () => {
  if (api.canonical(JSON.parse(golden)) !== golden) throw new Error("golden parity");
  const files = fs.readdirSync("docs/evidence").filter(f => f.endsWith(".json"));
  if (!files.length) throw new Error("no evidence bundles");
  for (const f of files) {
    const b = JSON.parse(fs.readFileSync("docs/evidence/" + f, "utf8"));
    const r = await api.verifyBundle(b, pub);
    if (!r.signaturesChecked) throw new Error("node has no Ed25519 in WebCrypto");
    if (!r.linksOK || !r.sigsOK || r.offenders.length) throw new Error(f + " did not verify");
    if (!(await api.tamperRejected(b, pub))) throw new Error(f + ": tampering accepted");
  }
})().catch(e => { console.error(e.message); process.exit(1); });
JS
  pass "demo page verifier matches the golden form and verifies the committed evidence"
else
  skip "demo page verifier not run: node is not installed"
fi

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

if [ "$skipped" -gt 0 ]; then
  printf '\npreflight green, %d skipped\n' "$skipped"
else
  printf '\npreflight green\n'
fi
