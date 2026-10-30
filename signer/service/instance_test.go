package service

import (
	"context"
	"testing"

	"github.com/mishmar-io/mishmar/signerapi"
)

func TestEveryAnswerNamesTheInstance(t *testing.T) {
	h := newHarness(t)
	r, err := h.client.HealthWithResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := r.HTTPResponse.Header.Get(signerapi.InstanceHeader); got == "" || got != h.svc.Instance() {
		t.Fatalf("instance header %q, want %q", got, h.svc.Instance())
	}
	if r.JSON200 == nil || r.JSON200.Instance != h.svc.Instance() {
		t.Fatalf("health instance %+v, want %q", r.JSON200, h.svc.Instance())
	}
	// A refused request carries it too.
	bad, err := h.client.GetKeyWithResponse(context.Background(), "vk_none")
	if err != nil {
		t.Fatal(err)
	}
	if bad.StatusCode() != 404 || bad.HTTPResponse.Header.Get(signerapi.InstanceHeader) != h.svc.Instance() {
		t.Fatalf("404 answer: status %d, header %q", bad.StatusCode(), bad.HTTPResponse.Header.Get(signerapi.InstanceHeader))
	}
}
