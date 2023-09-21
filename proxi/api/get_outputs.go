package api

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/spf13/cobra"
)

func initGetOutputsCmd(apiCmd *cobra.Command) {
	getOutputsCmd := &cobra.Command{
		Use:   "get_outputs",
		Short: `returns all outputs locked in the accountable from the heaviest state of the latest epoch`,
		Args:  cobra.NoArgs,
		Run:   runGetOutputsCmd,
	}

	getOutputsCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getOutputsCmd)
}

func runGetOutputsCmd(_ *cobra.Command, _ []string) {
	accountable := glb.MustGetTarget()

	oData, err := getClient().GetAccountOutputs(accountable)
	console.AssertNoError(err)

	outs, err := txutils.ParseAndSortOutputData(oData, nil)
	console.AssertNoError(err)

	console.Infof("%d outputs locked in the account %s", len(outs), accountable.String())
	for i, o := range outs {
		console.Infof("-- output %d --", i)
		console.Infof(o.String())
		console.Verbosef("Raw bytes: %s", hex.EncodeToString(o.Output.Bytes()))
	}
}
