package signer

import (
	"context"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
)

func TestRunHook(t *testing.T) {
	req := api.SignRequest{Id: "sr_hook", Purpose: api.SignRequestPurposeOrder, Venue: api.VenueBinance}
	cases := []struct {
		name, command string
		timeout       time.Duration
		want, detail  string
	}{
		{"exit 0 proceeds", "exit 0", 0, "", ""},
		{"reads the request", "grep -q '\"id\":\"sr_hook\"' && exit 0 || exit 1", 0, "", ""},
		{"exit 1 with a reason", "echo 'not during the auction'; echo more; exit 1", 0, RefuseHook, "not during the auction"},
		{"exit 1 on stderr", "echo nope >&2; exit 3", 0, RefuseHook, "nope"},
		{"exit 1 without output", "exit 2", 0, RefuseHook, "exit status 2"},
		{"missing command", "/no/such/command", 0, RefuseHook, ""},
		{"timeout", "sleep 5", 100 * time.Millisecond, RefuseHookTimeout, "the hook did not finish within 100ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			got := RunHook(context.Background(), tc.command, req, tc.timeout)
			switch {
			case tc.want == "" && got != nil:
				t.Fatalf("refused: %s", got.Reason())
			case tc.want != "" && got == nil:
				t.Fatalf("proceeded, want %s", tc.want)
			case tc.want != "" && got.Code != tc.want:
				t.Fatalf("got %s, want %s", got.Reason(), tc.want)
			case tc.want != "" && tc.detail != "" && got.Detail != tc.detail:
				t.Fatalf("detail %q, want %q", got.Detail, tc.detail)
			case tc.want == RefuseHookTimeout && time.Since(start) > 3*time.Second:
				t.Fatalf("the timeout took %s", time.Since(start))
			}
		})
	}
}
