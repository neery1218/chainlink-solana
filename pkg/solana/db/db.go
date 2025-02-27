package db

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"gopkg.in/guregu/null.v4"

	"github.com/smartcontractkit/chainlink/core/services/pg"
	"github.com/smartcontractkit/chainlink/core/store/models"
)

// ORM manages solana chains and nodes.
type ORM interface {
	Chain(string, ...pg.QOpt) (Chain, error)
	Chains(offset, limit int, qopts ...pg.QOpt) ([]Chain, int, error)
	CreateChain(id string, config ChainCfg, qopts ...pg.QOpt) (Chain, error)
	UpdateChain(id string, enabled bool, config ChainCfg, qopts ...pg.QOpt) (Chain, error)
	DeleteChain(id string, qopts ...pg.QOpt) error
	EnabledChains(...pg.QOpt) ([]Chain, error)

	CreateNode(NewNode, ...pg.QOpt) (Node, error)
	DeleteNode(int32, ...pg.QOpt) error
	Node(int32, ...pg.QOpt) (Node, error)
	NodeNamed(string, ...pg.QOpt) (Node, error)
	Nodes(offset, limit int, qopts ...pg.QOpt) (nodes []Node, count int, err error)
	NodesForChain(chainID string, offset, limit int, qopts ...pg.QOpt) (nodes []Node, count int, err error)
}

type Chain struct {
	ID        string
	Cfg       ChainCfg
	CreatedAt time.Time
	UpdatedAt time.Time
	Enabled   bool
}

type NewNode struct {
	Name          string `json:"name"`
	SolanaChainID string `json:"solanaChainId" db:"solana_chain_id"`
	SolanaURL     string `json:"solanaURL" db:"solana_url"`
}

type Node struct {
	ID            int32
	Name          string
	SolanaChainID string `json:"solanaChainId" db:"solana_chain_id"`
	SolanaURL     string `json:"solanaURL" db:"solana_url"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type ChainCfg struct {
	BalancePollPeriod   *models.Duration
	ConfirmPollPeriod   *models.Duration
	OCR2CachePollPeriod *models.Duration
	OCR2CacheTTL        *models.Duration
	TxTimeout           *models.Duration
	SkipPreflight       null.Bool // to enable or disable preflight checks
	Commitment          null.String
}

func (c *ChainCfg) Scan(value interface{}) error {
	b, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}

	return json.Unmarshal(b, c)
}

func (c ChainCfg) Value() (driver.Value, error) {
	return json.Marshal(c)
}
