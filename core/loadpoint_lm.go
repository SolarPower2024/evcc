package core

// Custom extension: the loadpoint's load management shed priority.
// See core/lm and core/site_lm.go.

import "github.com/evcc-io/evcc/core/lm"

var _ lm.Load = (*Loadpoint)(nil)

// LmPriority returns the loadpoint's load management shed priority, configured
// as `lmpriority`. Lower is shed first, the default 0 puts every loadpoint on
// the same level, which is upstream's first come, first served behaviour.
//
// This is deliberately not the loadpoint's `priority`: that one distributes pv
// surplus, where the answer to "who goes first" is usually the opposite of what
// it should be when a fuse forces a load to be dropped.
func (lp *Loadpoint) LmPriority() int {
	return lp.LmPrio
}
