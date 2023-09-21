package main

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/testutil"
)

const usage = "Usage: genpk <output file name> <number of private keys/addresses to generate>"

func main() {
	if len(os.Args) != 3 {
		fmt.Println(usage)
		os.Exit(1)
	}
	n, err := strconv.Atoi(os.Args[2])
	util.AssertNoError(err)
	util.Assertf(n > 0, "must be a positive number")
	fmt.Printf("FOR TESTING PURPOSES ONLY! DO NOT USE IN PRODUCTION!\nGenerate %d private keys and ED25519 addresses to the file %s.yaml\n", n, os.Args[1])

	privateKeys := testutil.GetTestingPrivateKeys(n, rand.Int())
	addresses := make([]core.AddressED25519, len(privateKeys))

	for i := range privateKeys {
		addresses[i] = core.AddressED25519FromPrivateKey(privateKeys[i])
	}

	ln := lines.New().
		Add("# This file was generated by 'genpk' program. ").
		Add("# command line: '%s'", strings.Join(os.Args, " ")).
		Add("# FOR TESTING PURPOSES ONLY! DO NOT USE IN PRODUCTION!")

	for i := range privateKeys {
		ln.Add("# --- %d ---", i)
		ln.Add("-")
		ln.Add("   pk: %s", hex.EncodeToString(privateKeys[i]))
		ln.Add("   addr: %s", addresses[i].String())
	}

	err = os.WriteFile(os.Args[1]+".yaml", []byte(ln.String()), 0644)
	util.AssertNoError(err)
}
