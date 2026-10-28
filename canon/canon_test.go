package canon

import "testing"

func TestJSON(t *testing.T) {
	type inner struct {
		B *string `json:"b"`
		A string  `json:"a"`
	}
	s := "x"
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"keys sorted, nil dropped, nested", map[string]any{"z": 1, "a": nil, "m": inner{A: "v"}}, `{"m":{"a":"v"},"z":1}`},
		{"nil dropped inside arrays too", []any{map[string]any{"k": nil, "j": &s}}, `[{"j":"x"}]`},
		{"html-safe escapes as Go writes them", map[string]any{"s": "a<b>&c\u2028"}, `{"s":"a\u003cb\u003e\u0026c\u2028"}`},
		{"control characters", map[string]any{"s": "tab\tnl\nquote\"bs\\nul\x00"}, `{"s":"tab\tnl\nquote\"bs\\nul\u0000"}`},
		{"non-ascii stays utf-8", map[string]any{"s": "ביטקוין €"}, `{"s":"ביטקוין €"}`},
		{"booleans and empty containers", map[string]any{"t": true, "f": false, "e": map[string]any{}, "l": []any{}}, `{"e":{},"f":false,"l":[],"t":true}`},
	}
	for _, c := range cases {
		got, err := JSON(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}
