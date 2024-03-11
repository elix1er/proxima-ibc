package node

import (
	"os"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/viper"
)

type ProximaNode struct {
	*global.Global
	multiStateDB         *badger_adaptor.DB
	txStoreDB            *badger_adaptor.DB
	txBytesStore         global.TxBytesStore
	peers                *peering.Peers
	workflow             *workflow.Workflow
	Sequencers           []*sequencer.Sequencer
	stopOnce             sync.Once
	workProcessesStopped sync.WaitGroup
	dbClosedWG           sync.WaitGroup
}

func init() {
	//pflag.Parse()
	//err := viper.BindPFlags(pflag.CommandLine)
	//util.AssertNoError(err)

	viper.SetConfigName("proxima")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	util.AssertNoError(err)
}

func New() *ProximaNode {
	ret := &ProximaNode{
		Global:     global.NewFromConfig(),
		Sequencers: make([]*sequencer.Sequencer, 0),
	}
	global.SetGlobalLogger(ret.Global)
	return ret
}

// WaitAllWorkProcessesToStop wait everything to stop before closing databases
func (p *ProximaNode) WaitAllWorkProcessesToStop(timeout ...time.Duration) {
	<-p.Ctx().Done()
	p.Global.MustWaitAllWorkProcessesStop(timeout...)
	p.workProcessesStopped.Done()
}

// WaitAllDBClosed ensuring databases has been closed
func (p *ProximaNode) WaitAllDBClosed() {
	p.dbClosedWG.Wait()
}

func (p *ProximaNode) StateStore() global.StateStore {
	return p.multiStateDB
}

func (p *ProximaNode) TxBytesStore() global.TxBytesStore {
	return p.txBytesStore
}

func (p *ProximaNode) readInTraceTags() {
	p.Global.StartTracingTags(viper.GetStringSlice("trace_tags")...)
}

func (p *ProximaNode) Start() {
	p.Log().Info(global.BannerString())
	p.readInTraceTags()
	p.workProcessesStopped.Add(1)

	err := util.CatchPanicOrError(func() error {
		p.initMultiStateLedger()
		p.initTxStore()
		p.initPeering()

		p.startWorkProcesses()
		p.startSequencers()
		p.startAPIServer()
		p.startMemoryLogging()
		p.startPProfIfEnabled()
		return nil
	})
	if err != nil {
		p.Log().Errorf("error on startup: %v", err)
		os.Exit(1)
	}
	p.Log().Infof("Proxima node has been started successfully")
	p.Log().Debug("running in debug mode")
}

// initMultiStateLedger opens ledger state DB and initializes global ledger object
func (p *ProximaNode) initMultiStateLedger() {
	var err error
	dbname := global.MultiStateDBName
	bdb, err := badger_adaptor.OpenBadgerDB(dbname)
	if err != nil {
		p.Log().Fatalf("can't open '%s'", dbname)
	}
	p.dbClosedWG.Add(1)
	p.multiStateDB = badger_adaptor.New(bdb)
	p.Log().Infof("opened multi-state DB '%s", dbname)

	// initialize global ledger object with the ledger ID data from DB
	multistate.InitLedgerFromStore(p.multiStateDB)
	p.Log().Infof("Ledger identity:\n%s", ledger.L().ID.Lines("       ").String())

	go func() {
		// wait until others will stop
		p.workProcessesStopped.Wait()
		_ = p.multiStateDB.Close()
		p.Log().Infof("multi-state database has been closed")
		p.dbClosedWG.Done()
	}()
}

func (p *ProximaNode) initTxStore() {
	switch viper.GetString(global.ConfigKeyTxStoreType) {
	case "dummy":
		p.Log().Infof("transaction store is 'dummy'")
		p.txBytesStore = txstore.NewDummyTxBytesStore()

	case "url":
		panic("'url' type of transaction store is not supported yet")

	default:
		// default option is predefined database name
		dbname := global.TxStoreDBName
		p.Log().Infof("transaction store database dbname is '%s'", dbname)
		p.txStoreDB = badger_adaptor.New(badger_adaptor.MustCreateOrOpenBadgerDB(dbname))
		p.dbClosedWG.Add(1)
		p.txBytesStore = txstore.NewSimpleTxBytesStore(p.txStoreDB)
		p.Log().Infof("opened DB '%s' as transaction store", dbname)

		go func() {
			p.workProcessesStopped.Wait()
			_ = p.txStoreDB.Close()
			p.Log().Infof("transaction store database has been closed")
			p.dbClosedWG.Done()
		}()
	}
}

func (p *ProximaNode) initPeering() {
	var err error
	p.peers, err = peering.NewPeersFromConfig(p)
	util.AssertNoError(err)

	p.peers.Run()

	go func() {
		<-p.Ctx().Done()
		p.peers.Stop()
	}()
}

func (p *ProximaNode) startWorkProcesses() {
	p.workflow = workflow.New(p, p.peers)
	p.workflow.Start()
}

func (p *ProximaNode) startSequencers() {
	sequencers := viper.GetStringMap("sequencers")
	if len(sequencers) == 0 {
		p.Log().Infof("No Sequencers will be started")
		return
	}
	p.Log().Infof("%d sequencer config profile(s) has been found", len(sequencers))

	seqNames := util.SortKeys(sequencers, func(k1, k2 string) bool {
		return k1 < k2
	})
	for _, name := range seqNames {
		seq, err := sequencer.NewFromConfig(name, p.workflow)
		if err != nil {
			p.Log().Errorf("can't start sequencer '%s': '%v'", name, err)
			continue
		}
		if seq == nil {
			p.Log().Infof("skipping disabled sequencer '%s'", name)
			continue
		}
		seq.Start()

		p.Log().Infof("started sequencer '%s', seqID: %s", name, util.Ref(seq.SequencerID()).String())
		p.Sequencers = append(p.Sequencers, seq)
		time.Sleep(500 * time.Millisecond)
	}
}
