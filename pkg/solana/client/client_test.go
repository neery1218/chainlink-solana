package client

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/pkg/errors"
	"github.com/smartcontractkit/chainlink-solana/pkg/solana/config"
	"github.com/smartcontractkit/chainlink-solana/pkg/solana/db"
	"github.com/smartcontractkit/chainlink/core/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_Reader_Integration(t *testing.T) {
	url := SetupLocalSolNode(t)
	privKey, err := solana.NewRandomPrivateKey()
	require.NoError(t, err)
	pubKey := privKey.PublicKey()
	FundTestAccounts(t, []solana.PublicKey{pubKey}, url)

	requestTimeout := 5 * time.Second
	lggr := logger.TestLogger(t)
	cfg := config.NewConfig(db.ChainCfg{}, lggr)

	c, err := NewClient(url, cfg, requestTimeout, lggr)
	require.NoError(t, err)

	// check balance
	bal, err := c.Balance(pubKey)
	assert.NoError(t, err)
	assert.Equal(t, uint64(100_000_000_000), bal) // once funds get sent to the system program it should be unrecoverable (so this number should remain > 0)

	// check SlotHeight
	slot0, err := c.SlotHeight()
	assert.NoError(t, err)
	assert.Greater(t, slot0, uint64(0))
	time.Sleep(time.Second)
	slot1, err := c.SlotHeight()
	assert.NoError(t, err)
	assert.Greater(t, slot1, slot0)

	// fetch recent blockhash
	hash, err := c.LatestBlockhash()
	assert.NoError(t, err)
	assert.NotEqual(t, hash.Value.Blockhash, solana.Hash{}) // not an empty hash

	// GetFeeForMessage (transfer to self, successful)
	tx, err := solana.NewTransaction(
		[]solana.Instruction{
			system.NewTransferInstruction(
				1,
				pubKey,
				pubKey,
			).Build(),
		},
		hash.Value.Blockhash,
		solana.TransactionPayer(pubKey),
	)
	assert.NoError(t, err)

	fee, err := c.GetFeeForMessage(tx.Message.ToBase64())
	assert.NoError(t, err)
	assert.Equal(t, uint64(5000), fee)

	// get chain ID based on gensis hash
	network, err := c.ChainID()
	assert.NoError(t, err)
	assert.Equal(t, "localnet", network)

	// get account info (also tested inside contract_test)
	res, err := c.GetAccountInfoWithOpts(context.TODO(), solana.PublicKey{}, &rpc.GetAccountInfoOpts{Commitment: rpc.CommitmentFinalized})
	assert.NoError(t, err)
	assert.Equal(t, uint64(1), res.Value.Lamports)
	assert.Equal(t, "NativeLoader1111111111111111111111111111111", res.Value.Owner.String())
}

func TestClient_Reader_ChainID(t *testing.T) {
	genesisHashes := []string{
		DevnetGenesisHash,  // devnet
		TestnetGenesisHash, // testnet
		MainnetGenesisHash, // mainnet
		"GH7ome3EiwEr7tu9JuTh2dpYWBJK3z69Xm1ZE3MEE6JC", // localnet (random)
	}
	networks := []string{"devnet", "testnet", "mainnet", "localnet"}
	hashCounter := 0

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := fmt.Sprintf(`{"jsonrpc":"2.0","result":"%s","id":1}`, genesisHashes[hashCounter])
		hashCounter++
		_, err := w.Write([]byte(out))
		require.NoError(t, err)
	}))
	defer mockServer.Close()

	requestTimeout := 5 * time.Second
	lggr := logger.TestLogger(t)
	cfg := config.NewConfig(db.ChainCfg{}, lggr)
	c, err := NewClient(mockServer.URL, cfg, requestTimeout, lggr)
	require.NoError(t, err)

	// get chain ID based on gensis hash
	for _, n := range networks {
		network, err := c.ChainID()
		assert.NoError(t, err)
		assert.Equal(t, n, network)
	}
}

func TestClient_Writer_Integration(t *testing.T) {
	url := SetupLocalSolNode(t)
	privKey, err := solana.NewRandomPrivateKey()
	require.NoError(t, err)
	pubKey := privKey.PublicKey()
	FundTestAccounts(t, []solana.PublicKey{pubKey}, url)

	requestTimeout := 5 * time.Second
	lggr := logger.TestLogger(t)
	cfg := config.NewConfig(db.ChainCfg{}, lggr)

	ctx := context.Background()
	c, err := NewClient(url, cfg, requestTimeout, lggr)
	require.NoError(t, err)

	// create + sign transaction
	createTx := func(to solana.PublicKey) *solana.Transaction {
		hash, err := c.LatestBlockhash()
		assert.NoError(t, err)

		tx, err := solana.NewTransaction(
			[]solana.Instruction{
				system.NewTransferInstruction(
					1,
					pubKey,
					to,
				).Build(),
			},
			hash.Value.Blockhash,
			solana.TransactionPayer(pubKey),
		)
		assert.NoError(t, err)
		_, err = tx.Sign(
			func(key solana.PublicKey) *solana.PrivateKey {
				if pubKey.Equals(key) {
					return &privKey
				}
				return nil
			},
		)
		assert.NoError(t, err)
		return tx
	}

	// simulate successful transcation
	txSuccess := createTx(pubKey)
	simSuccess, err := c.SimulateTx(ctx, txSuccess, nil)
	assert.NoError(t, err)
	assert.Nil(t, simSuccess.Err)
	assert.Equal(t, 0, len(simSuccess.Accounts)) // default option, no accounts requested

	// simulate successful transcation with custom options
	simCustom, err := c.SimulateTx(ctx, txSuccess, &rpc.SimulateTransactionOpts{
		Commitment: c.commitment,
		Accounts: &rpc.SimulateTransactionAccountsOpts{
			Encoding:  solana.EncodingBase64,
			Addresses: txSuccess.Message.AccountKeys, // request data for accounts in the tx
		},
	})
	assert.NoError(t, err)
	assert.Equal(t, len(txSuccess.Message.AccountKeys), len(simCustom.Accounts)) // data should be returned for the accounts in the tx

	// simulate failed transaction
	txFail := createTx(solana.MustPublicKeyFromBase58("11111111111111111111111111111111"))
	simFail, err := c.SimulateTx(ctx, txFail, nil)
	assert.NoError(t, err)
	assert.NotNil(t, simFail.Err)

	// send successful + failed tx to get tx signatures
	sigSuccess, err := c.SendTx(ctx, txSuccess)
	assert.NoError(t, err)

	sigFail, err := c.SendTx(ctx, txFail)
	assert.NoError(t, err)

	// check signature statuses
	time.Sleep(2 * time.Second) // wait for processing
	statuses, err := c.SignatureStatuses(ctx, []solana.Signature{sigSuccess, sigFail})
	assert.NoError(t, err)

	assert.Nil(t, statuses[0].Err)
	assert.NotNil(t, statuses[1].Err)
}

func TestClient_SendTxDuplicates_Integration(t *testing.T) {
	// set up environment
	url := SetupLocalSolNode(t)
	privKey, err := solana.NewRandomPrivateKey()
	require.NoError(t, err)
	pubKey := privKey.PublicKey()
	FundTestAccounts(t, []solana.PublicKey{pubKey}, url)

	// create client
	requestTimeout := 5 * time.Second
	lggr := logger.TestLogger(t)
	cfg := config.NewConfig(db.ChainCfg{}, lggr)
	c, err := NewClient(url, cfg, requestTimeout, lggr)
	require.NoError(t, err)

	// fetch recent blockhash
	hash, err := c.LatestBlockhash()
	assert.NoError(t, err)

	initBal, err := c.Balance(pubKey)
	assert.NoError(t, err)

	// create + sign tx
	// tx sends tokens to self
	tx, err := solana.NewTransaction(
		[]solana.Instruction{
			system.NewTransferInstruction(
				1,
				pubKey,
				pubKey,
			).Build(),
		},
		hash.Value.Blockhash,
		solana.TransactionPayer(pubKey),
	)
	assert.NoError(t, err)
	_, err = tx.Sign(
		func(key solana.PublicKey) *solana.PrivateKey {
			if pubKey.Equals(key) {
				return &privKey
			}
			return nil
		},
	)
	assert.NoError(t, err)

	// send 5 of the same transcation
	n := 5
	sigs := make([]solana.Signature, n)
	var wg sync.WaitGroup
	ctx := context.Background()
	wg.Add(5)
	for i := 0; i < n; i++ {
		go func(i int) {
			time.Sleep(time.Duration(rand.Intn(3)) * time.Second) // randomly submit txs
			sig, err := c.SendTx(ctx, tx)
			assert.NoError(t, errors.Wrapf(err, "try #%d", i))
			sigs[i] = sig
			wg.Done()
		}(i)
	}
	wg.Wait()

	// expect one single transaction hash
	for i := 1; i < n; i++ {
		assert.Equal(t, sigs[0], sigs[i])
	}

	// expect one sender has only sent one tx
	// original balance - current bal = 5000 lamports (tx fee)
	endBal, err := c.Balance(pubKey)
	assert.NoError(t, err)
	assert.Equal(t, initBal-endBal, uint64(5_000))
}
