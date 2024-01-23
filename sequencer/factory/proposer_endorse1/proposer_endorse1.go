package proposer_endorse1

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const (
	Endorse1ProposerName = "endorse1"
	TraceTag             = "propose-endorse1"
)

type Endorse1Proposer struct {
	proposer_generic.TaskGeneric
}

func Strategy() *proposer_generic.Strategy {
	return &proposer_generic.Strategy{
		Name: Endorse1ProposerName,
		Constructor: func(generic *proposer_generic.TaskGeneric) proposer_generic.Task {
			if generic.TargetTs.Tick() == 0 {
				// endorse strategy ia not applicable for genereting branches
				return nil
			}
			ret := &Endorse1Proposer{TaskGeneric: *generic}
			ret.WithProposalGenerator(func() (*attacher.IncrementalAttacher, bool) {
				return ret.propose(), false
			})
			return ret
		},
	}
}

func (b *Endorse1Proposer) propose() *attacher.IncrementalAttacher {
	a := b.ChooseExtendEndorsePair(b.Name, b.TargetTs)
	if a == nil {
		b.Tracef(TraceTag, "propose: ChooseExtendEndorsePair returned nil")
		return nil
	}
	if !a.Completed() {
		endorsing := a.Endorsing()[0]
		extending := a.Extending()
		b.Tracef(TraceTag, "proposal [extend=%s, endorsing=%s] not complete", extending.IDShortString, endorsing.IDShortString)
		return nil
	}
	b.AttachTagAlongInputs(a)
	util.Assertf(a.Completed(), "incremental attacher %s is not complete", a.Name())
	return a
}
