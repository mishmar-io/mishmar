"""The agent's identity: an Ed25519 key whose public half the engine stores.

The engine shows the secret once, as ``msk_<prefix>_<seed>``: a label, then
the 32-byte seed in base64url without padding. The key id (``key_…``) is
separate and public.

The key signs two things: proposals (see proposal.py) and, under the
contract's agentAuth scheme, every other agent-facing request: four headers
carry the key id, the time, a nonce and an Ed25519 signature over the canonical
JSON of ``{bodySha256, keyId, method, nonce, path, query, signedAt}``. The bytes
are pinned in spec/vectors/agent-request-signing.json.
"""

from __future__ import annotations

import base64
import hashlib
import secrets
from datetime import UTC, datetime

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import (
    Ed25519PrivateKey,
    Ed25519PublicKey,
)
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat

from ._time import rfc3339
from .canon import canonical_json

SEED_SIZE = 32

HEADER_KEY_ID = "X-Mishmar-Key-Id"
HEADER_SIGNED_AT = "X-Mishmar-Signed-At"
HEADER_NONCE = "X-Mishmar-Nonce"
HEADER_SIGNATURE = "X-Mishmar-Signature"


def _b64url_decode(s: str) -> bytes:
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def new_nonce() -> str:
    """A fresh nonce: 16 random bytes, hex."""
    return secrets.token_hex(16)


def request_signing_bytes(
    *,
    key_id: str,
    method: str,
    path: str,
    query: str = "",
    body: bytes = b"",
    nonce: str,
    signed_at: str,
) -> bytes:
    """What the agent signs for a request. ``path`` is without the server
    prefix (``/agent/decisions``), ``query`` the raw query string without the
    ``?``, ``signed_at`` exactly as the header carries it."""
    fields: dict[str, str] = {
        "keyId": key_id,
        "method": method.upper(),
        "nonce": nonce,
        "path": path,
        "signedAt": signed_at,
    }
    if query:
        fields["query"] = query
    if body:
        fields["bodySha256"] = hashlib.sha256(body).hexdigest()
    return canonical_json(fields)


class AgentKey:
    """The agent's private key."""

    def __init__(self, private: Ed25519PrivateKey):
        self._private = private

    @classmethod
    def parse(cls, secret: str) -> AgentKey:
        """Read the secret the engine showed when the key was issued."""
        # msk_<prefix>_<seed>: the seed is base64url and may itself contain
        # '_', so split on the first two underscores only.
        parts = secret.strip().split("_", 2)
        if len(parts) != 3 or parts[0] != "msk":
            raise ValueError("malformed agent secret: expected msk_<prefix>_<seed>")
        try:
            seed = _b64url_decode(parts[2])
        except ValueError as err:
            raise ValueError(
                "malformed agent secret: the seed is not base64url"
            ) from err
        if len(seed) != SEED_SIZE:
            raise ValueError("malformed agent secret: the seed is not 32 bytes")
        return cls.from_seed(seed)

    @classmethod
    def from_seed(cls, seed: bytes) -> AgentKey:
        return cls(Ed25519PrivateKey.from_private_bytes(seed))

    def sign(self, message: bytes) -> str:
        """The base64 (standard, padded) Ed25519 signature of message."""
        return base64.b64encode(self._private.sign(message)).decode("ascii")

    def sign_request(
        self,
        key_id: str,
        method: str,
        path: str,
        *,
        query: str = "",
        body: bytes = b"",
        nonce: str | None = None,
        signed_at: datetime | None = None,
    ) -> dict[str, str]:
        """The four agentAuth headers for a request; a nonce and the time are
        made here when not given."""
        nonce = nonce or new_nonce()
        signed_at_text = rfc3339(signed_at or datetime.now(UTC))
        message = request_signing_bytes(
            key_id=key_id,
            method=method,
            path=path,
            query=query,
            body=body,
            nonce=nonce,
            signed_at=signed_at_text,
        )
        return {
            HEADER_KEY_ID: key_id,
            HEADER_SIGNED_AT: signed_at_text,
            HEADER_NONCE: nonce,
            HEADER_SIGNATURE: self.sign(message),
        }

    @property
    def public_key(self) -> bytes:
        return self._private.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw)

    @property
    def public_key_b64(self) -> str:
        """The public half as the API stores and returns it."""
        return base64.b64encode(self.public_key).decode("ascii")


def secret(prefix: str, seed: bytes) -> str:
    """Render a seed the way the engine shows it."""
    return prefix + "_" + base64.urlsafe_b64encode(seed).rstrip(b"=").decode("ascii")


def verify(public_key_b64: str, message: bytes, signature_b64: str) -> bool:
    """Check a base64 signature over message with a public key in the API's form."""
    try:
        public = Ed25519PublicKey.from_public_bytes(
            base64.b64decode(public_key_b64, validate=True)
        )
        public.verify(base64.b64decode(signature_b64, validate=True), message)
        return True
    except (ValueError, InvalidSignature):
        return False


def verify_request(
    public_key_b64: str,
    *,
    key_id: str,
    method: str,
    path: str,
    query: str,
    body: bytes,
    nonce: str,
    signed_at: str,
    signature_b64: str,
) -> bool:
    """The engine's side of agentAuth: check the headers against the request.
    Freshness and nonce replay are the caller's checks."""
    message = request_signing_bytes(
        key_id=key_id,
        method=method,
        path=path,
        query=query,
        body=body,
        nonce=nonce,
        signed_at=signed_at,
    )
    return verify(public_key_b64, message, signature_b64)
