package history

// Canonical EVM chain IDs used for swap RPC.
const (
	ChainEthereum = 1
	ChainBSC      = 56
	ChainBase     = 8453
	ChainArbitrum = 42161
)

// RPCEnv is the environment variable that holds the RPC URL for a chain.
func RPCEnv(chainID int) string {
	switch chainID {
	case ChainEthereum:
		return "ETHEREUM_RPC_URL"
	case ChainBSC:
		return "BSC_RPC_URL"
	case ChainBase:
		return "BASE_RPC_URL"
	case ChainArbitrum:
		return "ARBITRUM_RPC_URL"
	default:
		return "RPC_URL"
	}
}

// NetworkLabel is the UI name for a chain.
func NetworkLabel(chainID int) string {
	switch chainID {
	case ChainEthereum:
		return "Ethereum"
	case ChainBSC:
		return "BNB Chain"
	case ChainBase:
		return "Base"
	case ChainArbitrum:
		return "Arbitrum"
	default:
		return ChainName(chainID)
	}
}

// ChainName is a short label for logs and errors.
func ChainName(chainID int) string {
	switch chainID {
	case ChainEthereum:
		return "ethereum"
	case ChainBSC:
		return "bsc"
	case ChainBase:
		return "base"
	case ChainArbitrum:
		return "arbitrum"
	default:
		return "unknown"
	}
}
