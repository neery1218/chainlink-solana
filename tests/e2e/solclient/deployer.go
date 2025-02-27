package solclient

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/smartcontractkit/integrations-framework/blockchain"

	ag_binary "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	associatedtokenaccount "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/rs/zerolog/log"
	access_controller2 "github.com/smartcontractkit/chainlink-solana/contracts/generated/access_controller"
	ocr_2 "github.com/smartcontractkit/chainlink-solana/contracts/generated/ocr2"
	store2 "github.com/smartcontractkit/chainlink-solana/contracts/generated/store"
	"github.com/smartcontractkit/helmenv/environment"
	"golang.org/x/sync/errgroup"
)

// All account sizes are calculated from Rust structures, ex. programs/access-controller/src/lib.rs:L80
// there is some wrapper in "anchor" that creates accounts for programs automatically, but we are doing that explicitly
const (
	Discriminator = 8
	// TokenMintAccountSize default size of data required for a new mint account
	TokenMintAccountSize             = uint64(82)
	TokenAccountSize                 = uint64(165)
	AccessControllerStateAccountSize = uint64(Discriminator + solana.PublicKeyLength + solana.PublicKeyLength + 8 + 32*64)
	StoreAccountSize                 = uint64(Discriminator + solana.PublicKeyLength*3)
	OCRTransmissionsAccountSize      = uint64(Discriminator + 192 + 8192*48)
	OCRProposalAccountSize           = Discriminator + 1 + 32 + 1 + 1 + (1 + 4) + 32 + ProposedOraclesSize + OCROffChainConfigSize
	ProposedOracleSize               = uint64(solana.PublicKeyLength + 20 + 4 + solana.PublicKeyLength)
	ProposedOraclesSize              = ProposedOracleSize*19 + 8
	OCROracle                        = uint64(solana.PublicKeyLength + 20 + solana.PublicKeyLength + solana.PublicKeyLength + 4 + 8)
	OCROraclesSize                   = OCROracle*19 + 8
	OCROffChainConfigSize            = uint64(8 + 4096 + 8)
	OCRConfigSize                    = 32 + 32 + 32 + 32 + 32 + 32 + 16 + 16 + (1 + 1 + 2 + 4 + 4 + 32) + (4 + 32 + 8) + (4 + 4)
	OCRAccountSize                   = Discriminator + 1 + 1 + 2 + 4 + solana.PublicKeyLength + OCRConfigSize + OCROffChainConfigSize + OCROraclesSize
)

type Authority struct {
	PublicKey solana.PublicKey
	Nonce     uint8
}

type ContractDeployer struct {
	Client   *Client
	Accounts *Accounts
	Env      *environment.Environment
}

// GenerateAuthorities generates authorities so other contracts can access OCR with on-chain calls when signer needed
func (c *ContractDeployer) GenerateAuthorities(seeds []string) error {
	authorities := make(map[string]*Authority)
	for _, seed := range seeds {
		auth, nonce, err := c.Client.FindAuthorityAddress(seed, c.Accounts.OCR.PublicKey(), c.Client.ProgramWallets["ocr2-keypair.json"].PublicKey())
		if err != nil {
			return err
		}
		authorities[seed] = &Authority{
			PublicKey: auth,
			Nonce:     nonce,
		}
	}
	c.Accounts.Authorities = authorities
	c.Accounts.Owner = c.Client.DefaultWallet
	return nil
}

// addMintInstr adds instruction for creating new mint (token)
func (c *ContractDeployer) addMintInstr(instr *[]solana.Instruction) error {
	accInstr, err := c.Client.CreateAccInstr(c.Accounts.Mint, TokenMintAccountSize, token.ProgramID)
	if err != nil {
		return err
	}
	*instr = append(
		*instr,
		accInstr,
		token.NewInitializeMintInstruction(
			18,
			c.Accounts.MintAuthority.PublicKey(),
			c.Accounts.MintAuthority.PublicKey(),
			c.Accounts.Mint.PublicKey(),
			solana.SysVarRentPubkey,
		).Build())
	return nil
}

// AddNewAssociatedAccInstr adds instruction to create new account associated with some mint (token)
func (c *ContractDeployer) AddNewAssociatedAccInstr(acc *solana.Wallet, ownerPubKey solana.PublicKey, instr *[]solana.Instruction) error {
	accInstr, err := c.Client.CreateAccInstr(acc, TokenAccountSize, token.ProgramID)
	if err != nil {
		return err
	}
	*instr = append(*instr,
		accInstr,
		token.NewInitializeAccountInstruction(
			acc.PublicKey(),
			c.Accounts.Mint.PublicKey(),
			ownerPubKey,
			solana.SysVarRentPubkey,
		).Build(),
		associatedtokenaccount.NewCreateInstruction(
			c.Client.DefaultWallet.PublicKey(),
			acc.PublicKey(),
			c.Accounts.Mint.PublicKey(),
		).Build(),
	)
	return nil
}

func (c *ContractDeployer) DeployOCRv2Store(billingAC string) (*Store, error) {
	programWallet := c.Client.ProgramWallets["store-keypair.json"]
	payer := c.Client.DefaultWallet
	accInstruction, err := c.Client.CreateAccInstr(c.Accounts.Store, StoreAccountSize, programWallet.PublicKey())
	if err != nil {
		return nil, err
	}
	bacPublicKey, err := solana.PublicKeyFromBase58(billingAC)
	if err != nil {
		return nil, err
	}
	err = c.Client.TXSync(
		"Deploy store",
		rpc.CommitmentConfirmed,
		[]solana.Instruction{
			accInstruction,
			store2.NewInitializeInstruction(
				c.Accounts.Store.PublicKey(),
				c.Accounts.Owner.PublicKey(),
				bacPublicKey,
			).Build(),
		},
		func(key solana.PublicKey) *solana.PrivateKey {
			if key.Equals(c.Accounts.Owner.PublicKey()) {
				return &c.Accounts.Owner.PrivateKey
			}
			if key.Equals(c.Accounts.Store.PublicKey()) {
				return &c.Accounts.Store.PrivateKey
			}
			if key.Equals(payer.PublicKey()) {
				return &payer.PrivateKey
			}
			return nil
		},
		payer.PublicKey(),
	)
	if err != nil {
		return nil, err
	}
	return &Store{
		Client:        c.Client,
		Store:         c.Accounts.Store,
		Feed:          c.Accounts.Feed,
		Owner:         c.Accounts.Owner,
		ProgramWallet: programWallet,
	}, nil
}

func (c *ContractDeployer) CreateFeed(desc string, decimals uint8, granularity int, liveLength int) error {
	payer := c.Client.DefaultWallet
	programWallet := c.Client.ProgramWallets["store-keypair.json"]
	feedAccInstruction, err := c.Client.CreateAccInstr(c.Accounts.Feed, OCRTransmissionsAccountSize, programWallet.PublicKey())
	if err != nil {
		return err
	}
	err = c.Client.TXSync(
		"Create feed",
		rpc.CommitmentFinalized,
		[]solana.Instruction{
			feedAccInstruction,
			store2.NewCreateFeedInstruction(
				desc,
				decimals,
				uint8(granularity),
				uint32(liveLength),
				c.Accounts.Feed.PublicKey(),
				c.Accounts.Owner.PublicKey(),
			).Build(),
		},
		func(key solana.PublicKey) *solana.PrivateKey {
			if key.Equals(c.Accounts.Owner.PublicKey()) {
				return &c.Accounts.Owner.PrivateKey
			}
			if key.Equals(c.Accounts.Feed.PublicKey()) {
				return &c.Accounts.Feed.PrivateKey
			}
			if key.Equals(payer.PublicKey()) {
				return &payer.PrivateKey
			}
			return nil
		},
		payer.PublicKey(),
	)
	if err != nil {
		return err
	}
	return nil
}

func (c *ContractDeployer) addMintToAccInstr(instr *[]solana.Instruction, dest *solana.Wallet, amount uint64) error {
	*instr = append(*instr, token.NewMintToInstruction(
		amount,
		c.Accounts.Mint.PublicKey(),
		dest.PublicKey(),
		c.Accounts.MintAuthority.PublicKey(),
		nil,
	).Build())
	return nil
}

func (c *ContractDeployer) DeployLinkTokenContract() (*LinkToken, error) {
	var err error
	payer := c.Client.DefaultWallet

	instr := make([]solana.Instruction, 0)
	if err = c.addMintInstr(&instr); err != nil {
		return nil, err
	}
	err = c.Client.TXAsync(
		"Createing LINK Token and associated accounts",
		instr,
		func(key solana.PublicKey) *solana.PrivateKey {
			if key.Equals(c.Accounts.OCRVault.PublicKey()) {
				return &c.Accounts.OCRVault.PrivateKey
			}
			if key.Equals(c.Accounts.Mint.PublicKey()) {
				return &c.Accounts.Mint.PrivateKey
			}
			if key.Equals(payer.PublicKey()) {
				return &payer.PrivateKey
			}
			if key.Equals(c.Accounts.MintAuthority.PublicKey()) {
				return &c.Accounts.MintAuthority.PrivateKey
			}
			return nil
		},
		payer.PublicKey(),
	)
	if err != nil {
		return nil, err
	}
	return &LinkToken{
		Client:        c.Client,
		Mint:          c.Accounts.Mint,
		MintAuthority: c.Accounts.MintAuthority,
	}, nil
}

func (c *ContractDeployer) InitOCR2(billingControllerAddr string, requesterControllerAddr string) (*OCRv2, error) {
	programWallet := c.Client.ProgramWallets["ocr2-keypair.json"]
	payer := c.Client.DefaultWallet
	ocrAccInstruction, err := c.Client.CreateAccInstr(c.Accounts.OCR, OCRAccountSize, programWallet.PublicKey())
	if err != nil {
		return nil, err
	}
	bacPubKey, err := solana.PublicKeyFromBase58(billingControllerAddr)
	if err != nil {
		return nil, err
	}
	racPubKey, err := solana.PublicKeyFromBase58(requesterControllerAddr)
	if err != nil {
		return nil, err
	}
	vault := c.Accounts.Authorities["vault"]
	vaultAssoc, _, err := solana.FindAssociatedTokenAddress(vault.PublicKey, c.Accounts.Mint.PublicKey())
	if err != nil {
		return nil, err
	}
	instr := make([]solana.Instruction, 0)
	if err = c.AddNewAssociatedAccInstr(c.Accounts.OCRVault, vault.PublicKey, &instr); err != nil {
		return nil, err
	}
	if err = c.addMintToAccInstr(&instr, c.Accounts.OCRVault, 1e8); err != nil {
		return nil, err
	}
	instr = append(instr, ocrAccInstruction)
	instr = append(instr, ocr_2.NewInitializeInstructionBuilder().
		SetMinAnswer(ag_binary.Int128{
			Lo: 1,
			Hi: 0,
		}).
		SetMaxAnswer(ag_binary.Int128{
			Lo: 1000000,
			Hi: 0,
		}).
		SetStateAccount(c.Accounts.OCR.PublicKey()).
		SetFeedAccount(c.Accounts.Feed.PublicKey()).
		SetPayerAccount(payer.PublicKey()).
		SetOwnerAccount(c.Accounts.Owner.PublicKey()).
		SetTokenMintAccount(c.Accounts.Mint.PublicKey()).
		SetTokenVaultAccount(vaultAssoc).
		SetVaultAuthorityAccount(vault.PublicKey).
		SetRequesterAccessControllerAccount(racPubKey).
		SetBillingAccessControllerAccount(bacPubKey).
		SetRentAccount(solana.SysVarRentPubkey).
		SetSystemProgramAccount(solana.SystemProgramID).
		SetTokenProgramAccount(solana.TokenProgramID).
		SetAssociatedTokenProgramAccount(solana.SPLAssociatedTokenAccountProgramID).
		Build())
	err = c.Client.TXSync(
		"Initializing OCRv2",
		rpc.CommitmentFinalized,
		instr,
		func(key solana.PublicKey) *solana.PrivateKey {
			if key.Equals(payer.PublicKey()) {
				return &payer.PrivateKey
			}
			if key.Equals(c.Accounts.OCR.PublicKey()) {
				return &c.Accounts.OCR.PrivateKey
			}
			if key.Equals(c.Accounts.Owner.PublicKey()) {
				return &c.Accounts.Owner.PrivateKey
			}
			if key.Equals(c.Accounts.OCRVault.PublicKey()) {
				return &c.Accounts.OCRVault.PrivateKey
			}
			if key.Equals(c.Accounts.Mint.PublicKey()) {
				return &c.Accounts.Mint.PrivateKey
			}
			if key.Equals(c.Accounts.MintAuthority.PublicKey()) {
				return &c.Accounts.MintAuthority.PrivateKey
			}
			return nil
		},
		payer.PublicKey(),
	)
	if err != nil {
		return nil, err
	}
	return &OCRv2{
		ContractDeployer:         c,
		Client:                   c.Client,
		State:                    c.Accounts.OCR,
		Authorities:              c.Accounts.Authorities,
		Owner:                    c.Accounts.Owner,
		Proposal:                 c.Accounts.Proposal,
		OCRVaultAssociatedPubKey: vaultAssoc,
		Mint:                     c.Accounts.Mint,
		ProgramWallet:            programWallet,
	}, nil
}

func (c *ContractDeployer) DeployProgramRemote(programName string) error {
	log.Debug().Str("Program", programName).Msg("Deploying program")
	connections := c.Env.Charts.Connections("solana-validator")
	cc, err := connections.Load("sol", "0", "sol-val")
	if err != nil {
		return err
	}
	chart := c.Env.Charts["solana-validator"]

	programPath := filepath.Join("programs", programName)
	programKeyFileName := strings.Replace(programName, ".so", "-keypair.json", -1)
	programKeyFilePath := filepath.Join("programs", programKeyFileName)
	cmd := fmt.Sprintf("solana deploy %s %s", programPath, programKeyFilePath)
	stdOutBytes, stdErrBytes, _ := chart.ExecuteInPod(cc.PodName, "sol-val", strings.Split(cmd, " "))
	log.Debug().Str("STDOUT", string(stdOutBytes)).Str("STDERR", string(stdErrBytes)).Str("CMD", cmd).Send()
	return nil
}

func (c *ContractDeployer) DeployOCRv2AccessController() (*AccessController, error) {
	programWallet := c.Client.ProgramWallets["access_controller-keypair.json"]
	payer := c.Client.DefaultWallet
	stateAcc := solana.NewWallet()
	accInstruction, err := c.Client.CreateAccInstr(stateAcc, AccessControllerStateAccountSize, programWallet.PublicKey())
	if err != nil {
		return nil, err
	}
	err = c.Client.TXAsync(
		"Initializing access controller",
		[]solana.Instruction{
			accInstruction,
			access_controller2.NewInitializeInstruction(
				stateAcc.PublicKey(),
				c.Accounts.Owner.PublicKey(),
			).Build(),
		},
		func(key solana.PublicKey) *solana.PrivateKey {
			if key.Equals(c.Accounts.Owner.PublicKey()) {
				return &c.Accounts.Owner.PrivateKey
			}
			if key.Equals(stateAcc.PublicKey()) {
				return &stateAcc.PrivateKey
			}
			if key.Equals(payer.PublicKey()) {
				return &payer.PrivateKey
			}
			return nil
		},
		payer.PublicKey(),
	)
	if err != nil {
		return nil, err
	}
	return &AccessController{
		State:         stateAcc,
		Client:        c.Client,
		Owner:         c.Accounts.Owner,
		ProgramWallet: programWallet,
	}, nil
}

func (c *ContractDeployer) RegisterAnchorPrograms() {
	access_controller2.SetProgramID(c.Client.ProgramWallets["access_controller-keypair.json"].PublicKey())
	store2.SetProgramID(c.Client.ProgramWallets["store-keypair.json"].PublicKey())
	ocr_2.SetProgramID(c.Client.ProgramWallets["ocr2-keypair.json"].PublicKey())
}

func (c *ContractDeployer) LoadPrograms(contractsDir string) error {
	keyFiles, err := c.Client.ListDirFilenamesByExt(contractsDir, ".json")
	if err != nil {
		return err
	}
	log.Debug().Interface("Files", keyFiles).Msg("Program key files")
	for _, kfn := range keyFiles {
		pk, err := solana.PrivateKeyFromSolanaKeygenFile(filepath.Join(contractsDir, kfn))
		if err != nil {
			return err
		}
		w, err := c.Client.LoadWallet(pk.String())
		if err != nil {
			return err
		}
		c.Client.ProgramWallets[kfn] = w
	}
	log.Debug().Interface("Keys", c.Client.ProgramWallets).Msg("Program wallets")
	return nil
}

func (c *ContractDeployer) DeployAnchorProgramsRemote(contractsDir string) error {
	contractBinaries, err := c.Client.ListDirFilenamesByExt(contractsDir, ".so")
	if err != nil {
		return err
	}
	log.Debug().Interface("Binaries", contractBinaries).Msg("Program binaries")
	g := errgroup.Group{}
	for _, bin := range contractBinaries {
		bin := bin
		g.Go(func() error {
			return c.DeployProgramRemote(bin)
		})
	}
	return g.Wait()
}

func (c *Client) FindAuthorityAddress(seed string, statePubKey solana.PublicKey, progPubKey solana.PublicKey) (solana.PublicKey, uint8, error) {
	log.Debug().
		Str("Seed", seed).
		Str("StatePubKey", statePubKey.String()).
		Str("ProgramPubKey", progPubKey.String()).
		Msg("Trying to find program authority")
	auth, nonce, err := solana.FindProgramAddress([][]byte{[]byte(seed), statePubKey.Bytes()}, progPubKey)
	if err != nil {
		return solana.PublicKey{}, 0, err
	}
	log.Debug().Str("Authority", auth.String()).Uint8("Nonce", nonce).Msg("Found authority addr")
	return auth, nonce, err
}

func NewContractDeployer(client blockchain.EVMClient, e *environment.Environment, lt *LinkToken) (*ContractDeployer, error) {
	cd := &ContractDeployer{
		Env: e,
		Accounts: &Accounts{
			OCR:           solana.NewWallet(),
			Store:         solana.NewWallet(),
			Feed:          solana.NewWallet(),
			Proposal:      solana.NewWallet(),
			Owner:         solana.NewWallet(),
			Mint:          solana.NewWallet(),
			MintAuthority: solana.NewWallet(),
			OCRVault:      solana.NewWallet(),
		},
		Client: client.(*Client),
	}
	if lt != nil {
		cd.Accounts.Mint = lt.Mint
		cd.Accounts.MintAuthority = lt.MintAuthority
	}
	return cd, nil
}
