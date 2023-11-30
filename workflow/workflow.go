package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/consumer"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/testutil"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	Workflow struct {
		stopFun         context.CancelFunc
		startOnce       sync.Once
		stopOnce        sync.Once
		working         atomic.Bool
		startPrunerOnce sync.Once
		log             *zap.SugaredLogger
		configParams    ConfigParams
		utxoTangle      *utangle.UTXOTangle
		peers           *peering.Peers
		debugCounters   *testutil.SyncCounters

		primaryInputConsumer *PrimaryConsumer
		preValidateConsumer  *PreValidateConsumer
		solidifyConsumer     *SolidifyConsumer
		pullConsumer         *PullTxConsumer
		validateConsumer     *ValidateConsumer
		appendTxConsumer     *AppendTxConsumer
		eventsConsumer       *EventsConsumer
		pullRequestConsumer  *PullRespondConsumer
		txGossipOutConsumer  *TxGossipSendConsumer

		handlersMutex sync.RWMutex
		eventHandlers map[eventtype.EventCode][]func(any)

		terminateWG sync.WaitGroup
		startWG     sync.WaitGroup

		traceMilestones atomic.Bool
	}

	Consumer[T any] struct {
		*consumer.Consumer[T]
		glb       *Workflow
		traceFlag bool
	}
)

const workflowLogName = "[workflow]"

func New(ut *utangle.UTXOTangle, peers *peering.Peers, configOptions ...ConfigOption) *Workflow {
	cfg := defaultConfigParams()
	for _, opt := range configOptions {
		opt(&cfg)
	}

	ret := &Workflow{
		configParams:  cfg,
		log:           general.NewLogger(workflowLogName, cfg.logLevel, cfg.logOutput, cfg.logTimeLayout),
		utxoTangle:    ut,
		peers:         peers,
		debugCounters: testutil.NewSynCounters(),
		eventHandlers: make(map[eventtype.EventCode][]func(any)),
	}
	ret.initPrimaryInputConsumer()
	ret.initPreValidateConsumer()
	ret.initSolidifyConsumer()
	ret.initPullConsumer()
	ret.initValidateConsumer()
	ret.initAppendTxConsumer()
	ret.initEventsConsumer()
	ret.initRespondTxQueryConsumer()
	ret.initGossipSendConsumer()

	ret.peers.OnReceiveTxBytes(func(from peer.ID, txBytes []byte) {
		if !ret.working.Load() {
			return
		}
		err := ret.TransactionIn(txBytes,
			WithTransactionSourcePeer(from),
			WithTraceCondition(func(tx *transaction.Transaction, _ TransactionSourceType, _ peer.ID) bool {
				return tx.IsSequencerMilestone()
			},
			))
		if err != nil {
			ret.log.Debugf("TransactionIn: %v", err)
			return
		}
	})

	ret.peers.OnReceivePullRequest(func(from peer.ID, txids []core.TransactionID) {
		if !ret.working.Load() {
			return
		}

		for _, txid := range txids {
			ret.pullRequestConsumer.Log().Infof(">>>>>>>>>>>>>> pull request received for %s", txid.StringShort())
			ret.pullRequestConsumer.Push(PullRespondData{
				TxID:   txid,
				PeerID: from,
			})
		}
	})

	err := ret.OnEvent(EventDroppedTx, func(dropData DropTxData) {
		ret.IncCounter("drop." + dropData.WhoDropped)
		ret.log.Debugf("dropped %s by '%s'. Reason: '%s'", dropData.TxID.StringShort(), dropData.WhoDropped, dropData.Msg)
		ret.pullConsumer.removeFromPullList(dropData.TxID)
		ret.solidifyConsumer.removeTxID(dropData.TxID)
	})
	util.AssertNoError(err)

	return ret
}

func (w *Workflow) LogLevel() zapcore.Level {
	return w.log.Level()
}

func (w *Workflow) SetTraceMilestones(f bool) {
	w.traceMilestones.Store(f)
}

func (w *Workflow) Start(parentCtx ...context.Context) {
	w.startOnce.Do(func() {
		w.log.Infof("STARTING [loglevel=%s]..", w.log.Level())

		var ctx context.Context
		if len(parentCtx) > 0 {
			ctx, w.stopFun = context.WithCancel(parentCtx[0])
		} else {
			ctx, w.stopFun = context.WithCancel(context.Background())
		}
		w.startWG.Add(1)

		w.primaryInputConsumer.Start()
		w.preValidateConsumer.Start()
		w.solidifyConsumer.Start()
		w.pullConsumer.Start()
		w.validateConsumer.Start()
		w.appendTxConsumer.Start()
		w.eventsConsumer.Start()
		w.pullRequestConsumer.Start()
		w.txGossipOutConsumer.Start()

		w.startWG.Done()
		w.working.Store(true)

		go func() {
			<-ctx.Done()

			util.Assertf(w.working.Swap(false), "wasn't started yet")
			w.startWG.Wait()
			w.primaryInputConsumer.Stop()
			w.terminateWG.Wait()
			w.log.Info("all consumers STOPPED")
			_ = w.log.Sync()
		}()
	})
}

func (w *Workflow) StartPruner() {
	w.startPrunerOnce.Do(func() {
		w.startPruner()
	})
}

func (w *Workflow) Stop() {
	w.stopOnce.Do(func() {
		w.stopFun()
	})
}

func (w *Workflow) WaitStop() {
	w.terminateWG.Wait()
}

func (w *Workflow) IsRunning() bool {
	return w.working.Load()
}

const maxWaitingTimeSlots = 10_000

func (w *Workflow) maxDurationInTheFuture() time.Duration {
	return time.Duration(maxWaitingTimeSlots) * core.TransactionTimePaceDuration()
}

func (w *Workflow) AddCounter(name string, i int) {
	w.debugCounters.Add(name, i)
}

func (w *Workflow) IncCounter(name string) {
	w.debugCounters.Inc(name)
}

func (w *Workflow) QueueInfo() string {
	m := map[string]string{
		w.primaryInputConsumer.Name(): w.primaryInputConsumer.InfoStr(),
		w.preValidateConsumer.Name():  w.preValidateConsumer.InfoStr(),
		w.solidifyConsumer.Name():     w.solidifyConsumer.InfoStr(),
		w.validateConsumer.Name():     w.validateConsumer.InfoStr(),
		w.appendTxConsumer.Name():     w.appendTxConsumer.InfoStr(),
		w.eventsConsumer.Name():       w.eventsConsumer.InfoStr(),
	}
	var ret strings.Builder
	for n, i := range m {
		_, _ = fmt.Fprintf(&ret, "%s: %s\n", n, i)
	}
	return ret.String()
}

func (w *Workflow) CounterInfo() string {
	return w.debugCounters.String()
}

func (w *Workflow) CheckDebugCounters(expect map[string]int) error {
	return w.debugCounters.CheckValues(expect)
}
