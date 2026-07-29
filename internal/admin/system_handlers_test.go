package admin

import "testing"

func TestResolveSystemVersionUsesInjectionAndDevelopmentFallback(t *testing.T) {
	if got := resolveSystemVersion("v1.2.3"); got != "v1.2.3" {
		t.Fatalf("injected version=%q", got)
	}
	if got := resolveSystemVersion(""); got != "development" {
		t.Fatalf("development version=%q", got)
	}
}
