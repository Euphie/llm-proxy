package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRequiresExplicitManifestAndOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "-manifest") {
		t.Fatalf("run error=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
