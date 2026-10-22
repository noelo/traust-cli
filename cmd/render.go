package cmd

import (
	"fmt"
	"io"

	"github.com/traust-security/traust-cli/internal/harness"
)

// keyedRenderer prints harness events, collapsing keyed streaming updates
// to their final content: a part that rewrites itself as it grows (crush
// streams text, tool inputs and results incrementally) prints once, with
// its last content, instead of once per fragment. Used by the plain sink,
// plain attach, logs and peek.
type keyedRenderer struct {
	w       io.Writer
	curKey  string
	pending string
	has     bool
}

func newKeyedRenderer(w io.Writer) *keyedRenderer {
	return &keyedRenderer{w: w}
}

// Event renders one event, buffering keyed updates until the key changes.
func (k *keyedRenderer) Event(ev harness.Event) {
	if ev.Key != "" {
		if ev.Key != k.curKey {
			k.Flush()
			k.curKey = ev.Key
		}
		k.pending = renderEventString(ev)
		k.has = true
		return
	}
	k.Flush()
	fmt.Fprint(k.w, renderEventString(ev))
}

// Flush prints the buffered keyed line, if any.
func (k *keyedRenderer) Flush() {
	if k.has {
		fmt.Fprint(k.w, k.pending)
		k.pending = ""
		k.has = false
	}
}
