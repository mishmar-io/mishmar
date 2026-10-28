"""The agentAuth vectors: the same bytes and signatures the Go package pins."""

import json
from pathlib import Path

import pytest

from mishmar import AgentKey, request_signing_bytes, verify, verify_request
from mishmar.agentkey import (
    HEADER_KEY_ID,
    HEADER_NONCE,
    HEADER_SIGNATURE,
    HEADER_SIGNED_AT,
)

VECTORS = (
    Path(__file__).resolve().parents[2]
    / "spec"
    / "vectors"
    / "agent-request-signing.json"
)


def load():
    with VECTORS.open(encoding="utf-8") as f:
        return json.load(f)


@pytest.mark.parametrize("case", load()["cases"], ids=lambda c: c["name"])
def test_vector(case):
    vectors = load()
    key = AgentKey.parse(vectors["secret"])
    body = case.get("body", "").encode("utf-8")
    message = request_signing_bytes(
        key_id=case["keyId"],
        method=case["method"],
        path=case["path"],
        query=case.get("query", ""),
        body=body,
        nonce=case["nonce"],
        signed_at=case["signedAt"],
    )
    assert message == case["signingBytes"].encode("utf-8")
    assert key.sign(message) == case["signature"]
    assert verify(vectors["publicKey"], message, case["signature"])
    assert verify_request(
        vectors["publicKey"],
        key_id=case["keyId"],
        method=case["method"],
        path=case["path"],
        query=case.get("query", ""),
        body=body,
        nonce=case["nonce"],
        signed_at=case["signedAt"],
        signature_b64=case["signature"],
    )


def test_sign_request_makes_the_headers():
    key = AgentKey.from_seed(bytes(range(32)))
    headers = key.sign_request("key_a", "get", "/agent/positions")
    assert headers[HEADER_KEY_ID] == "key_a"
    assert len(headers[HEADER_NONCE]) == 32
    assert headers[HEADER_SIGNED_AT].endswith("Z")
    assert verify_request(
        key.public_key_b64,
        key_id="key_a",
        method="GET",
        path="/agent/positions",
        query="",
        body=b"",
        nonce=headers[HEADER_NONCE],
        signed_at=headers[HEADER_SIGNED_AT],
        signature_b64=headers[HEADER_SIGNATURE],
    )
