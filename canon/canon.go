// Package canon produces the canonical JSON that agents sign and the engine
// verifies: one object, keys sorted, no whitespace, nulls and absent optional
// fields omitted. An SDK in any language must produce the same bytes from the
// same values, so the rules are pinned here and in spec/vectors:
//
//   - object keys in byte order (sorted as UTF-8), no spaces anywhere;
//   - a key whose value is null is left out, at every depth;
//   - strings as JSON strings with the escapes Go's encoding/json writes:
//     \" \\ \n \r \t, other control characters as \u00XX, and, HTML-safely,
//     < > & as < > & and U+2028 U+2029 as    ;
//     everything else, non-ASCII included, as the UTF-8 itself;
//   - booleans as true/false; numbers never appear in what Mishmar signs
//     (money and quantities travel as decimal strings).
//
// In Go, encoding/json already sorts map keys and writes those escapes, so the
// work is turning the value into a map and dropping the nils.
package canon

import "encoding/json"

// JSON returns the canonical encoding of v (any JSON-serialisable value).
func JSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(strip(generic))
}

func strip(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = strip(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = strip(t[i])
		}
		return t
	default:
		return v
	}
}
