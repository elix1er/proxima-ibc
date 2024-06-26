package attacher

import (
	"fmt"
	"runtime"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

const (
	TraceTagAttachMilestone = "milestone"
	periodicCheckEach       = 100 * time.Millisecond
)

func runMilestoneAttacher(vid *vertex.WrappedTx, metadata *txmetadata.TransactionMetadata, callback func(vid *vertex.WrappedTx, err error), env Environment) {
	a := newMilestoneAttacher(vid, env, metadata)
	defer func() {
		go a.close()
	}()

	err := a.run()

	if err != nil {
		vid.SetTxStatusBad(err)
		env.Log().Warnf(a.logErrorStatusString(err))
	} else {
		msData := env.ParseMilestoneData(vid)
		env.Log().Info(a.logFinalStatusString(msData))
		vid.SetSequencerAttachmentFinished()
	}

	env.PokeAllWith(vid)

	// calling callback with timeout in order to detect wrong callbacks immediately
	ok := util.CallWithTimeout(func() {
		callback(vid, err)
	}, 200*time.Millisecond)
	if !ok {
		env.Log().Fatalf("AttachTransaction: Internal error: 200 milisec timeout exceeded while calling callback")
	}
}

func newMilestoneAttacher(vid *vertex.WrappedTx, env Environment, metadata *txmetadata.TransactionMetadata) *milestoneAttacher {
	env.Assertf(vid.IsSequencerMilestone(), "newMilestoneAttacher: %s is not a sequencer milestone", vid.IDShortString)

	ret := &milestoneAttacher{
		attacher: newPastConeAttacher(env, vid.IDShortString()),
		vid:      vid,
		metadata: metadata,
		pokeChan: make(chan struct{}),
		finals:   attachFinals{started: time.Now()},
	}
	ret.Tracef(TraceTagCoverageAdjustment, "newMilestoneAttacher: metadata of %s: %s", vid.IDShortString, metadata.String)

	ret.attacher.pokeMe = func(vid *vertex.WrappedTx) {
		ret.pokeMe(vid)
	}
	ret.vid.OnPoke(func() {
		ret._doPoke()
	})
	vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ret.finals.numInputs = v.Tx.NumInputs()
			ret.finals.numOutputs = v.Tx.NumProducedOutputs()
		},
		VirtualTx: func(_ *vertex.VirtualTransaction) {
			env.Log().Fatalf("unexpected virtual Tx: %s", vid.IDShortString())
		},
		Deleted: vid.PanicAccessDeleted,
	})
	ret.markVertexUndefined(vid)
	return ret
}

func (a *milestoneAttacher) run() error {
	// first solidify baseline state
	status := a.solidifyBaseline()
	if status != vertex.Good {
		a.Tracef(TraceTagAttachMilestone, "baseline solidification failed. Reason: %v", a.err)
		util.AssertMustError(a.err)
		return a.err
	}

	a.Assertf(a.baseline != nil, "a.baseline != nil")
	// then continue with the rest
	a.Tracef(TraceTagAttachMilestone, "baseline is OK <- %s", a.baseline.IDShortString)

	status = a.solidifyPastCone()
	if status != vertex.Good {
		a.Tracef(TraceTagAttachMilestone, "past cone solidification failed. Reason: %v", a.err)
		a.AssertMustError(a.err)
		return a.err
	}
	a.Tracef(TraceTagAttachMilestone, "past cone OK")
	a.AssertNoError(a.err)
	a.AssertNoError(a.checkConsistencyBeforeWrapUp())

	// finalizing touches
	a.wrapUpAttacher()

	if a.vid.IsBranchTransaction() {
		// branch transaction vertex is immediately converted to the virtual transaction.
		// Thus branch transaction does not reference past cone
		a.Tracef(TraceTagAttachMilestone, ">>>>>>>>>>>>>>> ConvertVertexToVirtualTx: %s", a.vid.IDShortString())

		a.vid.ConvertVertexToVirtualTx()
	}

	a.vid.SetTxStatusGood()
	a.PostEventNewGood(a.vid)
	a.SendToTippool(a.vid)

	return nil
}

func (a *milestoneAttacher) lazyRepeat(fun func() vertex.Status) vertex.Status {
	for {
		// repeat until becomes defined
		if status := fun(); status != vertex.Undefined {
			return status
		}
		select {
		case <-a.pokeChan:
			a.finals.numPokes++
			a.Tracef(TraceTagAttachMilestone, "poked")
		case <-a.Ctx().Done():
			a.setError(fmt.Errorf("attacher has been interrupted"))
			return vertex.Bad
		case <-time.After(periodicCheckEach):
			a.finals.numPeriodic++
			a.Tracef(TraceTagAttachMilestone, "periodic check")
		}
	}
}

func (a *milestoneAttacher) close() {
	a.closeOnce.Do(func() {
		a.unReferenceAllByAttacher()

		a.pokeClosingMutex.Lock()
		defer a.pokeClosingMutex.Unlock()

		a.closed = true
		close(a.pokeChan)
		a.vid.OnPoke(nil)
	})
}

func (a *milestoneAttacher) solidifyBaseline() vertex.Status {
	return a.lazyRepeat(func() vertex.Status {
		ok := false
		success := false
		util.Assertf(a.vid.FlagsUp(vertex.FlagVertexTxAttachmentStarted), "AttachmentStarted flag must be up")
		util.Assertf(!a.vid.FlagsUp(vertex.FlagVertexTxAttachmentFinished), "AttachmentFinished flag must be down")

		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				ok = a.solidifyBaselineVertex(v)
				if ok && v.BaselineBranch != nil {
					success = a.setBaseline(v.BaselineBranch, a.vid.Timestamp())
					a.Assertf(success, "solidifyBaseline %s: failed to set baseline", a.name)
				}
			},
			VirtualTx: func(_ *vertex.VirtualTransaction) {
				// TODO not needed.
				a.Log().Fatalf("solidifyBaseline: unexpected virtual tx %s", a.vid.IDShortString())
			},
		})
		switch {
		case !ok:
			return vertex.Bad
		case success:
			return vertex.Good
		default:
			return vertex.Undefined
		}
	})
}

// solidifyPastCone solidifies and validates sequencer transaction in the context of known baseline state
func (a *milestoneAttacher) solidifyPastCone() vertex.Status {
	return a.lazyRepeat(func() (status vertex.Status) {
		ok := true
		finalSuccess := false
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				if ok = a.attachVertexUnwrapped(v, a.vid); !ok {
					util.AssertMustError(a.err)
					return
				}
				if ok, finalSuccess = a.validateSequencerTx(v, a.vid); !ok {
					util.AssertMustError(a.err)
					v.UnReferenceDependencies()
				}
			},
		})
		switch {
		case !ok:
			return vertex.Bad
		case finalSuccess:
			return vertex.Good
		default:
			return vertex.Undefined
		}
	})
}

func (a *milestoneAttacher) _doPoke() {
	a.pokeClosingMutex.RLock()
	defer a.pokeClosingMutex.RUnlock()

	// must be non-blocking, otherwise deadlocks when syncing or high TPS
	if !a.closed {
		select {
		case a.pokeChan <- struct{}{}:
			//a.Log().Warnf(">>>>>> poked ok %s", a.name)
		default:
			// poke is lost when blocked but that is ok because there's pull from the attacher's side
			//a.Log().Warnf(">>>>>> missed poke in %s", a.name)
			a.finals.numMissedPokes.Add(1)
		}
	}
}

func (a *milestoneAttacher) pokeMe(with *vertex.WrappedTx) {
	flags := a.flags(with)
	util.Assertf(flags.FlagsUp(FlagAttachedVertexKnown), "must be marked known %s", with.IDShortString)
	if !flags.FlagsUp(FlagAttachedVertexAskedForPoke) {
		a.Tracef(TraceTagAttachMilestone, "pokeMe with %s", with.IDShortString())
		a.PokeMe(a.vid, with)
		a.setFlagsUp(with, FlagAttachedVertexAskedForPoke)
	}
}

func (a *milestoneAttacher) logFinalStatusString(msData *ledger.MilestoneData) string {
	var msg string

	msDataStr := " (n/a)"
	if msData != nil {
		msDataStr = fmt.Sprintf(" (%s %d/%d)", msData.Name, msData.BranchHeight, msData.ChainHeight)
	}
	if a.vid.IsBranchTransaction() {
		msg = fmt.Sprintf("-- ATTACH BRANCH%s %s(in %d/out %d), infl: %s",
			msDataStr, a.vid.IDShortString(), a.finals.numInputs, a.finals.numOutputs, util.GoTh(a.vid.InflationAmountOfSequencerMilestone()))
	} else {
		msg = fmt.Sprintf("-- ATTACH SEQ TX%s %s(in %d/out %d), infl: %s",
			msDataStr, a.vid.IDShortString(), a.finals.numInputs, a.finals.numOutputs, util.GoTh(a.vid.InflationAmountOfSequencerMilestone()))
	}
	if a.vid.GetTxStatus() == vertex.Bad {
		msg += fmt.Sprintf("BAD: err = '%v'", a.vid.GetError())
	} else {
		bl := "<nil>"
		if a.finals.baseline != nil {
			bl = a.finals.baseline.StringShort()
		}
		if a.vid.IsBranchTransaction() {
			msg += fmt.Sprintf(", base: %s, cov: %s, slot inflation: %s, supply: %s",
				bl,
				util.GoTh(a.finals.coverage),
				util.GoTh(a.finals.slotInflation),
				util.GoTh(a.finals.supply))
		} else {
			msg += fmt.Sprintf(", base: %s, cov: %s, slot inflation: %s",
				bl, util.GoTh(a.finals.coverage), util.GoTh(a.finals.slotInflation))
		}
	}
	if a.LogAttacherStats() {
		msg += "\n          " + a.logStatsString()
	}
	return msg
}

func (a *milestoneAttacher) logErrorStatusString(err error) string {
	msg := fmt.Sprintf("-- ATTACH %s -> BAD(%v)", a.vid.ID.StringShort(), err)
	if a.LogAttacherStats() {
		msg = msg + "\n          " + a.logStatsString()
	}
	return msg
}

func (a *milestoneAttacher) logStatsString() string {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memStr := fmt.Sprintf("Mem. alloc: %.1f MB, GC: %d, GoRt: %d, ",
		float32(memStats.Alloc*10/(1024*1024))/10,
		memStats.NumGC,
		runtime.NumGoroutine(),
	)

	utxoInOut := ""
	if a.vid.IsBranchTransaction() {
		utxoInOut = fmt.Sprintf("UTXO mut +%d/-%d, ", a.finals.numCreatedOutputs, a.finals.numDeletedOutputs)
	}
	return fmt.Sprintf("stats %s: new tx: %d, %spoked/missed: %d/%d, periodic: %d, duration: %s. %s",
		a.vid.IDShortString(),
		a.finals.numTransactions,
		utxoInOut,
		a.finals.numPokes,
		a.finals.numMissedPokes.Load(),
		a.finals.numPeriodic,
		time.Since(a.finals.started),
		memStr,
	)
}

func (a *milestoneAttacher) AdjustCoverage() {
	a.adjustCoverage()
	if a.coverageAdjustment > 0 {
		a.Tracef(TraceTagCoverageAdjustment, " milestoneAttacher: coverage has been adjusted by %s, ms: %s, baseline: %s",
			func() string { return util.GoTh(a.coverageAdjustment) }, a.vid.IDShortString, a.baseline.IDShortString)
	}
}
