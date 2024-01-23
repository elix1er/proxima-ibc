package workflow

import (
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

type (
	txBytesInOptions struct {
		txMetadata       txmetadata.TransactionMetadata
		receivedFromPeer *peer.ID
		callback         func(vid *vertex.WrappedTx, err error)
	}

	TxBytesInOption func(options *txBytesInOptions)
)

const (
	TraceTagTxInput = "txinput"
	TraceTagDelay   = "delay"
)

func (w *Workflow) TxBytesIn(txBytes []byte, opts ...TxBytesInOption) (*ledger.TransactionID, error) {
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	txid := tx.ID()
	w.Tracef(TraceTagTxInput, "IN %s", txid.StringShort)

	options := &txBytesInOptions{}
	for _, opt := range opts {
		opt(options)
	}
	// bytes are identifiable as transaction
	util.Assertf(!options.txMetadata.IsResponseToPull || options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer,
		"inData.TxMetadata.IsResponseToPull || inData.TxMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer")

	w.StopPulling(tx.ID())

	if !tx.IsSequencerMilestone() {
		options.callback = func(_ *vertex.WrappedTx, _ error) {}
	}

	// TODO revisit checking lower time bounds
	enforceTimeBounds := options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypeAPI || options.txMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer
	// transaction is rejected if it is too far in the future wrt the local clock
	nowis := time.Now()

	timeUpperBound := nowis.Add(w.MaxDurationInTheFuture())
	err = tx.Validate(transaction.CheckTimestampUpperBound(timeUpperBound))
	if err != nil {
		if enforceTimeBounds {
			w.Tracef(TraceTagTxInput, "invalidate %s due to time bounds", txid.StringShort)
			err = fmt.Errorf("upper timestamp bound exceeded (MaxDurationInTheFuture = %v)", w.MaxDurationInTheFuture())
			attacher.InvalidateTxID(*txid, w, err)

			w.IncCounter("invalid")
			return txid, err
		}
		w.Log().Warnf("checking time bounds of %s: '%v'", txid.StringShort(), err)
	}

	// run remaining pre-validations on the transaction
	if err = tx.Validate(transaction.MainTxValidationOptions...); err != nil {
		w.Tracef(TraceTagTxInput, "invalidate %s due to failed validation: '%v'", txid.StringShort, err)
		err = fmt.Errorf("error while pre-validating transaction %s: '%w'", txid.StringShort(), err)
		attacher.InvalidateTxID(*txid, w, err)
		w.IncCounter("invalid")
		return txid, err
	}

	w.IncCounter("ok")
	if !options.txMetadata.IsResponseToPull {
		// gossip always, even if it needs delay.
		// Reason: other nodes might have slightly different clock, let them handle delay themselves
		w.Tracef(TraceTagTxInput, "send to gossip %s", txid.StringShort)
		w.GossipTransaction(tx, &options.txMetadata, options.receivedFromPeer)
	}

	// passes transaction to attacher
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := txid.Timestamp().Time()

	attachOpts := []attacher.Option{
		attacher.OptionWithTransactionMetadata(&options.txMetadata),
		attacher.OptionInvokedBy("txInput"),
	}
	if options.callback != nil {
		attachOpts = append(attachOpts, attacher.OptionWithAttachmentCallback(options.callback))
	}

	if txTime.Before(nowis) {
		// timestamp is in the past -> attach immediately
		w.IncCounter("ok.now")
		w.Tracef(TraceTagTxInput, "-> attach tx %s", txid.StringShort)
		attacher.AttachTransaction(tx, w, attachOpts...)
		return txid, nil
	}

	// timestamp is in the future. Put it on wait
	w.IncCounter("ok.delay")
	delayFor := txTime.Sub(nowis)
	w.Tracef(TraceTagTxInput, "%s -> delay for %v", txid.StringShort, delayFor)
	w.Tracef(TraceTagDelay, "%s -> delay for %v", txid.StringShort, delayFor)

	go func() {
		time.Sleep(delayFor)
		w.Tracef(TraceTagTxInput, "%s -> release", txid.StringShort)
		w.Tracef(TraceTagDelay, "%s -> release", txid.StringShort)
		w.IncCounter("ok.release")
		w.Tracef(TraceTagTxInput, "-> attach tx %s", txid.StringShort)
		attacher.AttachTransaction(tx, w, attachOpts...)
	}()
	return txid, nil
}

func (w *Workflow) SequencerMilestoneAttachWait(txBytes []byte, timeout time.Duration) (*vertex.WrappedTx, error) {
	type result struct {
		vid *vertex.WrappedTx
		err error
	}

	closed := false
	var closedMutex sync.Mutex
	resCh := make(chan result)
	defer func() {
		closedMutex.Lock()
		defer closedMutex.Unlock()
		closed = true
		close(resCh)
	}()

	go func() {
		writeResult := func(res result) {
			closedMutex.Lock()
			defer closedMutex.Unlock()
			if closed {
				return
			}
			resCh <- res
		}

		_, errParse := w.TxBytesIn(txBytes,
			WithMetadata(&txmetadata.TransactionMetadata{
				SourceTypeNonPersistent: txmetadata.SourceTypeSequencer,
			}),
			WithCallback(func(vid *vertex.WrappedTx, err error) {
				writeResult(result{vid: vid, err: err})
			}))
		if errParse != nil {
			writeResult(result{err: errParse})
		}
	}()
	select {
	case res := <-resCh:
		return res.vid, res.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout %v", timeout)
	}
}

func WithCallback(fun func(vid *vertex.WrappedTx, err error)) TxBytesInOption {
	return func(opts *txBytesInOptions) {
		opts.callback = fun
	}
}

func WithMetadata(metadata *txmetadata.TransactionMetadata) TxBytesInOption {
	return func(opts *txBytesInOptions) {
		if metadata != nil {
			opts.txMetadata = *metadata
		}
	}
}

func WithSourceType(sourceType txmetadata.SourceType) TxBytesInOption {
	return func(opts *txBytesInOptions) {
		opts.txMetadata.SourceTypeNonPersistent = sourceType
	}
}