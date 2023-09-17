package db

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/proxi/config"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	supply      uint64
	description string
	nowis       time.Time
)

func initDbGenesis(dbCmd *cobra.Command) {
	genesisCmd := &cobra.Command{
		Use:   "genesis [--state_db] [--tx_store_db] [--supply <supply>] [--desc 'description']",
		Short: "creates genesis ledger state database and transaction store database",
		Args:  cobra.NoArgs,
		Run:   runGenesis,
	}
	nowis = time.Now()
	genesisCmd.Flags().Uint64Var(&supply, "supply", genesis.DefaultSupply, fmt.Sprintf("initial supply (default is %s", util.GoThousands(genesis.DefaultSupply)))
	defaultDesc := fmt.Sprintf("genesis has been created at Unix time (nanoseconds) %d", nowis.UnixNano())
	genesisCmd.Flags().StringVar(&description, "desc", defaultDesc, fmt.Sprintf("default is '%s'", defaultDesc))

	dbCmd.AddCommand(genesisCmd)
}

func runGenesis(_ *cobra.Command, args []string) {
	address := config.AddressBytes()
	if len(address) == 0 {
		console.Fatalf("private key not set. Use 'proxi setpk'")
	}
	dbName := viper.GetString(general.ConfigKeyMultiStateDbName)
	util.Assertf(dbName != "", "genesis database name not set")

	txStoreName := viper.GetString(general.ConfigKeyTxStoreName)
	if txStoreName == "" {
		txStoreName = dbName + ".txstore"
	}

	mustNotExist(dbName)
	mustNotExist(txStoreName)

	console.Infof("Creating genesis ledger state...")
	console.Infof("Multi-state database : %s", dbName)
	console.Infof("Transaction store database : %s", txStoreName)
	console.Infof("Initial supply: %s", util.GoThousands(supply))
	console.Infof("Description: '%s'", description)
	nowisTs := core.LogicalTimeFromTime(nowis)
	console.Infof("Genesis time slot: %d", nowisTs.TimeSlot())
	console.Infof("Genesis controller address: %s", config.AddressHex())

	if !console.YesNoPrompt(fmt.Sprintf("Create Proxima genesis '%s' and transactions store '%s'?", dbName, txStoreName), true) {
		console.Fatalf("exit: genesis database wasn't created")
	}
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName, badger.DefaultOptions(dbName))
	stateStore := badger_adaptor.New(stateDb)

	bootstrapChainID, _ := genesis.InitLedgerState(genesis.StateIdentityData{
		Description:                description,
		InitialSupply:              supply,
		GenesisControllerPublicKey: config.GetPrivateKey().Public().(ed25519.PublicKey),
		BaselineTime:               core.BaselineTime,
		TimeTickDuration:           core.TimeTickDuration(),
		MaxTimeTickValueInTimeSlot: core.TimeTicksPerSlot - 1,
		GenesisTimeSlot:            core.LogicalTimeFromTime(nowis).TimeSlot(),
	}, stateStore)
	console.AssertNoError(stateDb.Close())

	console.Infof("Genesis state DB '%s' has been created successfully.\nBootstrap sequencer chainID: %s", dbName, bootstrapChainID.String())

	txStoreDB := badger_adaptor.MustCreateOrOpenBadgerDB(txStoreName, badger.DefaultOptions(txStoreName))
	console.AssertNoError(txStoreDB.Close())

	console.Infof("Transaction store DB '%s' has been created successfully", dbName)

	config.SetKeyValue(general.ConfigKeyMultiStateDbName, dbName)
	config.SetKeyValue(general.ConfigKeyTxStoreName, txStoreName)
}

func mustNotExist(dir string) {
	_, err := os.Stat(dir)
	if err == nil {
		console.Fatalf("'%s' already exists", dir)
	} else {
		if !os.IsNotExist(err) {
			console.AssertNoError(err)
		}
	}
}