"""The cross-language vectors: the same bytes and signatures the Go package and the engine pin."""

import json
from pathlib import Path

import pytest

from mishmar import AgentKey, Proposal, verify

VECTORS = (
    Path(__file__).resolve().parents[2] / "spec" / "vectors" / "proposal-signing.json"
)


def load():
    with VECTORS.open(encoding="utf-8") as f:
        return json.load(f)


@pytest.mark.parametrize("case", load()["cases"], ids=lambda c: c["name"])
def test_signing_bytes_and_signature(case):
    vectors = load()
    key = AgentKey.parse(vectors["secret"])
    assert key.public_key_b64 == vectors["publicKey"]
    p = Proposal.from_wire(case["proposal"])
    assert p.signing_bytes() == case["signingBytes"].encode("utf-8")
    assert p.sign(key).signature == case["signature"]
    assert verify(vectors["publicKey"], p.signing_bytes(), case["signature"])
    # The signed wire body round-trips and still signs identically.
    again = Proposal.from_wire(p.to_wire())
    assert again.signing_bytes() == p.signing_bytes()
    assert again.signature == case["signature"]
