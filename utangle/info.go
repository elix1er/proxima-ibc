package utangle

import (
	"bytes"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type (
	SummarySupplyAndInflation struct {
		NumberOfBranches int
		OldestSlot       core.TimeSlot
		LatestSlot       core.TimeSlot
		BeginSupply      uint64
		EndSupply        uint64
		TotalInflation   uint64
		InfoPerSeqID     map[core.ChainID]SequencerInfo
	}

	SequencerInfo struct {
		BeginBalance   uint64
		EndBalance     uint64
		TotalInflation uint64
		NumBranches    int
	}
)

func (ut *UTXOTangle) FetchSummarySupplyAndInflation(nBack int) *SummarySupplyAndInflation {
	branchData := multistate.FetchHeaviestBranchChainNSlotsBack(ut.stateStore, nBack) // descending
	util.Assertf(len(branchData) > 0, "len(branchData) > 0")

	ret := &SummarySupplyAndInflation{
		BeginSupply:      branchData[len(branchData)-1].Stem.Output.MustStemLock().Supply,
		EndSupply:        branchData[0].Stem.Output.MustStemLock().Supply,
		TotalInflation:   0,
		NumberOfBranches: len(branchData),
		OldestSlot:       branchData[len(branchData)-1].Stem.Timestamp().TimeSlot(),
		LatestSlot:       branchData[0].Stem.Timestamp().TimeSlot(),
		InfoPerSeqID:     make(map[core.ChainID]SequencerInfo),
	}
	for i := 0; i < len(branchData)-1; i++ {
		inflation := branchData[i].Stem.Output.MustStemLock().InflationAmount
		ret.TotalInflation += inflation

		seqInfo := ret.InfoPerSeqID[branchData[i].SequencerID]
		seqInfo.NumBranches++
		seqInfo.TotalInflation += inflation
		ret.InfoPerSeqID[branchData[i].SequencerID] = seqInfo
	}
	util.Assertf(ret.EndSupply-ret.BeginSupply == ret.TotalInflation, "FetchSummarySupplyAndInflation: ret.EndSupply - ret.BeginSupply == ret.TotalInflation")

	for seqID, seqInfo := range ret.InfoPerSeqID {
		seqInfo.EndBalance = branchData[len(branchData)-1].SequencerOutput.Output.Amount()

		for i := range branchData {
			rdr := multistate.MustNewSugaredReadableState(ut.stateStore, branchData[i].Root)
			o, err := rdr.GetChainOutput(&seqID)
			if err == nil {
				seqInfo.BeginBalance = o.Output.Amount()
				break
			}
		}
		ret.InfoPerSeqID[seqID] = seqInfo
	}
	return ret
}

func (s *SummarySupplyAndInflation) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...).
		Add("Slots from %d to %d inclusive. Total %d slots", s.OldestSlot, s.LatestSlot, s.LatestSlot-s.OldestSlot+1).
		Add("Number of branches: %d", s.NumberOfBranches).
		Add("Supply begin: %s", util.GoThousands(s.BeginSupply)).
		Add("Supply end: %s", util.GoThousands(s.EndSupply)).
		Add("Total inflation: %s", util.GoThousands(s.TotalInflation)).
		Add("Info per sequencer:")

	sortedSeqIDs := util.KeysSorted(s.InfoPerSeqID, func(k1, k2 core.ChainID) bool {
		return bytes.Compare(k1[:], k2[:]) < 0
	})
	for _, seqId := range sortedSeqIDs {
		seqInfo := s.InfoPerSeqID[seqId]
		ret.Add("    %s : inflation: %s, number of branches: %d, balance: %s -> %s",
			seqId.Short(), util.GoThousands(seqInfo.TotalInflation), seqInfo.NumBranches,
			util.GoThousands(seqInfo.BeginBalance), util.GoThousands(seqInfo.EndBalance))
	}
	return ret
}
