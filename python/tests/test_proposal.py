from datetime import UTC, datetime, timedelta, timezone

import pytest

from mishmar import AgentKey, Proposal, new_nonce, rfc3339

KEY = AgentKey.from_seed(bytes(range(32)))


def good():
    return Proposal(
        trading_account_id="rt_1",
        symbol="BTC",
        side="buy",
        size="0.5",
        price="98000.5",
        key_id="key_a",
    )


def test_sign_fills_times_and_verifies():
    p = good().sign(KEY)
    assert p.proposed_at is not None and p.expires_at == p.proposed_at + timedelta(
        minutes=1
    )
    body = p.to_wire()
    assert body["signature"] == p.signature
    assert (
        body["proposedAt"].endswith("Z") and "price" in body and "leverage" not in body
    )


def test_rfc3339():
    t = datetime(2026, 10, 18, 11, 0, 0, 250000, tzinfo=timezone(timedelta(hours=1)))
    assert rfc3339(t) == "2026-10-18T10:00:00.25Z"
    assert rfc3339(t.replace(microsecond=0)) == "2026-10-18T10:00:00Z"
    assert rfc3339(t.replace(microsecond=1)) == "2026-10-18T10:00:00.000001Z"
    with pytest.raises(ValueError):
        rfc3339(datetime(2026, 1, 1))


@pytest.mark.parametrize(
    "change, message",
    [
        ({"key_id": ""}, "key_id"),
        ({"size": "1e3"}, "size"),
        ({"size": "+1"}, "size"),
        ({"price": None}, "limit order"),
        ({"order_type": "market"}, "market order"),
        ({"side": "long"}, "side"),
        ({"leverage": "2x"}, "leverage"),
        ({"expires_at": datetime(2020, 1, 1, tzinfo=UTC)}, "expires_at"),
    ],
)
def test_validate_names_the_field(change, message):
    p = good()
    for k, v in change.items():
        setattr(p, k, v)
    with pytest.raises(ValueError, match=message):
        p.validate()


def test_floats_are_refused_at_signing():
    p = good()
    p.size = 0.5  # type: ignore[assignment]
    with pytest.raises(ValueError, match="size"):
        p.sign(KEY)


def test_nonce():
    assert len(new_nonce()) == 32 and new_nonce() != new_nonce()


def cancel():
    return Proposal(
        trading_account_id="rt_1",
        symbol="BTCUSDT",
        kind="cancel",
        target_client_order_id="msh-a1b2c3-k3j9xq",
        key_id="key_a",
    )


def test_cancel_carries_no_order_fields():
    body = cancel().sign(KEY).to_wire()
    assert body["targetClientOrderId"] == "msh-a1b2c3-k3j9xq"
    for k in ("side", "size", "price", "orderType", "reduceOnly", "leverage"):
        assert k not in body


@pytest.mark.parametrize(
    "change, message",
    [
        ({"target_client_order_id": None}, "target_client_order_id"),
        ({"side": "buy"}, "no order fields"),
        ({"size": "1"}, "no order fields"),
        ({"reduce_only": True}, "no order fields"),
    ],
)
def test_cancel_validation(change, message):
    p = cancel()
    for k, v in change.items():
        setattr(p, k, v)
    with pytest.raises(ValueError, match=message):
        p.validate()


def test_order_with_a_target_is_refused():
    p = good()
    p.target_client_order_id = "msh-x"
    with pytest.raises(ValueError, match="cancels only"):
        p.validate()
