package vertex

import (
	"errors"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

const (
	TxStatusUndefined = TxStatus(iota)
	TxStatusGood
	TxStatusBad
)

// ErrDeletedVertexAccessed exception is raised by PanicAccessDeleted handler of Unwrap vertex so that could be caught if necessary
var (
	ErrDeletedVertexAccessed = errors.New("deleted vertex should not be accessed")
	ErrShouldNotBeVirtualTx  = errors.New("virtualTx is unexpected")
)

func (v _vertex) _id() *core.TransactionID {
	return v.Tx.ID()
}

func (v _vertex) _time() time.Time {
	return v.whenWrapped
}

func (v _vertex) _outputAt(idx byte) (*core.Output, error) {
	return v.Tx.ProducedOutputAt(idx)
}

func (v _vertex) _hasOutputAt(idx byte) (bool, bool) {
	if int(idx) >= v.Tx.NumProducedOutputs() {
		return false, true
	}
	return true, false
}

func (v _virtualTx) _id() *core.TransactionID {
	return &v.txid
}

func (v _virtualTx) _time() time.Time {
	return time.Time{}
}

func (v _virtualTx) _outputAt(idx byte) (*core.Output, error) {
	if o, available := v.OutputAt(idx); available {
		return o, nil
	}
	return nil, nil
}

func (v _virtualTx) _hasOutputAt(idx byte) (bool, bool) {
	_, hasIt := v.OutputAt(idx)
	return hasIt, false
}

func (v _deletedTx) _id() *core.TransactionID {
	return &v.TransactionID
}

func (v _deletedTx) _time() time.Time {
	return time.Time{}
}

func (v _deletedTx) _outputAt(_ byte) (*core.Output, error) {
	panic(ErrDeletedVertexAccessed)
}

func (v _deletedTx) _hasOutputAt(_ byte) (bool, bool) {
	panic(ErrDeletedVertexAccessed)
}

func _newVID(g _genericWrapper) *WrappedTx {
	return &WrappedTx{
		_genericWrapper: g,
	}
}

func (vid *WrappedTx) _put(g _genericWrapper) {
	vid._genericWrapper = g
}

func (vid *WrappedTx) ConvertVirtualTxToVertex(v *Vertex) {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	vTx, isVirtualTx := vid._genericWrapper.(_virtualTx)
	lazy := func() any { return vid._id().StringShort() }
	util.Assertf(isVirtualTx, "ConvertVirtualTxToVertex: virtual tx expected %s", lazy)
	util.Assertf(vTx.txid == *v.Tx.ID(), "ConvertVirtualTxToVertex: txid-s do not match in: %s", lazy)
	vid._put(_vertex{Vertex: v})
}

func (vid *WrappedTx) ID() *core.TransactionID {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid._genericWrapper._id()
}

func (vid *WrappedTx) GetTxStatusNoLock() TxStatus {
	return vid.txStatus
}

func (vid *WrappedTx) GetTxStatus() TxStatus {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.txStatus
}

func (vid *WrappedTx) SetTxStatus(s TxStatus) {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	vid.txStatus = s
}

func (vid *WrappedTx) OnNotify(fun func(vid *WrappedTx)) {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	vid.onNotify = fun
}

func (vid *WrappedTx) Notify(downstreamVID *WrappedTx) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	if vid.onNotify != nil {
		vid.onNotify(downstreamVID)
	}
}

func (vid *WrappedTx) NotifyFutureCone() {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	for _, lst := range vid.consumers {
		for _, consumer := range lst {
			consumer.Notify(vid)
		}
	}

	for _, endorser := range vid.endorsers {
		endorser.Notify(vid)
	}
}

func WrapTxID(txid core.TransactionID) *WrappedTx {
	return _newVID(_virtualTx{VirtualTransaction: newVirtualTx(txid)})
}

func DecodeIDs(vids ...*WrappedTx) []*core.TransactionID {
	ret := make([]*core.TransactionID, len(vids))
	for i, vid := range vids {
		ret[i] = vid.ID()
	}
	return ret
}

func (vid *WrappedTx) IDShortString() string {
	return vid.ID().StringShort()
}

func (vid *WrappedTx) LazyIDShort() func() any {
	return func() any {
		return vid.IDShortString()
	}
}

func (vid *WrappedTx) IDVeryShort() string {
	return vid.ID().StringVeryShort()
}

func (vid *WrappedTx) IsBranchTransaction() bool {
	return vid.ID().IsBranchTransaction()
}

func (vid *WrappedTx) IsSequencerMilestone() bool {
	return vid.ID().IsSequencerMilestone()
}

func (vid *WrappedTx) Timestamp() core.LogicalTime {
	return vid.ID().Timestamp()
}

func (vid *WrappedTx) TimeSlot() core.TimeSlot {
	return vid._genericWrapper._id().TimeSlot()
}

func (vid *WrappedTx) MarkDeleted() {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	switch v := vid._genericWrapper.(type) {
	case _vertex:
		vid._put(_deletedTx{TransactionID: *v.Tx.ID()})
	case _virtualTx:
		vid._put(_deletedTx{TransactionID: v.txid})
	case _deletedTx:
		vid.PanicAccessDeleted()
	}
}

func (vid *WrappedTx) OutputWithIDAt(idx byte) (*core.OutputWithID, error) {
	ret, err := vid.OutputAt(idx)
	if err != nil || ret == nil {
		return nil, err
	}
	return &core.OutputWithID{
		ID:     core.NewOutputID(vid.ID(), idx),
		Output: ret,
	}, nil
}

// OutputAt return output at index, if available.
// err != nil indicates wrong index
// nil, nil means output not available, but no error (orphaned)
func (vid *WrappedTx) OutputAt(idx byte) (*core.Output, error) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid._outputAt(idx)
}

func (vid *WrappedTx) HasOutputAt(idx byte) (bool, bool) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid._hasOutputAt(idx)
}

func (vid *WrappedTx) SequencerIDIfAvailable() (core.ChainID, bool) {
	var isAvailable bool
	var ret core.ChainID
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			isAvailable = v.Tx.IsSequencerMilestone()
			if isAvailable {
				ret = v.Tx.SequencerTransactionData().SequencerID
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			if v.sequencerOutputs != nil {
				seqOData, ok := v.outputs[v.sequencerOutputs[0]].SequencerOutputData()
				util.Assertf(ok, "sequencer output data unavailable for the output #%d", v.sequencerOutputs[0])
				ret = seqOData.ChainConstraint.ID
				if ret == core.NilChainID {
					oid := vid.OutputID(v.sequencerOutputs[0])
					ret = core.OriginChainID(&oid)
				}
				isAvailable = true
			}
		},
	})
	return ret, isAvailable
}

func (vid *WrappedTx) MustSequencerID() core.ChainID {
	ret, ok := vid.SequencerIDIfAvailable()
	util.Assertf(ok, "not a sequencer milestone")
	return ret
}

func (vid *WrappedTx) SequencerPredecessor() (ret *WrappedTx) {
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		if seqData := v.Tx.SequencerTransactionData(); seqData != nil {
			ret = v.Inputs[seqData.SequencerOutputData.ChainConstraint.PredecessorInputIndex]
		}
	}})
	return
}

func (vid *WrappedTx) IsVertex() (ret bool) {
	vid.Unwrap(UnwrapOptions{Vertex: func(_ *Vertex) {
		ret = true
	}})
	return
}

func (vid *WrappedTx) IsVirtualTx() (ret bool) {
	vid.Unwrap(UnwrapOptions{VirtualTx: func(_ *VirtualTransaction) {
		ret = true
	}})
	return
}

func (vid *WrappedTx) OutputID(idx byte) (ret core.OutputID) {
	ret = core.NewOutputID(vid.ID(), idx)
	return
}

func (vid *WrappedTx) Unwrap(opt UnwrapOptions) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	switch v := vid._genericWrapper.(type) {
	case _vertex:
		if opt.Vertex != nil {
			opt.Vertex(v.Vertex)
		}
	case _virtualTx:
		if opt.VirtualTx != nil {
			opt.VirtualTx(v.VirtualTransaction)
		}

	case _deletedTx:
		if opt.Deleted != nil {
			opt.Deleted()
		}
	default:
		util.Assertf(false, "inconsistency: unsupported wrapped type")
	}
}

func (vid *WrappedTx) Time() time.Time {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid._genericWrapper._time()
}

func (vid *WrappedTx) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret.Add("== vertex %s", vid.IDShortString())
			ret.Append(v.Lines(prefix...))
		},
		VirtualTx: func(v *VirtualTransaction) {
			ret.Add("== virtual tx %s", vid.IDShortString())
			idxs := util.SortKeys(v.outputs, func(k1, k2 byte) bool {
				return k1 < k2
			})
			for _, i := range idxs {
				ret.Add("    #%d :", i)
				ret.Append(v.outputs[i].Lines("     "))
			}
		},
		Deleted: func() {
			ret.Add("== orphaned vertex")
		},
	})
	return ret
}

func (vid *WrappedTx) NumInputs() int {
	ret := 0
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.Tx.NumInputs()
	}})
	return ret
}

func (vid *WrappedTx) NumProducedOutputs() int {
	ret := 0
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.Tx.NumProducedOutputs()
	}})
	return ret
}

func (vid *WrappedTx) ConvertToVirtualTx() {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	switch v := vid._genericWrapper.(type) {
	case _vertex:
		vid._put(_virtualTx{VirtualTransaction: v.convertToVirtualTx()})
	case _deletedTx:
		vid.PanicAccessDeleted()
	}
}

func (vid *WrappedTx) WrappedInputs() []WrappedOutput {
	ret := make([]WrappedOutput, vid.NumInputs())
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		util.Assertf(v.IsSolid(), "not solid inputs of %s", v.Tx.IDShortString())

		v.ForEachInputDependency(func(i byte, inp *WrappedTx) bool {
			inpID := v.Tx.MustInputAt(i)
			ret[i] = WrappedOutput{
				VID:   inp,
				Index: inpID.Index(),
			}
			return true
		})
	}})
	return ret
}

func (vid *WrappedTx) PanicAccessDeleted() {
	txid := vid._genericWrapper.(_deletedTx).TransactionID
	util.Panicf("%w: %s", ErrDeletedVertexAccessed, txid.StringShort())
}

func (vid *WrappedTx) PanicShouldNotBeVirtualTx(_ *VirtualTransaction) {
	txid := vid._genericWrapper.(_virtualTx).txid
	util.Panicf("%w: %s", ErrShouldNotBeVirtualTx, txid.StringShort())
}

func (vid *WrappedTx) BaselineBranch() (baselineBranch *WrappedTx) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			baselineBranch = v.BaselineBranch
		},
		VirtualTx: func(v *VirtualTransaction) {
			if vid._genericWrapper._id().IsBranchTransaction() && vid.txStatus == TxStatusGood {
				baselineBranch = vid
			}
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return
}