package sequencer_old

import (
	"crypto/ed25519"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/utangle_old"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	milestoneFactory struct {
		mutex                       sync.RWMutex
		seqName                     string
		log                         *zap.SugaredLogger
		utangle                     *utangle_old.UTXOTangle
		tipPool                     *sequencerTipPool
		controllerKey               ed25519.PrivateKey
		proposal                    latestMilestoneProposal
		ownMilestones               map[*utangle_old.WrappedTx]ownMilestone
		maxFeeInputs                int
		lastPruned                  time.Time
		ownMilestoneCount           int
		removedMilestonesSinceReset int
	}

	ownMilestone struct {
		utangle_old.WrappedOutput
		consumedInThePastPath set.Set[utangle_old.WrappedOutput]
	}

	proposedMilestoneWithData struct {
		tx         *transaction.Transaction
		extended   utangle_old.WrappedOutput
		coverage   uint64
		elapsed    time.Duration
		proposedBy string
	}

	latestMilestoneProposal struct {
		mutex             sync.RWMutex
		targetTs          ledger.LogicalTime
		bestSoFarCoverage uint64
		current           *transaction.Transaction
		currentExtended   utangle_old.WrappedOutput
		durations         []time.Duration
	}

	factoryStats struct {
		numOwnMilestones            int
		ownMilestoneCount           int
		removedMilestonesSinceReset int
		tipPoolStats
	}
)

const (
	maxAdditionalOutputs = 256 - 2              // 1 for chain output, 1 for stem
	veryMaxFeeInputs     = maxAdditionalOutputs // edge case with sequencer commands
)

func (seq *Sequencer) createMilestoneFactory() error {
	logname := fmt.Sprintf("[%sF-%s]", seq.config.SequencerName, seq.chainID.StringVeryShort())
	log := global.NewLogger(logname, seq.config.LogLevel, seq.config.LogOutputs, seq.config.LogTimeLayout)

	chainOut := seq.config.StartOutput
	if chainOut.VID == nil {
		rdr := seq.glb.UTXOTangle().HeaviestStateForLatestTimeSlot()
		odata, err := rdr.GetUTXOForChainID(&seq.chainID)
		if err != nil {
			return fmt.Errorf("can't get chain output: %v", err)
		}
		var hasIt, invalid bool
		chainOut, hasIt, invalid = seq.glb.UTXOTangle().GetWrappedOutput(&odata.ID, rdr)
		util.Assertf(hasIt && !invalid, "can't retrieve chain output")
	}
	var err error

	ownMilestones := map[*utangle_old.WrappedTx]ownMilestone{
		chainOut.VID: newOwnMilestone(chainOut),
	}

	tippoolLoglevel := seq.config.LogLevel
	if seq.config.TraceTippool {
		tippoolLoglevel = zapcore.DebugLevel
	}
	tippool, err := startTipPool(seq.config.SequencerName, seq.glb, seq.chainID, tippoolLoglevel)
	if err != nil {
		return err
	}

	ret := &milestoneFactory{
		seqName:       seq.config.SequencerName,
		log:           log,
		utangle:       seq.glb.UTXOTangle(),
		tipPool:       tippool,
		ownMilestones: ownMilestones,
		controllerKey: seq.controllerKey,
		maxFeeInputs:  seq.config.MaxFeeInputs,
	}
	if ret.maxFeeInputs == 0 || ret.maxFeeInputs > veryMaxFeeInputs {
		ret.maxFeeInputs = veryMaxFeeInputs
	}
	ret.log.Debugf("milestone factory started")

	seq.factory = ret
	return nil
}

func (mf *milestoneFactory) trace(format string, args ...any) {
	if traceAll.Load() {
		mf.log.Infof("TRACE "+format, args...)
	}
}

func newOwnMilestone(wOut utangle_old.WrappedOutput, inputs ...utangle_old.WrappedOutput) ownMilestone {
	return ownMilestone{
		WrappedOutput:         wOut,
		consumedInThePastPath: set.New[utangle_old.WrappedOutput](inputs...),
	}
}

func (mf *milestoneFactory) makeMilestone(chainIn, stemIn *utangle_old.WrappedOutput, preSelectedFeeInputs []utangle_old.WrappedOutput, endorse []*utangle_old.WrappedTx, targetTs ledger.LogicalTime) (*transaction.Transaction, error) {
	chainInReal, err := chainIn.Unwrap()
	if err != nil || chainInReal == nil {
		return nil, err
	}
	var stemInReal *ledger.OutputWithID

	if stemIn != nil {
		stemInReal, err = stemIn.Unwrap()
		if err != nil || stemInReal == nil {
			return nil, err
		}
	}
	feeInputsReal := make([]*ledger.OutputWithID, len(preSelectedFeeInputs))
	for i, wOut := range preSelectedFeeInputs {
		feeInputsReal[i], err = wOut.Unwrap()
		if err != nil {
			return nil, err
		}
		if feeInputsReal[i] == nil {
			return nil, nil
		}
	}
	// interpret sequencer commands contained in fee consumedInThePastPath. This also possibly adjusts consumedInThePastPath
	var additionalOutputs []*ledger.Output
	capWithdrawals := uint64(0)
	if chainInReal.Output.Amount() > ledger.MinimumAmountOnSequencer {
		capWithdrawals = chainInReal.Output.Amount() - ledger.MinimumAmountOnSequencer
	}

	// calculate inflation
	var inflationAmount uint64
	if stemIn != nil {
		inflationAmount = ledger.MaxInflationFromPredecessorAmount(chainInReal.Output.Amount())
	}

	// interpret possible sequencer commands in inputs
	feeInputsReal, additionalOutputs = mf.makeAdditionalInputsOutputs(feeInputsReal, capWithdrawals)
	endorseReal := utangle_old.DecodeIDs(endorse...)

	if err != nil {
		return nil, err
	}
	txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
		SeqName: mf.seqName,
		ChainInput: &ledger.OutputWithChainID{
			OutputWithID: *chainInReal,
			ChainID:      mf.tipPool.ChainID(),
		},
		StemInput:         stemInReal,
		Inflation:         inflationAmount,
		Timestamp:         targetTs,
		AdditionalInputs:  feeInputsReal,
		AdditionalOutputs: additionalOutputs,
		Endorsements:      endorseReal,
		PrivateKey:        mf.controllerKey,
	})
	if err != nil {
		return nil, err
	}
	return transaction.FromBytesMainChecksWithOpt(txBytes)
}

func (mf *milestoneFactory) addOwnMilestone(wOut utangle_old.WrappedOutput) {
	inputs := wOut.VID.WrappedInputs()
	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	om := newOwnMilestone(wOut, inputs...)
	if wOut.VID.IsSequencerMilestone() {
		if prev := wOut.VID.SequencerPredecessor(); prev != nil {
			if prevOm, found := mf.ownMilestones[prev]; found {
				om.consumedInThePastPath.AddAll(prevOm.consumedInThePastPath)
			}
		}
	}
	if _, found := mf.ownMilestones[wOut.VID]; !found {
		mf.ownMilestones[wOut.VID] = om
		mf.ownMilestoneCount++
	}
}

func (mf *milestoneFactory) isConsumedInThePastPath(wOut utangle_old.WrappedOutput, ms *utangle_old.WrappedTx) bool {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	return mf.ownMilestones[ms].consumedInThePastPath.Contains(wOut)
}

func (mf *milestoneFactory) selectInputs(targetTs ledger.LogicalTime, ownMs utangle_old.WrappedOutput, otherSeqVIDs ...*utangle_old.WrappedTx) ([]utangle_old.WrappedOutput, *utangle_old.WrappedOutput) {
	if ownMs.IsConsumed(otherSeqVIDs...) {
		return nil, &ownMs
	}

	allSeqVIDs := append(slices.Clone(otherSeqVIDs), ownMs.VID)

	consolidatedPastTrack, conflict := utangle_old.MergePastTracks(mf.utangle.StateStore, allSeqVIDs...)
	if conflict != nil {
		return nil, conflict
	}

	// pre-selects not orphaned and with suitable timestamp outputs, sorts by timestamp ascending
	selected := mf.tipPool.filterAndSortOutputs(func(wOut utangle_old.WrappedOutput) bool {
		if !ledger.ValidTimePace(wOut.Timestamp(), targetTs) {
			return false
		}
		if mf.isConsumedInThePastPath(wOut, ownMs.VID) {
			// fast filtering out already consumed outputs
			return false
		}
		return true
	})

	// filters outputs which can be merged into the target delta but no more than maxFeeInputs limit
	selected = util.FilterSlice(selected, func(wOut utangle_old.WrappedOutput) bool {
		conflict = consolidatedPastTrack.AbsorbPastTrackSafe(wOut.VID, mf.utangle.StateStore)
		return conflict == nil && !wOut.IsConsumed(otherSeqVIDs...)
	}, mf.maxFeeInputs)

	return selected, nil
}

func (mf *milestoneFactory) getLatestMilestone() (ret utangle_old.WrappedOutput) {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	for _, ms := range mf.ownMilestones {
		if ret.VID == nil || ms.Timestamp().After(ret.Timestamp()) {
			ret = ms.WrappedOutput
		}
	}
	util.Assertf(ret.VID != nil, "ret.VID != nil")
	return ret
}

// setNewTarget signals proposer allMilestoneProposingStrategies about new timestamp,
// Returns last proposed proposal
func (mf *milestoneFactory) setNewTarget(ts ledger.LogicalTime) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	mf.proposal.targetTs = ts
	mf.proposal.current = nil
	if ts.IsSlotBoundary() {
		mf.proposal.bestSoFarCoverage = 0
	}
	mf.proposal.durations = make([]time.Duration, 0)
}

func (mf *milestoneFactory) storeProposalDuration(d time.Duration) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	mf.proposal.durations = append(mf.proposal.durations, d)
}

func (mf *milestoneFactory) averageProposalDuration() (time.Duration, int) {
	mf.proposal.mutex.RLock()
	defer mf.proposal.mutex.RUnlock()

	if len(mf.proposal.durations) == 0 {
		return 0, 0
	}
	sum := int64(0)
	for _, d := range mf.proposal.durations {
		sum += int64(d)
	}
	return time.Duration(sum / int64(len(mf.proposal.durations))), len(mf.proposal.durations)
}

// continueCandidateProposing the proposing strategy checks if its assumed target timestamp
// is still actual. Strategy keeps proposing latestMilestone candidates until it is no longer actual
func (mc *latestMilestoneProposal) continueCandidateProposing(ts ledger.LogicalTime) bool {
	mc.mutex.RLock()
	defer mc.mutex.RUnlock()

	return mc.targetTs == ts
}

func (mc *latestMilestoneProposal) getLatestProposal() *transaction.Transaction {
	mc.mutex.RLock()
	defer mc.mutex.RUnlock()

	return mc.current
}

func (mf *milestoneFactory) startProposingForTargetLogicalTime(targetTs ledger.LogicalTime) (*transaction.Transaction, time.Duration, int) {
	//mf.log.Infof("startProposingForTargetLogicalTime: %s", targetTs.String())
	deadline := targetTs.Time()
	nowis := time.Now()

	if deadline.Before(nowis) {
		//mf.log.Warnf("startProposingForTargetLogicalTime: nowis: %v, deadline: %v", nowis, deadline)
		return nil, 0, 0
	}
	// start worker(s)
	mf.setNewTarget(targetTs)
	mf.startProposerWorkers(targetTs)
	// wait util real time deadline
	time.Sleep(deadline.Sub(nowis))

	ret := mf.proposal.getLatestProposal() // will return nil if wasn't able to generate transaction
	// set target time to nil -> signal workers to exit
	avgProposalDuration, numProposals := mf.averageProposalDuration()
	mf.setNewTarget(ledger.NilLogicalTime)
	return ret, avgProposalDuration, numProposals
}

func (mf *milestoneFactory) startProposerWorkers(targetTime ledger.LogicalTime) {
	for strategyName, rec := range allProposingStrategies {
		task := rec.constructor(mf, targetTime)
		if task != nil {
			task.trace("RUN '%s' proposer for the target %s", strategyName, targetTime.String())
			util.RunWrappedRoutine(mf.seqName+"::"+task.name(), func() {
				mf.runProposerTask(task)
			}, func(err error) {
				mf.log.Fatal(err)
			},
				common.ErrDBUnavailable)
		} else {
			mf.trace("SKIP '%s' proposer for the target %s", strategyName, targetTime.String())
		}
	}
}

func (mf *milestoneFactory) runProposerTask(task proposerTask) {
	//task.setTraceNAhead(1)
	task.trace(" START proposer %s", task.name())
	task.run()
	//task.setTraceNAhead(1)
	task.trace(" END proposer %s", task.name())
}

const cleanupMilestonesPeriod = 1 * time.Second

func (mf *milestoneFactory) cleanOwnMilestonesIfNecessary() {
	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	if time.Since(mf.lastPruned) < cleanupMilestonesPeriod {
		return
	}

	toDelete := make([]*utangle_old.WrappedTx, 0)
	for vid := range mf.ownMilestones {
		vid.Unwrap(utangle_old.UnwrapOptions{Deleted: func() {
			toDelete = append(toDelete, vid)
		}})
	}
	for _, vid := range toDelete {
		delete(mf.ownMilestones, vid)
	}
	mf.removedMilestonesSinceReset += len(toDelete)
}

// makeAdditionalInputsOutputs makes additional outputs according to commands in inputs.
// Filters consumedInThePastPath so that transfer commands would not exceed maximumTotal
func (mf *milestoneFactory) makeAdditionalInputsOutputs(inputs []*ledger.OutputWithID, maximumTotal uint64) ([]*ledger.OutputWithID, []*ledger.Output) {
	retImp := make([]*ledger.OutputWithID, 0)
	retOut := make([]*ledger.Output, 0)

	myAddr := ledger.AddressED25519FromPrivateKey(mf.controllerKey)
	total := uint64(0)
	for _, inp := range inputs {
		if cmdData := parseSenderCommandDataRaw(myAddr, inp); len(cmdData) > 0 {
			o, err := makeOutputFromCommandData(cmdData)
			if err != nil {
				mf.log.Warnf("error while parsing sequencer command in input %s: %v", inp.IDShort(), err)
				continue
			}
			if o.Amount() <= maximumTotal-total {
				retImp = append(retImp, inp)
				retOut = append(retOut, o)
			}
		} else {
			retImp = append(retImp, inp)
		}
	}
	util.Assertf(len(retOut) <= maxAdditionalOutputs, "len(ret)<=maxAdditionalOutputs")
	return retImp, retOut
}

func (mf *milestoneFactory) getStatsAndReset() (ret factoryStats) {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	ret = factoryStats{
		numOwnMilestones:            len(mf.ownMilestones),
		ownMilestoneCount:           mf.ownMilestoneCount,
		removedMilestonesSinceReset: mf.removedMilestonesSinceReset,
		tipPoolStats:                mf.tipPool.getStatsAndReset(),
	}
	mf.removedMilestonesSinceReset = 0
	return
}