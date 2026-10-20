package views

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/traust-security/traust-sdk/go/v1/ledger"
)

// LedgerViews is a read-only view over a ledger service. It never writes:
// no submissions, no countersign — the two-person rule is not a CLI concern.
type LedgerViews struct {
	Client *ledger.Client
	URL    string
}

// NewLedgerViews builds the read-only client. Token resolution is the
// caller's concern (env var), mirroring the SDK's explicit-credential
// posture: the CLI stores env var names, never secrets.
func NewLedgerViews(url, token string) *LedgerViews {
	var opts []ledger.HTTPOption
	if token != "" {
		opts = append(opts, ledger.WithBearerToken(token))
	}
	return &LedgerViews{Client: ledger.NewHTTPClient(url, opts...), URL: url}
}

// Whoami renders the verified actor for the current token.
func (v *LedgerViews) Whoami(ctx context.Context) (string, error) {
	actor, err := v.Client.Whoami(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "actor kind:  %s\n", actor.Kind)
	if actor.DisplayName != nil {
		fmt.Fprintf(&b, "name:        %s\n", *actor.DisplayName)
	}
	if actor.Identity != nil {
		fmt.Fprintf(&b, "identity:    %s\n", *actor.Identity)
	}
	if actor.IdentityProvider != nil {
		fmt.Fprintf(&b, "provider:    %s\n", *actor.IdentityProvider)
	}
	if actor.IdentityVerified != nil {
		fmt.Fprintf(&b, "verified:    %t\n", *actor.IdentityVerified)
	}
	if actor.EmployeeStatus != nil {
		fmt.Fprintf(&b, "employee:    %s\n", *actor.EmployeeStatus)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Findings renders resolved dispositions for one layer (or every layer when
// layerID is empty), paginated.
func (v *LedgerViews) Findings(ctx context.Context, layerID string, limit int) (string, error) {
	var b strings.Builder
	if layerID != "" {
		resp, err := v.Client.GetFindings(ctx, layerID)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "layer %s — %d findings\n", layerID, len(resp.Findings))
		writeDispositions(&b, resp.Findings)
		return b.String(), nil
	}
	if limit <= 0 {
		limit = 100
	}
	resp, err := v.Client.ListFindings(ctx, ledger.ListFindingsOpts{Limit: limit})
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "%d findings across %d layers", resp.TotalFindings, len(resp.Layers))
	if resp.HasMore {
		fmt.Fprintf(&b, " (more available)")
	}
	b.WriteString("\n")
	for _, layer := range resp.Layers {
		fmt.Fprintf(&b, "\nlayer %s — %d findings\n", layer.LayerID, len(layer.Findings))
		writeDispositions(&b, layer.Findings)
	}
	return b.String(), nil
}

func writeDispositions(b *strings.Builder, findings []ledger.FindingDisposition) {
	sorted := make([]ledger.FindingDisposition, len(findings))
	copy(sorted, findings)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].FindingRef < sorted[j].FindingRef })
	for _, f := range sorted {
		validity := "—"
		if f.Disposition.Validity != "" {
			validity = string(f.Disposition.Validity)
		}
		resolution := "—"
		if f.Disposition.Resolution != "" {
			resolution = string(f.Disposition.Resolution)
		}
		fmt.Fprintf(b, "  %-40s validity=%-14s resolution=%-14s events=%d\n", truncate(f.FindingRef, 40), validity, resolution, f.EventCount)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// Layers lists the layers known to the ledger service.
func (v *LedgerViews) Layers(ctx context.Context) (string, error) {
	resp, err := v.Client.ListLayers(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d layers\n", len(resp.Layers))
	for _, l := range resp.Layers {
		line := "  " + l.LayerID
		if l.ProductRepoID != nil {
			line += "  product_repo=" + *l.ProductRepoID
		}
		if len(l.NeedsReview) > 0 {
			line += fmt.Sprintf("  needs_review=%d", len(l.NeedsReview))
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Verify checks one layer's integrity and (optionally) signatures.
func (v *LedgerViews) Verify(ctx context.Context, layerID string, checkSignatures bool) (string, error) {
	resp, err := v.Client.VerifyLayer(ctx, layerID, ledger.VerifyOpts{CheckSignatures: checkSignatures})
	if err != nil {
		return "", err
	}
	verdict := "FAILED"
	if resp.Passed {
		verdict = "passed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "layer %s: %s (checked at %s)\n", layerID, verdict, resp.CheckedAt)
	for _, f := range resp.Findings {
		fmt.Fprintf(&b, "  [%s] %s\n", f.Severity, f.Message)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
