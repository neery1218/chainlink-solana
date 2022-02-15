package monitoring

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var BalanceAccountNames = []string{
	"contract",
	"state",
	"transmissions",
	"token_vault",
	"requester_access_controller",
	"billing_access_controller",
}

var labelNames = []string{
	// This is the address of the account associated with one of the account names above.
	"account_address",
	"feed_id",
	"chain_id",
	"contract_status",
	"contract_type",
	"feed_name",
	"feed_path",
	"network_id",
	"network_name",
}

var gauges map[string]*prometheus.GaugeVec

func init() {
	gauges = map[string]*prometheus.GaugeVec{}
	for _, name := range BalanceAccountNames {
		gauges[name] = promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: fmt.Sprintf("sol_balance_%s", name),
			},
			labelNames,
		)
	}
}

type Metrics interface {
	SetBalance(balance uint64, balanceAccountName, accountAddress, feedID, chainID, contractStatus, contractType, feedName, feedPath, networkID, networkName string)
	Cleanup(accountAddress, feedID, chainID, contractStatus, contractType, feedName, feedPath, networkID, networkName string)
}

type defaultMetrics struct{}

var DefaultMetrics = &defaultMetrics{}

func (d *defaultMetrics) SetBalance(balance uint64, balanceAccountName, accountAddress, feedID, chainID, contractStatus, contractType, feedName, feedPath, networkID, networkName string) {
	gauge, found := gauges[balanceAccountName]
	if !found {
		panic(fmt.Sprintf("gauge not known for name '%s'", balanceAccountName))
	}
	gauge.WithLabelValues(accountAddress, feedID, chainID, contractStatus, contractType, feedName, feedPath, networkID, networkName).Set(float64(balance))
}

func (d *defaultMetrics) Cleanup(accountAddress, feedID, chainID, contractStatus, contractType, feedName, feedPath, networkID, networkName string) {
	for _, name := range BalanceAccountNames {
		_ = gauges[name].DeleteLabelValues(accountAddress, feedID, chainID, contractStatus, contractType, feedName, feedPath, networkID, networkName)
	}
}
