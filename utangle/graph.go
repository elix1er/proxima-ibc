package utangle

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/dominikbraun/graph"
	"github.com/dominikbraun/graph/draw"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

var (
	fontsizeAttribute    = graph.VertexAttribute("fontsize", "10")
	simpleNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("colorscheme", "blues3"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "2"),
		graph.VertexAttribute("fillcolor", "1"),
	}
	seqNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("colorscheme", "paired9"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "9"),
	}
	finalTxAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("colorscheme", "bugn9"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "9"),
		graph.VertexAttribute("fillcolor", "1"),
	}
	orphanedTxAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("colorscheme", "bugn9"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "9"),
		graph.VertexAttribute("fillcolor", "1"),
	}
)

func sequencerNodeAttributes(v *Vertex, coverage uint64, dict map[core.ChainID]int) []func(*graph.VertexProperties) {
	seqID := v.Tx.SequencerTransactionData().SequencerID
	if _, found := dict[seqID]; !found {
		dict[seqID] = (len(dict) % 9) + 1
	}
	ret := make([]func(*graph.VertexProperties), len(seqNodeAttributes))
	copy(ret, seqNodeAttributes)
	ret = append(ret, graph.VertexAttribute("fillcolor", strconv.Itoa(dict[seqID])))
	if coverage > 0 {
		ret = append(ret, graph.VertexAttribute("xlabel", util.GoThousands(coverage)))
	}
	return ret
}

func makeGraphNode(vid *WrappedTx, gr graph.Graph[string, string], seqDict map[core.ChainID]int, highlighted bool) {
	id := vid.IDVeryShort()
	attr := simpleNodeAttributes
	var err error

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if v.Tx.IsSequencerMilestone() {
				attr = sequencerNodeAttributes(v, vid.LedgerCoverage(nil), seqDict)
			}
			if v.Tx.IsBranchTransaction() {
				attr = append(attr, graph.VertexAttribute("shape", "box"))
			}
			if highlighted {
				attr = append(attr, graph.VertexAttribute("penwidth", "3"))
			}
			err = gr.AddVertex(id, attr...)
		},
		VirtualTx: func(v *VirtualTransaction) {
			err = gr.AddVertex(id, finalTxAttributes...)
		},
		Deleted: func() {
			err = gr.AddVertex(id, orphanedTxAttributes...)
		},
	})
	util.AssertNoError(err)
}

func makeGraphEdges(vid *WrappedTx, gr graph.Graph[string, string]) {
	id := vid.IDVeryShort()
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
			o, err := v.getConsumedOutput(i)
			util.AssertNoError(err)
			outIndex := v.Tx.MustOutputIndexOfTheInput(i)
			edgeAttributes := []func(_ *graph.EdgeProperties){
				graph.EdgeAttribute("label", fmt.Sprintf("%s(#%d)", util.GoThousands(o.Amount()), outIndex)),
				graph.EdgeAttribute("fontsize", "10"),
			}
			_ = gr.AddEdge(id, inp.IDVeryShort(), edgeAttributes...)
			return true
		})
		v.forEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
			err := gr.AddEdge(id, vEnd.IDVeryShort(), graph.EdgeAttribute("color", "red"))
			util.Assertf(err == nil || errors.Is(err, graph.ErrEdgeAlreadyExists), "%v", err)
			return true
		})
	}})
}

func (ut *UTXOTangle) MakeGraph(additionalVertices ...*WrappedTx) graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	seqDict := make(map[core.ChainID]int)
	for _, vid := range ut.vertices {
		makeGraphNode(vid, ret, seqDict, false)
	}
	for _, vid := range additionalVertices {
		makeGraphNode(vid, ret, seqDict, true)
	}
	for _, vid := range ut.vertices {
		makeGraphEdges(vid, ret)
	}
	for _, vid := range additionalVertices {
		makeGraphEdges(vid, ret)
	}
	return ret
}

func (ut *UTXOTangle) SaveGraph(fname string) {
	gr := ut.MakeGraph()
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}

func MakeGraphPastCone(vid *WrappedTx, maxVertices ...int) graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	max := math.MaxUint16
	if len(maxVertices) > 0 && maxVertices[0] < math.MaxUint16 {
		max = maxVertices[0]
	}

	seqDict := make(map[core.ChainID]int)
	count := 0

	mkNode := func(vidCur *WrappedTx) bool {
		if count > max {
			return false
		}
		count++
		makeGraphNode(vidCur, ret, seqDict, false)
		return true
	}
	vid.TraversePastConeDepthFirst(UnwrapOptionsForTraverse{
		Vertex: func(vidCur *WrappedTx, _ *Vertex) bool {
			return mkNode(vidCur)
		},
		VirtualTx: func(vidCur *WrappedTx, vCur *VirtualTransaction) bool {
			return mkNode(vidCur)
		},
		Orphaned: func(vidCur *WrappedTx) bool {
			return mkNode(vidCur)
		},
	})
	count = 0
	vid.TraversePastConeDepthFirst(UnwrapOptionsForTraverse{
		Vertex: func(vidCur *WrappedTx, _ *Vertex) bool {
			makeGraphEdges(vidCur, ret)
			return true
		},
	})
	return ret
}

func SaveGraphPastCone(vid *WrappedTx, fname string) {
	gr := MakeGraphPastCone(vid, 500)
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}

func MakeGraphFromVertexSet(vertices set.Set[*WrappedTx]) graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())
	seqDict := make(map[core.ChainID]int)

	vertices.ForEach(func(vid *WrappedTx) bool {
		makeGraphNode(vid, ret, seqDict, false)
		return true
	})
	vertices.ForEach(func(vid *WrappedTx) bool {
		makeGraphEdges(vid, ret)
		return true
	})
	return ret
}

func SaveGraphFromVertexSet(vertices set.Set[*WrappedTx], fname string) {
	gr := MakeGraphFromVertexSet(vertices)
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}

var _branchNodeAttributes = []func(*graph.VertexProperties){
	fontsizeAttribute,
	graph.VertexAttribute("colorscheme", "accent8"),
	graph.VertexAttribute("style", "filled"),
	graph.VertexAttribute("color", "2"),
	graph.VertexAttribute("fillcolor", "1"),
}

func branchNodeAttributes(seqID *core.ChainID, coverage uint64, dict map[core.ChainID]int) []func(*graph.VertexProperties) {
	if _, found := dict[*seqID]; !found {
		dict[*seqID] = (len(dict) % 9) + 1
	}
	ret := make([]func(*graph.VertexProperties), len(_branchNodeAttributes))
	copy(ret, _branchNodeAttributes)
	ret = append(ret, graph.VertexAttribute("fillcolor", strconv.Itoa(dict[*seqID])))
	if coverage > 0 {
		ret = append(ret, graph.VertexAttribute("xlabel", util.GoThousands(coverage)))
	}
	return ret
}

// TODO MakeTree and SaveTree move to multistate

func MakeTree(stateStore general.StateStore, slots ...int) graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	var branches []*multistate.BranchData
	if len(slots) == 0 {
		branches = multistate.FetchBranchDataMulti(stateStore, multistate.FetchAllRootRecords(stateStore)...)
	} else {
		branches = multistate.FetchBranchDataMulti(stateStore, multistate.FetchRootRecordsNSlotsBack(stateStore, slots[0])...)
	}

	byOid := make(map[core.OutputID]*multistate.BranchData)
	idDict := make(map[core.ChainID]int)
	for _, b := range branches {
		byOid[b.Stem.ID] = b
		txid := b.Stem.ID.TransactionID()
		id := txid.Short()
		err := ret.AddVertex(id, branchNodeAttributes(&b.SequencerID, b.LedgerCoverage.Sum(), idDict)...)
		util.AssertNoError(err)
	}

	for _, b := range branches {
		txid := b.Stem.ID.TransactionID()
		id := txid.Short()
		stemLock, stemLockFound := b.Stem.Output.StemLock()
		util.Assertf(stemLockFound, "stem lock not found")

		if pred, ok := byOid[stemLock.PredecessorOutputID]; ok {
			txid := pred.Stem.ID.TransactionID()
			predID := txid.Short()
			err := ret.AddEdge(id, predID)
			util.AssertNoError(err)
		}
	}
	return ret
}

func (ut *UTXOTangle) SaveTree(fname string) {
	SaveTree(ut.stateStore, fname)
}

func SaveTree(stateStore general.StateStore, fname string, slotsBack ...int) {
	gr := MakeTree(stateStore, slotsBack...)
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}
