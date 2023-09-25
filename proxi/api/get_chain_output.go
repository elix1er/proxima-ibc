package api

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initGetChainOutputCmd(apiCmd *cobra.Command) {
	getUTXOCmd := &cobra.Command{
		Use:   "get_chain_output <chain ID hex-encoded>",
		Short: `returns chain output by chain ID`,
		Args:  cobra.ExactArgs(1),
		Run:   runGetChainOutputCmd,
	}
	getUTXOCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getUTXOCmd)
}

func runGetChainOutputCmd(_ *cobra.Command, args []string) {
	chainID, err := core.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	o, _, err := getClient().GetChainOutput(chainID)
	glb.AssertNoError(err)

	glb.Infof(o.String())
}
