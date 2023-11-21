package workflow

import (
	"github.com/lunfardo314/proxima/peering"
)

// TxOutboundConsumer is forwarding the transaction to peering which didn't see it yet

const TxOutboundConsumerName = "outbound"

type (
	TxOutboundConsumerData struct {
		*PrimaryInputConsumerData
		ReceivedFrom peering.PeerID
	}

	TxOutboundConsumer struct {
		*Consumer[TxOutboundConsumerData]
	}
)

func (w *Workflow) initTxOutboundConsumer() {
	c := &TxOutboundConsumer{
		Consumer: NewConsumer[TxOutboundConsumerData](TxOutboundConsumerName, w),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.terminateWG.Done()
	})
	w.txOutboundConsumer = c
}

func (c *TxOutboundConsumer) consume(inp TxOutboundConsumerData) {
	if inp.SourceType == TransactionSourceTypePeer {
		c.glb.peers.GossipTxBytesToPeers(inp.Tx.Bytes(), inp.ReceivedFrom)
	} else {
		c.glb.peers.GossipTxBytesToPeers(inp.Tx.Bytes())
	}
}