"""The agent's identity: an Ed25519 key whose public half the engine stores.

The engine shows the secret once, the agent's API key:
``msk_<environment>_<account suffix>_<key random part>_<seed>``. The seed is
the 32-byte Ed25519 seed in base64url without padding; before it comes what
names the engine and the key, so an agent needs nothing else: the
environment (testnet, prod, … for hosted Mishmar; anything else means the
engine URL is given by hand) and the two parts of the public key id,
``key_<account suffix>_<random part>``. Keys issued before contract 0.19.0
are ``msk_<six characters>_<seed>`` and still parse; they name neither
engine nor key id, which then come separately.

The key signs two things: proposals (see proposal.py) and, under the
contract's agentAuth scheme, every other agent-facing request: four headers
carry the key id, the time, a nonce and an Ed25519 signature over the canonical
JSON of ``{bodySha256, keyId, method, nonce, path, query, signedAt}``. The bytes
are pinned in spec/vectors/agent-request-signing.json.
"""

from __future__ import annotations

import base64
import hashlib
import re
import secrets
from dataclasses import dataclass
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


def _seed_of(text: str) -> bytes | None:
    """The 32-byte seed a secret ends with, or ``None``: strict base64url
    without padding, as the engine writes it."""
    try:
        seed = _b64url_decode(text)
    except ValueError:
        return None
    if (
        len(seed) != SEED_SIZE
        or base64.urlsafe_b64encode(seed).rstrip(b"=").decode("ascii") != text
    ):
        return None
    return seed


# An environment is a short lowercase word (testnet, stage, prod, self); the
# parts of an id are lowercase letters and digits, the random part twelve of them.
_ENVIRONMENT = re.compile(r"[a-z][a-z0-9-]{0,31}")
_ID_PART = re.compile(r"[a-z0-9]+")
_RANDOM_PART_LEN = 12


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
        """Read the key out of the secret the engine showed when it was
        issued, in either format (see :func:`parse_secret`)."""
        return parse_secret(secret).key

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


@dataclass(frozen=True)
class ParsedSecret:
    """An agent's API key taken apart: the key, and what the secret says
    about where it belongs. ``environment``, ``account_id`` and ``key_id``
    are set for a key issued at contract 0.19.0 or later and ``None`` for an
    older one."""

    key: AgentKey
    environment: str | None = None
    account_id: str | None = None
    key_id: str | None = None


def parse_secret(secret: str) -> ParsedSecret:
    """Read the secret the engine showed when the key was issued, in either
    format: ``msk_<environment>_<account>_<key>_<seed>`` (0.19.0 and later),
    which also names the engine and the key id, or ``msk_<prefix>_<seed>``."""
    s = secret.strip()
    if not s.startswith("msk_"):
        raise ValueError("malformed agent secret: it starts with msk_")
    # 0.19.0: five parts, the seed last. The seed is base64url and may itself
    # contain '_', so split for at most five. An older secret cannot pass
    # here: its seed would have to be split across the parts, leaving too few
    # characters for the last one to be a whole seed.
    parts = s.split("_", 4)
    if (
        len(parts) == 5
        and _ENVIRONMENT.fullmatch(parts[1])
        and _ID_PART.fullmatch(parts[2])
        and len(parts[3]) == _RANDOM_PART_LEN
        and _ID_PART.fullmatch(parts[3])
    ):
        seed = _seed_of(parts[4])
        if seed is not None:
            return ParsedSecret(
                AgentKey.from_seed(seed),
                environment=parts[1],
                account_id="acc_" + parts[2],
                key_id=f"key_{parts[2]}_{parts[3]}",
            )
    # Before 0.19.0: msk_<prefix>_<seed>, split on the first two underscores only.
    parts = s.split("_", 2)
    if len(parts) != 3 or not parts[1]:
        raise ValueError(
            "malformed agent secret: expected msk_<environment>_<account>_<key>_<seed>, or msk_<prefix>_<seed>"
        )
    seed = _seed_of(parts[2])
    if seed is None:
        raise ValueError("malformed agent secret: the seed is not 32 base64url bytes")
    return ParsedSecret(AgentKey.from_seed(seed))


def new_secret(environment: str, key_id: str, seed: bytes) -> str:
    """Render the secret of a key issued at contract 0.19.0 or later: the
    environment, the key id's account and random parts, and the seed. For
    tests and tools; the engine issues the real ones."""
    if not _ENVIRONMENT.fullmatch(environment):
        raise ValueError(
            f"environment {environment!r} is not a lowercase word of up to 32 letters, digits and dashes"
        )
    parts = key_id.split("_")
    if (
        len(parts) != 3
        or parts[0] != "key"
        or not _ID_PART.fullmatch(parts[1])
        or len(parts[2]) != _RANDOM_PART_LEN
        or not _ID_PART.fullmatch(parts[2])
    ):
        raise ValueError(
            f"key id {key_id!r} is not key_<account suffix>_<12 characters>"
        )
    if len(seed) != SEED_SIZE:
        raise ValueError("a seed is 32 bytes")
    return secret(f"msk_{environment}_{parts[1]}_{parts[2]}", seed)


def secret(prefix: str, seed: bytes) -> str:
    """Render a seed the way the engine shows it, after ``prefix``."""
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
