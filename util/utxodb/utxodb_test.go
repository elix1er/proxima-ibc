package utxodb

import (
	"testing"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestUTXODB(t *testing.T) {
	t.Run("origin", func(t *testing.T) {
		u := NewUTXODB()
		genesisStemOutputID := general.GenesisStemOutputID(u.GenesisTimeSlot())
		genesisOutputID := general.GenesisChainOutputID(u.GenesisTimeSlot())
		t.Logf("genesis time slot: %d", u.GenesisTimeSlot())
		t.Logf("genesis addr: %s, balance: %s", u.GenesisControllerAddress().String(), util.GoThousands(u.Balance(u.GenesisControllerAddress())))
		t.Logf("faucet addr: %s, balance: %s", u.FaucetAddress().String(), util.GoThousands(u.Balance(u.FaucetAddress())))
		controlledByChain, onChain, err := u.BalanceOnChain(*u.GenesisChainID())
		require.NoError(t, err)
		t.Logf("bootstrap chainID: %s, on-chain balance: %s, controlled by chain: %s", u.GenesisChainID().String(), util.GoThousands(onChain), util.GoThousands(controlledByChain))
		t.Logf("origin output: %s\n%s", genesisOutputID.String(), u.genesisOutput.ToString("   "))
		t.Logf("origin stem output: %s\n%s", genesisStemOutputID.String(), u.genesisStemOutput.ToString("   "))

		t.Logf("\nUTXODB origin distribution transaction:\n%s", u.OriginDistributionTransactionString())
		require.EqualValues(t, int(initFaucetBalance), int(u.Balance(u.FaucetAddress())))
		require.EqualValues(t, int(supplyForTesting-initFaucetBalance), int(u.Balance(u.GenesisControllerAddress())))
		require.EqualValues(t, supplyForTesting-initFaucetBalance, onChain)
		require.EqualValues(t, 0, controlledByChain)
		t.Logf("State identity:\n%s", u.StateIdentityData().String())
	})
	t.Run("from faucet", func(t *testing.T) {
		u := NewUTXODB()
		addr := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(100))
		err := u.TokensFromFaucet(addr, 1337)
		require.NoError(t, err)
		require.EqualValues(t, 1337, int(u.Balance(addr)))
		require.EqualValues(t, initFaucetBalance-1337, u.Balance(u.FaucetAddress()))
	})
	t.Run("from faucet multi", func(t *testing.T) {
		u := NewUTXODB()
		_, _, addrs := u.GenerateAddressesWithFaucetAmount(100, 10, 1337)
		require.EqualValues(t, 10, len(addrs))
		for _, a := range addrs {
			require.EqualValues(t, 1337, u.Balance(a))
		}
	})
}
