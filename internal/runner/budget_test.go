package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func writePolicy(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "budget-policy.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadPolicyMissing(t *testing.T) {
	p, err := LoadPolicy(t.TempDir())
	if err != nil || p != nil {
		t.Fatalf("missing policy should be (nil, nil), got (%v, %v)", p, err)
	}
}

func TestLoadPolicyWrapperAndDefaults(t *testing.T) {
	dir := writePolicy(t, `
budget_policy:
  enforcement: enforced
  units: output_tokens
  per_run_defaults:
    secure-code-audit: 2500000
    default: 1000000
  bands:
    warn: 1.5
    degrade: 2.0
    refuse: 3.0
`)
	p, err := LoadPolicy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Enforcement != "enforced" {
		t.Errorf("enforcement = %q", p.Enforcement)
	}
	if got := p.CapFor("secure-code-audit"); got != 2_500_000 {
		t.Errorf("cap for secure-code-audit = %d", got)
	}
	if got := p.CapFor("unknown-skill"); got != 1_000_000 {
		t.Errorf("default cap = %d", got)
	}
	if p.Warn != 1.5 || p.Degrade != 2.0 || p.Refuse != 3.0 {
		t.Errorf("bands = %v/%v/%v", p.Warn, p.Degrade, p.Refuse)
	}
}

func TestLoadPolicyBareDefaults(t *testing.T) {
	dir := writePolicy(t, `
enforcement: observe
per_run_defaults:
  default: 500
`)
	p, err := LoadPolicy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CapFor("anything"); got != 500 {
		t.Errorf("cap = %d, want 500", got)
	}
	if p.Warn != defaultWarnBand || p.Degrade != defaultDegradeBand || p.Refuse != defaultRefuseBand {
		t.Errorf("default bands not applied: %v/%v/%v", p.Warn, p.Degrade, p.Refuse)
	}
}

func TestBandFor(t *testing.T) {
	p := &Policy{DefaultCap: 1000, Warn: 1.5, Degrade: 2.0, Refuse: 3.0}
	cases := []struct {
		tokens int64
		want   Band
	}{
		{0, BandWithin},
		{100, BandWithin},
		{1499, BandWithin},
		{1500, BandWarn},
		{1999, BandWarn},
		{2000, BandDegrade},
		{2999, BandDegrade},
		{3000, BandRefuse},
		{99999, BandRefuse},
	}
	for _, c := range cases {
		if got := p.BandFor("skill", c.tokens); got != c.want {
			t.Errorf("BandFor(%d) = %v, want %v", c.tokens, got, c.want)
		}
	}
}
