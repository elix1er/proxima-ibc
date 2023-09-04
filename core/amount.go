package core

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

const amountSource = `

// ensuring endorsements are allowed only in sequencer transactions
func noEndorsingForNonSequencerTransaction : 
if(
	and(selfIsProducedOutput, not(isZero(numEndorsements))),
	require(isSequencerTransaction, !!!endorsements_are_allowed_only_in_sequencer_transactions),
	true
)

// $0 - amount uint64 big-endian
func amount: and(
    equal(selfBlockIndex,0), // amount must be at block 0
	mustSize($0,8),             // length must be 8
	noEndorsingForNonSequencerTransaction  // suboptimal, redundant repeating run on each produced output 
)

// utility function which extracts amount value from the output by evaluating it
// $0 - output bytes
func amountValue : evalBytecodeArg(@Array8($0, amountBlockIndex), #amount,0)

func selfAmountValue: amountValue(selfOutputBytes)

// utility function
func selfMustAmountAtLeast : if(
	lessThan(selfAmountValue, $0),
	!!!amount_is_smaller_than_expected,
	true
)

func selfMustStandardAmount: selfMustAmountAtLeast( 
	concat(u32/0, mul16_32(vbCost16,len16(selfOutputBytes)))
)

`

const (
	AmountConstraintName = "amount"
	amountTemplate       = AmountConstraintName + "(u64/%d)"
)

type Amount uint64

func (a Amount) Name() string {
	return AmountConstraintName
}

func (a Amount) source() string {
	return fmt.Sprintf(amountTemplate, uint64(a))
}

func (a Amount) Bytes() []byte {
	return mustBinFromSource(a.source())
}

func (a Amount) String() string {
	return fmt.Sprintf("%s(%s)", AmountConstraintName, util.GoThousands(int(a)))
}

func NewAmount(a uint64) Amount {
	return Amount(a)
}

func initAmountConstraint() {
	easyfl.MustExtendMany(amountSource)
	// sanity check
	example := NewAmount(1337)
	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	amountBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(sym == AmountConstraintName && len(amountBin) == 8 && binary.BigEndian.Uint64(amountBin) == 1337, "'amount' consistency check failed")
	registerConstraint(AmountConstraintName, prefix, func(data []byte) (Constraint, error) {
		return AmountFromBytes(data)
	})
}

func AmountFromBytes(data []byte) (Amount, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data)
	if err != nil {
		return 0, err
	}
	if sym != AmountConstraintName {
		return 0, fmt.Errorf("not an 'amount' constraint")
	}
	amountBin := easyfl.StripDataPrefix(args[0])
	if len(amountBin) != 8 {
		return 0, fmt.Errorf("wrong data length")
	}
	return Amount(binary.BigEndian.Uint64(amountBin)), nil
}

func (a Amount) Amount() uint64 {
	return uint64(a)
}
