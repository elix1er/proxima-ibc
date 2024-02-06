package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/dag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func Test1SequencerPruner(t *testing.T) {
	ledger.SetTimeTickDuration(10 * time.Millisecond)
	t.Run("idle", func(t *testing.T) {
		const maxSlots = 20
		testData := initWorkflowTest(t, 1, true)
		t.Logf("%s", testData.wrk.Info())

		//testData.wrk.EnableTraceTags("seq,factory,tippool,txinput, proposer, incAttach")
		//testData.wrk.EnableTraceTags(sequencer.TraceTag, factory.TraceTag, tippool.TraceTag, proposer_base.TraceTag)

		ctx, _ := context.WithCancel(context.Background())
		seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, testData.genesisPrivKey,
			ctx, sequencer.WithMaxBranches(maxSlots))
		require.NoError(t, err)
		var countBr, countSeq atomic.Int32
		seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
			if ms.IsBranchTransaction() {
				countBr.Inc()
			} else {
				countSeq.Inc()
			}
		})
		seq.Start()
		seq.WaitStop()
		testData.stopAndWait()
		require.EqualValues(t, maxSlots, int(countBr.Load()))
		require.EqualValues(t, maxSlots, int(countSeq.Load()))
		t.Logf("%s", testData.wrk.Info())
		//br := testData.wrk.HeaviestBranchOfLatestTimeSlot()
		//dag.SaveGraphPastCone(br, "latest_branch")
	})
	t.Run("tag along transfers", func(t *testing.T) {
		const (
			maxSlots   = 40
			batchSize  = 10
			maxBatches = 5
			sendAmount = 2000
		)
		testData := initWorkflowTest(t, 1, true)
		//t.Logf("%s", testData.wrk.Info())

		//testData.wrk.EnableTraceTags(factory.TraceTag)

		ctx, _ := context.WithCancel(context.Background())
		seq, err := sequencer.New(testData.wrk, testData.bootstrapChainID, testData.genesisPrivKey,
			ctx, sequencer.WithMaxBranches(maxSlots))
		require.NoError(t, err)
		var countBr, countSeq atomic.Int32
		seq.OnMilestoneSubmitted(func(_ *sequencer.Sequencer, ms *vertex.WrappedTx) {
			if ms.IsBranchTransaction() {
				countBr.Inc()
			} else {
				countSeq.Inc()
			}
		})

		seq.Start()

		rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
		require.EqualValues(t, initBalance+tagAlongFee, int(rdr.BalanceOf(testData.addrAux.AccountID())))

		initialBalanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)

		auxOuts, err := rdr.GetOutputsForAccount(testData.addrAux.AccountID())
		require.EqualValues(t, 1, len(auxOuts))
		targetPrivKey := testutil.GetTestingPrivateKey(10000)
		targetAddr := ledger.AddressED25519FromPrivateKey(targetPrivKey)

		ctx, cancel := context.WithTimeout(context.Background(), (maxSlots+1)*ledger.SlotDuration())
		//ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		par := &spammerParams{
			t:             t,
			privateKey:    testData.privKeyFaucet,
			remainder:     testData.faucetOutput,
			tagAlongSeqID: []ledger.ChainID{testData.bootstrapChainID},
			target:        targetAddr,
			pace:          30,
			batchSize:     batchSize,
			maxBatches:    maxBatches,
			sendAmount:    sendAmount,
			tagAlongFee:   tagAlongFee,
			spammedTxIDs:  make([]ledger.TransactionID, 0),
		}
		go testData.spamTransfers(par, ctx)

		<-ctx.Done()
		cancel()

		require.EqualValues(t, batchSize*maxBatches, len(par.spammedTxIDs))

		seq.WaitStop()
		testData.stopAndWait()
		t.Logf("%s", testData.wrk.Info(true))

		testData.wrk.SaveGraph("utangle")
		testData.wrk.SaveTree("utangle_tree")

		require.EqualValues(t, maxSlots, int(countBr.Load()))

		rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
		for _, txid := range par.spammedTxIDs {
			require.True(t, rdr.KnowsCommittedTransaction(&txid))
			//t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), )
		}
		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, maxBatches*batchSize*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*(sendAmount+tagAlongFee), int(balanceLeft))

		balanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)
		require.EqualValues(t, int(initialBalanceOnChain)+len(par.spammedTxIDs)*tagAlongFee, int(balanceOnChain))
	})
}

func TestNSequencersIdlePruner(t *testing.T) {
	ledger.SetTimeTickDuration(10 * time.Millisecond)
	t.Run("finalize chain origins", func(t *testing.T) {
		const (
			nSequencers = 5 // in addition to bootstrap
		)
		testData := initMultiSequencerTest(t, nSequencers, true)

		testData.bootstrapSeq.StopAndWait()
		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info(true))
		//testData.wrk.SaveGraph("utangle")
	})
	t.Run("idle 2", func(t *testing.T) {
		const (
			maxSlots    = 50
			nSequencers = 1 // in addition to bootstrap
		)
		testData := initMultiSequencerTest(t, nSequencers, true)

		//testData.wrk.EnableTraceTags(proposer_endorse1.TraceTag)
		//testData.wrk.EnableTraceTags(proposer_base.TraceTag)
		//testData.wrk.EnableTraceTags(factory.TraceTag)
		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(attacher.TraceTagAttachVertex, attacher.TraceTagAttachOutput)

		testData.startSequencersWithTimeout(maxSlots)
		time.Sleep(20 * time.Second)
		testData.stopAndWaitSequencers()
		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info(true))
		testData.wrk.SaveGraph("utangle")
		dag.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))
	})
}

func Test5SequencersIdlePruner(t *testing.T) {
	ledger.SetTimeTickDuration(10 * time.Millisecond)
	const (
		maxSlots    = 50
		nSequencers = 4 // in addition to bootstrap
	)
	testData := initMultiSequencerTest(t, nSequencers, true)

	//testData.wrk.EnableTraceTags(proposer_base.TraceTag)
	testData.startSequencersWithTimeout(maxSlots)
	time.Sleep(20 * time.Second)
	testData.stopAndWaitSequencers()
	testData.stopAndWait()

	t.Logf("--------\n%s", testData.wrk.Info())
	//testData.wrk.SaveGraph("utangle")
	testData.wrk.SaveSequencerGraph(fmt.Sprintf("utangle_seq_tree_%d", nSequencers+1))
	dag.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))
}

func TestNSequencersTransferPruner(t *testing.T) {
	ledger.SetTimeTickDuration(10 * time.Millisecond)
	t.Run("seq 3 transfer 1 tag along", func(t *testing.T) {
		const (
			maxSlots        = 50
			nSequencers     = 2 // in addition to bootstrap
			batchSize       = 10
			sendAmount      = 2000
			spammingTimeout = 30 * time.Second
		)
		testData := initMultiSequencerTest(t, nSequencers, true)

		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(attacher.TraceTagAttachVertex, attacher.TraceTagAttachOutput)
		//testData.wrk.EnableTraceTags(proposer_endorse1.TraceTag)
		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(factory.TraceTag)

		rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
		require.EqualValues(t, initBalance*nSequencers, int(rdr.BalanceOf(testData.addrAux.AccountID())))

		initialBalanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)

		targetPrivKey := testutil.GetTestingPrivateKey(10000)
		targetAddr := ledger.AddressED25519FromPrivateKey(targetPrivKey)

		ctx, cancelSpam := context.WithTimeout(context.Background(), spammingTimeout)
		par := &spammerParams{
			t:             t,
			privateKey:    testData.privKeyFaucet,
			remainder:     testData.faucetOutput,
			tagAlongSeqID: []ledger.ChainID{testData.bootstrapChainID},
			target:        targetAddr,
			pace:          30,
			batchSize:     batchSize,
			//maxBatches:    maxBatches,
			sendAmount:   sendAmount,
			tagAlongFee:  tagAlongFee,
			spammedTxIDs: make([]ledger.TransactionID, 0),
		}
		go testData.spamTransfers(par, ctx)
		go func() {
			<-ctx.Done()
			cancelSpam()
			t.Log("spamming stopped")
		}()

		testData.startSequencersWithTimeout(maxSlots, spammingTimeout+(5*time.Second))

		testData.waitSequencers()
		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info())
		//testData.wrk.SaveGraph("utangle")
		dag.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))

		rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
		for _, txid := range par.spammedTxIDs {
			require.True(t, rdr.KnowsCommittedTransaction(&txid))
			//t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(&txid))
		}
		//require.EqualValues(t, (maxBatches+1)*batchSize, len(par.spammedTxIDs))

		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*(sendAmount+tagAlongFee), int(balanceLeft))

		balanceOnChain := rdr.BalanceOnChain(&testData.bootstrapChainID)
		require.EqualValues(t, int(initialBalanceOnChain)+len(par.spammedTxIDs)*tagAlongFee, int(balanceOnChain))
	})
	t.Run("seq 3 transfer multi tag along", func(t *testing.T) {
		const (
			maxSlots        = 50
			nSequencers     = 4 // in addition to bootstrap
			batchSize       = 10
			sendAmount      = 2000
			spammingTimeout = 30 * time.Second
		)
		testData := initMultiSequencerTest(t, nSequencers, true)

		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(attacher.TraceTagAttachVertex, attacher.TraceTagAttachOutput)
		//testData.wrk.EnableTraceTags(proposer_endorse1.TraceTag)
		//testData.wrk.EnableTraceTags(factory.TraceTagChooseExtendEndorsePair)
		//testData.wrk.EnableTraceTags(factory.TraceTag)

		rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
		require.EqualValues(t, initBalance*nSequencers, int(rdr.BalanceOf(testData.addrAux.AccountID())))

		targetPrivKey := testutil.GetTestingPrivateKey(10000)
		targetAddr := ledger.AddressED25519FromPrivateKey(targetPrivKey)

		tagAlongSeqIDs := []ledger.ChainID{testData.bootstrapChainID}
		for _, o := range testData.chainOrigins {
			tagAlongSeqIDs = append(tagAlongSeqIDs, o.ChainID)
		}
		tagAlongInitBalances := make(map[ledger.ChainID]uint64)
		for _, seqID := range tagAlongSeqIDs {
			tagAlongInitBalances[seqID] = rdr.BalanceOnChain(&seqID)
		}

		ctx, cancelSpam := context.WithTimeout(context.Background(), spammingTimeout)
		par := &spammerParams{
			t:             t,
			privateKey:    testData.privKeyFaucet,
			remainder:     testData.faucetOutput,
			tagAlongSeqID: tagAlongSeqIDs,
			target:        targetAddr,
			pace:          30,
			batchSize:     batchSize,
			//maxBatches:    maxBatches,
			sendAmount:   sendAmount,
			tagAlongFee:  tagAlongFee,
			spammedTxIDs: make([]ledger.TransactionID, 0),
		}
		go testData.spamTransfers(par, ctx)
		go func() {
			<-ctx.Done()
			cancelSpam()
			t.Log("spamming stopped")
		}()

		testData.startSequencersWithTimeout(maxSlots, spammingTimeout+(5*time.Second))

		testData.waitSequencers()
		testData.stopAndWait()

		t.Logf("%s", testData.wrk.Info())
		rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
		for _, txid := range par.spammedTxIDs {
			require.True(t, rdr.KnowsCommittedTransaction(&txid))
			t.Logf("    %s: in the heaviest state: %v", txid.StringShort(), rdr.KnowsCommittedTransaction(&txid))
		}

		//testData.wrk.SaveSequencerGraph(fmt.Sprintf("utangle_seq_tree_%d", nSequencers+1))
		dag.SaveBranchTree(testData.wrk.StateStore(), fmt.Sprintf("utangle_tree_%d", nSequencers+1))

		targetBalance := rdr.BalanceOf(targetAddr.AccountID())
		require.EqualValues(t, len(par.spammedTxIDs)*sendAmount, int(targetBalance))

		balanceLeft := rdr.BalanceOf(testData.addrFaucet.AccountID())
		require.EqualValues(t, initBalance-len(par.spammedTxIDs)*(sendAmount+tagAlongFee), int(balanceLeft))

		for seqID, initBal := range tagAlongInitBalances {
			balanceOnChain := rdr.BalanceOnChain(&seqID)
			t.Logf("%s tx: %d, init: %s, final: %s", seqID.StringShort(), par.perChainID[seqID], util.GoTh(initBal), util.GoTh(balanceOnChain))
			require.EqualValues(t, int(initBal)+par.perChainID[seqID]*tagAlongFee, int(balanceOnChain))
		}
	})
}