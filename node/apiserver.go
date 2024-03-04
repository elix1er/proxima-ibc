package node

import (
	"fmt"

	"github.com/lunfardo314/proxima/api/server"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startAPIServer() {
	port := viper.GetInt("api.server.port")
	addr := fmt.Sprintf(":%d", port)
	p.Log().Infof("starting API server on %s", addr)

	go server.RunOn(addr, p)
	go func() {
		<-p.Ctx().Done()
		p.stopAPIServer()
	}()
}

func (p *ProximaNode) stopAPIServer() {
	// do we need to do something else here?
	p.Log().Debugf("API server has been stopped")
}

func (p *ProximaNode) GetNodeInfo() *global.NodeInfo {
	alivePeers, configuredPeers := p.peers.NumPeers()
	ret := &global.NodeInfo{
		Name:           "a Proxima node",
		ID:             p.peers.SelfID(),
		NumStaticPeers: uint16(configuredPeers),
		NumActivePeers: uint16(alivePeers),
		Sequencers:     make([]ledger.ChainID, len(p.Sequencers)),
		Branches:       make([]ledger.TransactionID, 0),
	}
	// TODO
	//for i := range p.Sequencers {
	//	ret.Sequencers[i] = *p.Sequencers[i].ID()
	//}
	return ret
}

func (p *ProximaNode) HeaviestStateForLatestTimeSlot() multistate.SugaredStateReader {
	return p.workflow.HeaviestStateForLatestTimeSlot()
}

func (p *ProximaNode) SubmitTxBytesFromAPI(txBytes []byte) error {
	_, err := p.workflow.TxBytesIn(txBytes)
	return err
}

func (p *ProximaNode) QueryTxIDStatus(txid *ledger.TransactionID) vertex.TxIDStatus {
	return p.workflow.QueryTxIDStatus(txid)
}
