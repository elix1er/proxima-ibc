package sequencer_old

import (
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/utangle_old"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const BaseProposerName = "base"

type baseProposer struct {
	proposerTaskGeneric
}

func init() {
	registerProposingStrategy(BaseProposerName, func(mf *milestoneFactory, targetTs ledger.LogicalTime) proposerTask {
		ret := &baseProposer{newProposerGeneric(mf, targetTs, BaseProposerName)}
		return ret
	})
}

func (b *baseProposer) run() {
	startTime := time.Now()
	var tx *transaction.Transaction
	var forceExit bool
	for b.factory.proposal.continueCandidateProposing(b.targetTs) {
		b.startProposingTime()
		latestMs := b.factory.getLatestMilestone()
		b.runAndIgnoreDeletedVerticesException(func() {
			if tx, forceExit = b.proposeBase(latestMs); forceExit {
				b.storeProposalDuration()
				return
			}
			if tx != nil {
				b.trace("generated %s", func() any { return tx.IDShortString() })
				if forceExit = b.assessAndAcceptProposal(tx, latestMs, startTime, b.name()); forceExit {
					return
				}
			}
		})
		if forceExit {
			break
		}
		b.storeProposalDuration()
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *baseProposer) proposeBase(extend utangle_old.WrappedOutput) (*transaction.Transaction, bool) {
	// own latest milestone exists
	if !b.targetTs.IsSlotBoundary() {
		if extend.TimeSlot() != b.targetTs.Slot() {
			// on startup or cross-slot will only produce branches
			//b.setTraceNAhead(1)
			b.trace("proposeBase.force exit: cross-slot %s", extend.IDShort())
			return nil, true
		}
		if !extend.VID.IsSequencerMilestone() {
			// not cross-slot, but predecessor must be sequencer tx
			//b.setTraceNAhead(1)
			b.trace("proposeBase.force exit: not-sequencer %s", extend.IDShort())
			return nil, true
		}
	}

	if !b.targetTs.IsSlotBoundary() && extend.TimeSlot() != b.targetTs.Slot() {
		// on startup or cross-slot will only produce branches
		b.trace("proposeBase.force exit: cross-slot %s", extend.IDShort())
		return nil, true
	}

	if b.targetTs.TimeTick() == 0 {
		b.trace("making branch, extending %s", extend.IDShort())
		// generate branch, no fee outputs are consumed
		baseStem := extend.VID.BaseStemOutput(b.factory.utangle)
		if baseStem == nil {
			// base stem is not available for a milestone which is virtual and non-branch
			b.factory.log.Warnf("proposeBase.force exit: stem not available, %s cannot be extended to a branch", extend.IDShort())
			return nil, true
		}
		// create branch
		return b.makeMilestone(&extend, baseStem, nil, nil), false
	}
	// non-branch

	//b.setTraceNAhead(1)
	b.trace("non-branch")

	feeOutputsToConsume, conflict := b.selectInputs(extend)
	util.Assertf(conflict == nil, "unexpected conflict")

	b.trace("making ordinary milestone")
	return b.makeMilestone(&extend, nil, feeOutputsToConsume, nil), false
}