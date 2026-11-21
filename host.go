package pywasi

import (
	"context"
	"sync/atomic"

	"github.com/ChrisRx/wazero"

	"go.chrisrx.dev/pywasi/wasisock"
	"go.chrisrx.dev/pywasi/wasiterm"
)

// network is the WithNetwork policy.
type network struct {
	enabled bool
	allow   func(op, address string) error
}

// disabledKey marks a run's context with a *atomic.Bool, set when Python
// tried to use the network while WithNetwork is off. Eval and Call then add
// a hint to the PermissionError.
type disabledKey struct{}

func noteDisabled(ctx context.Context) {
	if b, ok := ctx.Value(disabledKey{}).(*atomic.Bool); ok {
		b.Store(true)
	}
}

// instantiateHost adds the "pywasi" host module to r: sockets (package
// wasisock) and terminals (package wasiterm).
func instantiateHost(ctx context.Context, r wazero.Runtime, n network) error {
	b := r.NewHostModuleBuilder("pywasi")
	wasisock.Export(b, wasisock.Policy{Enabled: n.enabled, Allow: n.allow, Disabled: noteDisabled})
	wasiterm.Export(b)
	_, err := b.Instantiate(ctx)
	return err
}
