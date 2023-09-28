package db

import (
	"strconv"

	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

var outputFile string

func initDBTreeCmd(dbCmd *cobra.Command) {
	dbTreeCmd := &cobra.Command{
		Use:   "tree",
		Short: "create .DOT file for the tree of all branches",
		Args:  cobra.MaximumNArgs(1),
		Run:   runDbTreeCmd,
	}
	dbTreeCmd.PersistentFlags().StringVarP(&outputFile, "output", "o", "", "output file")

	dbTreeCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(dbTreeCmd)
}

func runDbTreeCmd(_ *cobra.Command, args []string) {
	dbName := GetMultiStateStoreName()
	if dbName == "(not set)" {
		return
	}
	outFile := outputFile
	if outFile == "" {
		outFile = dbName + "_TREE"
	}
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	defer stateDb.Close()

	stateStore := badger_adaptor.New(stateDb)

	if len(args) == 0 {
		utangle.SaveTree(stateStore, outFile)
	} else {
		slots, err := strconv.Atoi(args[0])
		glb.AssertNoError(err)
		utangle.SaveTree(stateStore, outFile, slots)
	}
}
