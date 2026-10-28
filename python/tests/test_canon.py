import pytest

from mishmar import canonical_json


@pytest.mark.parametrize(
    "value, want",
    [
        ({"z": "1", "a": None, "m": {"b": None, "a": "v"}}, '{"m":{"a":"v"},"z":"1"}'),
        ([{"k": None, "j": "x"}], '[{"j":"x"}]'),
        ({"s": "a<b>&c "}, '{"s":"a\\u003cb\\u003e\\u0026c\\u2028"}'),
        (
            {"s": 'tab\tnl\nquote"bs\\nul\x00bf\b\f'},
            '{"s":"tab\\tnl\\nquote\\"bs\\\\nul\\u0000bf\\b\\f"}',
        ),
        ({"s": "ביטקוין €/\x7f"}, '{"s":"ביטקוין €/\x7f"}'),
        (
            {"t": True, "f": False, "e": {}, "l": []},
            '{"e":{},"f":false,"l":[],"t":true}',
        ),
    ],
)
def test_matches_go(value, want):
    assert canonical_json(value) == want.encode("utf-8")


def test_numbers_are_refused():
    with pytest.raises(TypeError):
        canonical_json({"size": 0.5})
    with pytest.raises(TypeError):
        canonical_json({"n": 1})
