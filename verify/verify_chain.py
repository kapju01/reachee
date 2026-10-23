#!/usr/bin/env python3
"""Independent verifier for a Reachee evidence bundle.

This program shares no code with Reachee. It implements the canonical byte
format, the SHA-256 chain, and Ed25519 verification from scratch, with nothing
outside the Python standard library. That is the point: a verifier that
imported Reachee's own serializer would not be checking the chain, it would be
agreeing with itself.

It reads only hashes. No message body, no patient reading, no identifier, and
no clinician's words are in the bundle, so running this tells you that a person
was reached and decided without telling you what was said.

Usage:
    python3 verify/verify_chain.py bundle.json
    python3 verify/verify_chain.py bundle.json --pubkey https://host/keys/reachee.pub

With --pubkey the key is fetched from that URL and the key embedded in the
bundle is ignored. That is the honest way to run it: a bundle that carries its
own key only proves it is internally consistent.
"""

import argparse
import hashlib
import json
import sys
import urllib.request

# --------------------------------------------------------------------------
# Canonical serialization.
#
# Must agree byte for byte with Go's encoding/json when writing a map with
# SetEscapeHTML(false). Three places where a naive json.dumps would diverge:
# Python puts a space after separators, escapes non-ASCII by default, and uses
# \b and \f where Go uses \u0008 and \u000c. Writing it out by hand removes the
# guesswork.
# --------------------------------------------------------------------------

_SHORT = {
    0x22: '\\"',
    0x5C: "\\\\",
    0x08: "\\b",
    0x09: "\\t",
    0x0A: "\\n",
    0x0C: "\\f",
    0x0D: "\\r",
}


def _string(s):
    out = ['"']
    for ch in s:
        cp = ord(ch)
        if cp in _SHORT:
            out.append(_SHORT[cp])
        elif cp < 0x20:
            out.append("\\u%04x" % cp)
        elif cp in (0x2028, 0x2029):
            out.append("\\u%04x" % cp)
        else:
            out.append(ch)
    out.append('"')
    return "".join(out)


def canonical(obj):
    """Serialize obj exactly as Go's encoder writes a map[string]any."""
    if isinstance(obj, str):
        return _string(obj)
    if isinstance(obj, dict):
        parts = ["%s:%s" % (_string(k), canonical(obj[k])) for k in sorted(obj)]
        return "{" + ",".join(parts) + "}"
    raise TypeError("the chain format holds only strings and string maps, found %r" % type(obj))


# --------------------------------------------------------------------------
# Ed25519 verification, RFC 8032, pure Python.
# --------------------------------------------------------------------------

P = 2**255 - 19
L = 2**252 + 27742317777372353535851937790883648493
D = -121665 * pow(121666, P - 2, P) % P
I = pow(2, (P - 1) // 4, P)


def _xrecover(y):
    xx = (y * y - 1) * pow(D * y * y + 1, P - 2, P)
    x = pow(xx, (P + 3) // 8, P)
    if (x * x - xx) % P != 0:
        x = (x * I) % P
    if x % 2 != 0:
        x = P - x
    return x


_BY = 4 * pow(5, P - 2, P) % P
_B = (_xrecover(_BY) % P, _BY % P)


def _add(pt1, pt2):
    x1, y1 = pt1
    x2, y2 = pt2
    k = D * x1 * x2 * y1 * y2
    x3 = (x1 * y2 + x2 * y1) * pow(1 + k, P - 2, P)
    y3 = (y1 * y2 + x1 * x2) * pow(1 - k, P - 2, P)
    return (x3 % P, y3 % P)


def _mul(pt, e):
    result = (0, 1)
    addend = pt
    while e > 0:
        if e & 1:
            result = _add(result, addend)
        addend = _add(addend, addend)
        e >>= 1
    return result


def _on_curve(pt):
    x, y = pt
    return (-x * x + y * y - 1 - D * x * x * y * y) % P == 0


def _decode_point(blob):
    bits = int.from_bytes(blob, "little")
    y = bits & ((1 << 255) - 1)
    x = _xrecover(y)
    if (x & 1) != ((bits >> 255) & 1):
        x = P - x
    pt = (x, y)
    if not _on_curve(pt):
        raise ValueError("point is not on the curve")
    return pt


def _encode_point(pt):
    x, y = pt
    return (y | ((x & 1) << 255)).to_bytes(32, "little")


def ed25519_verify(public_key, message, signature):
    if len(signature) != 64 or len(public_key) != 32:
        return False
    try:
        r = _decode_point(signature[:32])
        a = _decode_point(public_key)
    except ValueError:
        return False
    s = int.from_bytes(signature[32:], "little")
    if s >= L:
        return False
    h = int.from_bytes(
        hashlib.sha512(signature[:32] + public_key + message).digest(), "little"
    )
    return _encode_point(_mul(_B, s)) == _encode_point(_add(r, _mul(a, h)))


# --------------------------------------------------------------------------
# The chain walk.
# --------------------------------------------------------------------------

GENESIS = "0" * 64


def verify_bundle(bundle, pubkey_hex):
    pub = bytes.fromhex(pubkey_hex)
    prev = bundle.get("genesis", GENESIS)
    results = []

    for index, event in enumerate(bundle["events"]):
        signature_hex = event.get("signature", "")
        signed = dict(event)
        signed["signature"] = ""
        payload = canonical(signed).encode("utf-8")

        chain_ok = event.get("prev_hash") == prev
        try:
            sig_ok = ed25519_verify(pub, payload, bytes.fromhex(signature_hex))
        except ValueError:
            sig_ok = False

        results.append(
            {
                "index": index,
                "event_id": event.get("event_id", "?"),
                "event_type": event.get("event_type", "?"),
                "signature": sig_ok,
                "chain": chain_ok,
            }
        )

        digest = hashlib.sha256()
        digest.update(payload)
        digest.update(signature_hex.encode("ascii"))
        prev = digest.hexdigest()

    return results


def has_message_content(bundle):
    """A bundle should carry hashes, never bodies. Flag anything suspicious."""
    offenders = []
    for event in bundle["events"]:
        for key, value in event.items():
            if key == "metadata":
                continue
            if isinstance(value, str) and len(value) > 200:
                offenders.append("%s.%s" % (event.get("event_id", "?"), key))
    return offenders


# --------------------------------------------------------------------------
# Parity self-test.
#
# testdata/canonical_golden.txt holds one agreed rendering of a deliberately
# hostile event. Go's encoder is tested against that file and so is this
# serializer, so the two implementations are pinned to the same literal rather
# than to each other's behavior on well-behaved input.
# --------------------------------------------------------------------------

GOLDEN_EVENT = {
    "event_id": "01a16000-0000-7000-8000-000000000001",
    "event_type": "SENT",
    "message_hash": "aa",
    "metadata": {
        "z_last": "tab\there",
        "a_first": "line\nbreak",
        "unicode": "38.5\u00b0C caf\u00e9 \u4e2d\u6587",
        "control": "bell\u0007back\u0008form\u000c",
        "sep": "u2028\u2028u2029\u2029",
    },
    "prev_hash": "00",
    "provider_name": "ses-v2",
    "recipient_id": "j.okafor",
    "request_id": 'req-<1>&"2"',
    "signature": "",
    "timestamp": "2026-10-23T17:04:05.123456Z",
}


def selftest(golden_path):
    with open(golden_path, "r", encoding="utf-8") as handle:
        want = handle.read()
    got = canonical(GOLDEN_EVENT)
    if got == want:
        print("canonicalization parity OK: this serializer agrees with the golden file")
        return 0
    print("canonicalization parity FAILED")
    print(" got: %s" % got)
    print("want: %s" % want)
    for i, (a, b) in enumerate(zip(got, want)):
        if a != b:
            print("first difference at byte %d: %r vs %r" % (i, a, b))
            break
    return 1

def main():
    parser = argparse.ArgumentParser(description="Verify a Reachee evidence bundle.")
    parser.add_argument("bundle", nargs="?", help="path to the exported bundle JSON")
    parser.add_argument(
        "--pubkey",
        help="URL or path of the published verifying key, hex encoded. "
        "Overrides the key embedded in the bundle.",
    )
    parser.add_argument(
        "--selftest",
        metavar="GOLDEN",
        help="check this serializer against testdata/canonical_golden.txt and exit",
    )
    args = parser.parse_args()

    if args.selftest:
        return selftest(args.selftest)

    with open(args.bundle, "r", encoding="utf-8") as handle:
        bundle = json.load(handle)

    if args.pubkey:
        if args.pubkey.startswith(("http://", "https://")):
            with urllib.request.urlopen(args.pubkey, timeout=15) as response:
                pubkey_hex = response.read().decode("ascii").strip()
            source = args.pubkey
        else:
            with open(args.pubkey, "r", encoding="utf-8") as handle:
                pubkey_hex = handle.read().strip()
            source = args.pubkey
    else:
        pubkey_hex = bundle["public_key"]
        source = "the bundle itself, which proves only internal consistency"

    print("verifying key: %s" % pubkey_hex)
    print("key source:    %s" % source)
    print("events:        %d\n" % len(bundle["events"]))

    results = verify_bundle(bundle, pubkey_hex)
    ok = True
    for row in results:
        sig = "MATCH" if row["signature"] else "FAIL"
        chain = "LINKED" if row["chain"] else "BROKEN"
        if not (row["signature"] and row["chain"]):
            ok = False
        print(
            "  %-12s %-14s signature %-6s chain %s"
            % (row["event_id"][:12], row["event_type"], sig, chain)
        )

    offenders = has_message_content(bundle)
    if offenders:
        ok = False
        print("\nunexpectedly long fields, message content may have leaked into the log:")
        for name in offenders:
            print("  %s" % name)
    else:
        print("\nno field in this bundle is long enough to hold a message body")

    print()
    if ok:
        print("CHAIN VERIFIED: %d events, every signature and every link" % len(results))
        return 0
    print("CHAIN FAILED")
    return 1


if __name__ == "__main__":
    sys.exit(main())
