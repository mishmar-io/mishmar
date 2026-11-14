package daemon

import (
	"strconv"
	"strings"
)

// APIVersion is the local API's version. Every request but GET /health and
// POST /shutdown (the handshake) carries it in HeaderAPI, and a daemon
// refuses another (426 wrong_api_version), so a client never talks to a
// daemon whose answers it would misread.
const APIVersion = 2

// HeaderAPI is the header that carries it.
const HeaderAPI = "Mishmar-Local-API"

// Newer says whether release a is strictly newer than release b, both
// vMAJOR.MINOR.PATCH with an optional -pre-release (lower than the release
// itself). Either one not a release (a development build, "dev") is not
// comparable: false, so a daemon is never replaced on a guess.
func Newer(a, b string) bool {
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if va.n[i] != vb.n[i] {
			return va.n[i] > vb.n[i]
		}
	}
	switch {
	case va.pre == vb.pre:
		return false
	case va.pre == "":
		return true
	case vb.pre == "":
		return false
	}
	return va.pre > vb.pre
}

type version struct {
	n   [3]int
	pre string
}

func parseVersion(s string) (version, bool) {
	s, ok := strings.CutPrefix(strings.TrimSpace(s), "v")
	if !ok {
		return version{}, false
	}
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return version{}, false
		}
		v.n[i] = n
	}
	v.pre = pre
	return v, true
}
