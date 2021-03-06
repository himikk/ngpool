package service

import (
	log "github.com/inconshreveable/log15"
	"github.com/mitchellh/mapstructure"
	"strings"
)

type ShareChainConfig struct {
	Name         string  `json:"name"`
	PayoutMethod string  `json:"payout_method"`
	Fee          float64 `json:"fee"`
	AlgoName     string  `mapstructure:"algo" json:"algo"`
	Algo         *Algo   `mapstructure:"-" json:"-"`
}

var ShareChain = map[string]*ShareChainConfig{}

func SetupShareChains(rawConfig map[string]interface{}) {
	// TODO: Chain to array of maps, makes more sense
	for name, rawConfig := range rawConfig {
		var chain = ShareChainConfig{
			Name: strings.ToUpper(name),
		}

		err := mapstructure.Decode(rawConfig, &chain)
		if err != nil {
			panic(err)
		}
		chain.Algo = AlgoConfig[chain.AlgoName]
		if chain.Algo == nil {
			panic("Must specify sharechain algorithm")
		}
		log.Debug("Decoded share chain config", "chain", chain, "rawConfig", rawConfig)
		// TODO: Ensure supported PayoutMethod to avoid misconfiguration
		ShareChain[chain.Name] = &chain
	}
}
