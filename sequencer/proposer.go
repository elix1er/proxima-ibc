package sequencer

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	utangle "github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/atomic"
)

type (
	proposerTask interface {
		run()
		name() string
		makeMilestone(chainIn, stemIn *utangle.WrappedOutput, feeInputs []utangle.WrappedOutput, endorse []*utangle.WrappedTx) *transaction.Transaction
		trace(format string, args ...any)
		setTraceNAhead(n int64)
	}

	proposerTaskGeneric struct {
		strategyName    string
		factory         *milestoneFactory
		targetTs        core.LogicalTime
		alreadyProposed set.Set[[32]byte]
		traceNAhead     atomic.Int64
		startTime       time.Time
		visited         set.Set[extendEndorsePair]
	}

	extendEndorsePair struct {
		extend  *utangle.WrappedTx
		endorse *utangle.WrappedTx
	}

	proposerTaskConstructor func(mf *milestoneFactory, targetTs core.LogicalTime) proposerTask

	proposerRegistered struct {
		constructor proposerTaskConstructor
		trace       *atomic.Bool
	}
)

var allProposingStrategies = make(map[string]proposerRegistered)

// registerProposingStrategy must always be called from init
func registerProposingStrategy(strategyName string, constructor proposerTaskConstructor) {
	allProposingStrategies[strategyName] = proposerRegistered{
		constructor: constructor,
		trace:       new(atomic.Bool),
	}
}

func SetTraceProposer(name string, v bool) {
	if _, ok := allProposingStrategies[name]; ok {
		allProposingStrategies[name].trace.Store(v)
	}
}

func newProposerGeneric(mf *milestoneFactory, targetTs core.LogicalTime, strategyName string) proposerTaskGeneric {
	return proposerTaskGeneric{
		factory:         mf,
		targetTs:        targetTs,
		strategyName:    strategyName,
		alreadyProposed: set.New[[32]byte](),
		visited:         set.New[extendEndorsePair](),
	}
}

func (c *proposerTaskGeneric) storeVisited(extend, endorse *utangle.WrappedTx) {
	c.visited.Insert(extendEndorsePair{
		extend:  extend,
		endorse: endorse,
	})
}

func (c *proposerTaskGeneric) alreadyVisited(extend, endorse *utangle.WrappedTx) bool {
	return c.visited.Contains(extendEndorsePair{
		extend:  extend,
		endorse: endorse,
	})
}

func (c *proposerTaskGeneric) name() string {
	return fmt.Sprintf("%s-%s", c.strategyName, c.targetTs.String())
}

func (c *proposerTaskGeneric) setTraceNAhead(n int64) {
	c.traceNAhead.Store(n)
}

func (c *proposerTaskGeneric) traceEnabled() bool {
	reg, registered := allProposingStrategies[c.strategyName]
	if !registered {
		return false
	}
	if c.traceNAhead.Dec() >= 0 {
		return true
	}
	return reg.trace.Load()
}

func (c *proposerTaskGeneric) trace(format string, args ...any) {
	if c.traceEnabled() {
		pref := fmt.Sprintf("TRACE(%s) -- ", c.name())
		c.factory.log.Infof(pref+format, util.EvalLazyArgs(args...)...)
	}
}

func (c *proposerTaskGeneric) forceTrace(format string, args ...any) {
	c.setTraceNAhead(1)
	c.trace(format, args...)
}

func (c *proposerTaskGeneric) startProposingTime() {
	c.startTime = time.Now()
}

func (c *proposerTaskGeneric) selectInputs(ownMs utangle.WrappedOutput, seqVIDs ...*utangle.WrappedTx) ([]utangle.WrappedOutput, *utangle.WrappedOutput) {
	return c.factory.selectInputs(c.targetTs, ownMs, seqVIDs...)
}

func (c *proposerTaskGeneric) makeMilestone(chainIn, stemIn *utangle.WrappedOutput, feeInputs []utangle.WrappedOutput, endorse []*utangle.WrappedTx) *transaction.Transaction {
	util.Assertf(chainIn != nil, "chainIn != nil")
	util.Assertf(c.targetTs.TimeTick() != 0 || len(endorse) == 0, "proposer task %s: targetTs.TimeTick() != 0 || len(endorse) == 0", c.name())
	util.Assertf(len(feeInputs) <= c.factory.maxFeeInputs, "proposer task %s: len(feeInputs) <= mf.maxFeeInputs", c.name())

	ret, err := c.factory.makeMilestone(chainIn, stemIn, feeInputs, endorse, c.targetTs)
	util.Assertf(err == nil, "error in %s: %v", c.name(), err)
	if ret == nil {
		c.trace("makeMilestone: nil")
	} else {
		c.trace("makeMilestone: %s", ret.ID().Short())
	}
	return ret
}

// assessAndAcceptProposal returns reject reason of empty string, if accepted
func (c *proposerTaskGeneric) assessAndAcceptProposal(tx *transaction.Transaction, extend utangle.WrappedOutput, startTime time.Time, taskName string) bool {
	c.trace("inside assessAndAcceptProposal: %s", tx.IDShort())

	// prevent repeating transactions with same consumedInThePastPath
	hashOfProposal := tx.HashInputsAndEndorsements()
	if c.alreadyProposed.Contains(hashOfProposal) {
		c.trace("repeating proposal in '%s', wait 10ms %s", c.name(), tx.IDShort())
		time.Sleep(10 * time.Millisecond)
		return false
	}
	c.alreadyProposed.Insert(hashOfProposal)

	coverage, err := c.factory.utangle.LedgerCoverageFromTransaction(tx)
	if err != nil {
		c.factory.log.Warnf("assessAndAcceptProposal::LedgerCoverageFromTransaction (%s, %s): %v", tx.Timestamp(), taskName, err)
	}

	//c.setTraceNAhead(1)
	//c.trace("LedgerCoverageFromTransaction %s = %d", tx.IDShort(), coverage)

	msData := &proposedMilestoneWithData{
		tx:         tx,
		extended:   extend,
		coverage:   coverage,
		elapsed:    time.Since(startTime),
		proposedBy: taskName,
	}
	rejectReason, forceExit := c.placeProposalIfRelevant(msData)
	if rejectReason != "" {
		//c.setTraceNAhead(1)
		c.trace(rejectReason)

	}
	return forceExit
}

func (c *proposerTaskGeneric) storeProposalDuration() {
	c.factory.storeProposalDuration(time.Since(c.startTime))
}

func (c *proposerTaskGeneric) placeProposalIfRelevant(mdProposed *proposedMilestoneWithData) (string, bool) {
	c.factory.proposal.mutex.Lock()
	defer c.factory.proposal.mutex.Unlock()

	//c.setTraceNAhead(1)
	c.trace("proposed %s: coverage: %s (base %s), numIN: %d, elapsed: %v",
		mdProposed.proposedBy, util.GoThousands(mdProposed.coverage), util.GoThousands(c.factory.proposal.bestSoFarCoverage),
		mdProposed.tx.NumInputs(), mdProposed.elapsed)

	if c.factory.proposal.targetTs == core.NilLogicalTime {
		return fmt.Sprintf("%s SKIPPED: target is nil", mdProposed.tx.IDShort()), false
	}

	// decide if it is not lagging behind the target
	if mdProposed.tx.Timestamp() != c.factory.proposal.targetTs {
		c.factory.log.Warnf("%s: proposed milestone timestamp %s is behind current target %s. Generation duration: %v",
			mdProposed.proposedBy, mdProposed.tx.Timestamp().String(), c.factory.proposal.targetTs.String(), mdProposed.elapsed)
		return fmt.Sprintf("%s SKIPPED: task is behind target", mdProposed.tx.IDShort()), true
	}

	if c.factory.proposal.current != nil && *c.factory.proposal.current.ID() == *mdProposed.tx.ID() {
		return fmt.Sprintf("%s SKIPPED: repeating", mdProposed.tx.IDShort()), false
	}

	baselineCoverage := c.factory.proposal.bestSoFarCoverage

	if !mdProposed.tx.IsBranchTransaction() {
		if mdProposed.coverage <= baselineCoverage {
			return fmt.Sprintf("%s SKIPPED: no increase in coverage %s <- %s)",
				mdProposed.tx.IDShort(), util.GoThousands(mdProposed.coverage), util.GoThousands(c.factory.proposal.bestSoFarCoverage)), false
		}
	}

	// branch proposals always accepted
	c.factory.proposal.bestSoFarCoverage = mdProposed.coverage
	c.factory.proposal.current = mdProposed.tx
	c.factory.proposal.currentExtended = mdProposed.extended

	//c.setTraceNAhead(1)
	c.trace("(%s): ACCEPTED %s, coverage: %s (base: %s), elapsed: %v, inputs: %d, tipPool: %d",
		mdProposed.proposedBy,
		mdProposed.tx.IDShort(),
		util.GoThousands(mdProposed.coverage),
		util.GoThousands(baselineCoverage),
		mdProposed.elapsed,
		mdProposed.tx.NumInputs(),
		c.factory.tipPool.numOutputsInBuffer(),
	)
	return "", false
}

// extensionChoicesInEndorsementTargetPastCone sorted by coverage descending
// excludes those pairs which are marked already visited
func (c *proposerTaskGeneric) extensionChoicesInEndorsementTargetPastCone(endorsementTarget *utangle.WrappedTx) []utangle.WrappedOutput {
	stateRdr := c.factory.utangle.MustGetBaselineState(endorsementTarget)
	rdr := multistate.MakeSugared(stateRdr)

	anotherSeqID := endorsementTarget.MustSequencerID()
	rootOutput, err := rdr.GetChainOutput(&c.factory.tipPool.chainID)
	if errors.Is(err, multistate.ErrNotFound) {
		// cannot find own seqID in the state of anotherSeqID. The tree is empty
		c.trace("cannot find own seqID %s in the state of another seq %s (%s). The tree is empty",
			c.factory.tipPool.chainID.VeryShort(), endorsementTarget.IDShort(), anotherSeqID.VeryShort())
		return nil
	}
	util.AssertNoError(err)
	c.trace("found own seqID %s in the state of another seq %s (%s)",
		c.factory.tipPool.chainID.VeryShort(), endorsementTarget.IDShort(), anotherSeqID.VeryShort())

	rootWrapped, ok, _ := c.factory.utangle.GetWrappedOutput(&rootOutput.ID, rdr)
	if !ok {
		c.trace("cannot fetch wrapped root output %s", rootOutput.IDShort())
		return nil
	}
	c.factory.addOwnMilestone(rootWrapped) // to ensure it is among own milestones

	cone := c.futureConeMilestonesOrdered(rootWrapped.VID)

	return util.FilterSlice(cone, func(extensionChoice utangle.WrappedOutput) bool {
		return !c.alreadyVisited(extensionChoice.VID, endorsementTarget)
	})
}

func (c *proposerTaskGeneric) futureConeMilestonesOrdered(rootVID *utangle.WrappedTx) []utangle.WrappedOutput {
	c.factory.cleanOwnMilestonesIfNecessary()

	c.factory.mutex.RLock()
	defer c.factory.mutex.RUnlock()

	//p.setTraceNAhead(1)
	c.trace("futureConeMilestonesOrdered for root %s. Total %d own milestones", rootVID.LazyIDShort(), len(c.factory.ownMilestones))

	om, ok := c.factory.ownMilestones[rootVID]
	util.Assertf(ok, "futureConeMilestonesOrdered: milestone %s of chain %s is expected to be among set of own milestones (%d)",
		rootVID.LazyIDShort(),
		func() any { return c.factory.tipPool.chainID.Short() },
		len(c.factory.ownMilestones))

	rootOut := om.WrappedOutput
	ordered := util.SortKeys(c.factory.ownMilestones, func(vid1, vid2 *utangle.WrappedTx) bool {
		// by timestamp -> equivalent to topological order, ascending, i.e. older first
		return vid1.Timestamp().Before(vid2.Timestamp())
	})

	visited := set.New[*utangle.WrappedTx](rootVID)
	ret := append(make([]utangle.WrappedOutput, 0, len(ordered)), rootOut)
	for _, vid := range ordered {
		if !vid.IsDeleted() &&
			vid.IsSequencerMilestone() &&
			visited.Contains(vid.SequencerPredecessor()) &&
			core.ValidTimePace(vid.Timestamp(), c.targetTs) {
			visited.Insert(vid)
			ret = append(ret, c.factory.ownMilestones[vid].WrappedOutput)
		}
	}
	return ret
}

// betterMilestone returns if vid1 is strongly better than vid2
func isPreferredMilestoneAgainstTheOther(ut *utangle.UTXOTangle, vid1, vid2 *utangle.WrappedTx) bool {
	util.Assertf(vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone(), "vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone()")

	if vid1 == vid2 {
		return false
	}
	if vid2 == nil {
		return true
	}

	coverage1 := ut.LedgerCoverage(vid1)
	coverage2 := ut.LedgerCoverage(vid2)
	switch {
	case coverage1 > coverage2:
		// main preference is by ledger coverage
		return true
	case coverage1 == coverage2:
		// in case of equal coverage hash will be used
		return bytes.Compare(vid1.ID()[:], vid2.ID()[:]) > 0
	default:
		return false
	}
}

func milestoneSliceString(path []utangle.WrappedOutput) string {
	ret := make([]string, 0)
	for _, md := range path {
		ret = append(ret, "       "+md.IDShort())
	}
	return strings.Join(ret, "\n")
}
