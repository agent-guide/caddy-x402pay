package x402pay

import (
	"fmt"

	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	httpcaddyfile.RegisterGlobalOption("chain_network", parseChainNetworkGlobal)
	httpcaddyfile.RegisterGlobalOption("x402.facilitator", parseX402Facilitator)
	httpcaddyfile.RegisterHandlerDirective("x402seller", parseX402Seller)
	httpcaddyfile.RegisterHandlerDirective("x402buyer", parseX402Buyer)
}

// Global storage for chain networks parsed from Caddyfile
// This is a simple approach to accumulate chain_network configurations
var globalChainNetworks []ChainNetworkConfig

// parseChainNetworkGlobal parses a global chain_network option.
// Syntax: chain_network <name> { ... }
func parseChainNetworkGlobal(d *caddyfile.Dispenser, _ any) (any, error) {
	if !d.Next() {
		return nil, d.Err("expected directive name")
	}

	// Get network name
	if !d.NextArg() {
		return nil, d.ArgErr()
	}
	networkName := d.Val()

	networkConfig := ChainNetworkConfig{Name: networkName}
	if err := parseChainNetwork(d, &networkConfig); err != nil {
		return nil, err
	}

	// Append to global storage
	globalChainNetworks = append(globalChainNetworks, networkConfig)

	// Return empty config - the networks will be picked up by x402.facilitator
	return nil, nil
}

// parseX402Facilitator parses the x402.facilitator app configuration.
// Syntax: x402.facilitator { ... }
// Returns the app configuration that will be merged into the global config.
func parseX402Facilitator(d *caddyfile.Dispenser, _ any) (any, error) {
	app := &X402FacilitatorApp{}

	// Import global chain networks
	if len(globalChainNetworks) > 0 {
		app.ChainNetworks = make([]ChainNetworkConfig, len(globalChainNetworks))
		copy(app.ChainNetworks, globalChainNetworks)
	}

	if err := app.UnmarshalCaddyfile(d); err != nil {
		return nil, err
	}

	// Debug: verify configuration was parsed
	if app.PrivateKey == "" {
		return nil, fmt.Errorf("private_key was not parsed from Caddyfile")
	}

	return httpcaddyfile.App{
		Name:  "x402.facilitator",
		Value: caddyconfig.JSON(app, nil),
	}, nil
}

// parseX402Seller parses the x402seller handler directive.
func parseX402Seller(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m X402SellerMiddleware
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return &m, err
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler for X402FacilitatorApp. Syntax:
//
//	x402.facilitator {
//	    private_key 0x...
//	    supported_schemes exact
//	    gas_limit 21000
//	    gas_price 10
//	}
func (m *X402FacilitatorApp) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	// When called from RegisterGlobalOption, the Dispenser is already positioned
	// at the directive name "x402.facilitator". We need to consume it first.
	if !d.Next() {
		return d.Err("expected directive name")
	}

	// Check for arguments (should be none)
	if d.NextArg() {
		return d.ArgErr()
	}

	// Parse the block content
	for d.NextBlock(0) {
		switch d.Val() {
		case "private_key":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.PrivateKey = d.Val()
			// Debug: ensure private_key is set
			if m.PrivateKey == "" {
				return d.Errf("private_key is empty after parsing")
			}

		case "supported_schemes":
			args := d.RemainingArgs()
			if len(args) == 0 {
				return d.ArgErr()
			}
			m.SupportedSchemes = args

		case "gas_limit":
			if !d.NextArg() {
				return d.ArgErr()
			}
			var gasLimit uint64
			if _, err := fmt.Sscanf(d.Val(), "%d", &gasLimit); err != nil {
				return d.Errf("invalid gas_limit: %v", err)
			}
			m.GasLimit = gasLimit

		case "gas_price":
			if !d.NextArg() {
				return d.ArgErr()
			}
			var gasPrice uint64
			if _, err := fmt.Sscanf(d.Val(), "%d", &gasPrice); err != nil {
				return d.Errf("invalid gas_price: %v", err)
			}
			m.GasPrice = gasPrice

		default:
			return d.Errf("unknown subdirective: %s", d.Val())
		}
	}

	return nil
}

// parseChainNetwork parses a chain_network block.
func parseChainNetwork(d *caddyfile.Dispenser, config *ChainNetworkConfig) error {
	for nesting := d.Nesting(); d.NextBlock(nesting); {
		switch d.Val() {
		case "rpc":
			if !d.NextArg() {
				return d.ArgErr()
			}
			config.RPC = d.Val()

		case "id":
			if !d.NextArg() {
				return d.ArgErr()
			}
			var chainID uint64
			if _, err := fmt.Sscanf(d.Val(), "%d", &chainID); err != nil {
				return d.Errf("invalid chain id: %v", err)
			}
			config.ID = chainID

		case "token_address":
			if !d.NextArg() {
				return d.ArgErr()
			}
			config.TokenAddress = d.Val()

		case "token_name":
			if !d.NextArg() {
				return d.ArgErr()
			}
			config.TokenName = d.Val()

		case "token_version":
			if !d.NextArg() {
				return d.ArgErr()
			}
			config.TokenVersion = d.Val()

		case "token_decimals":
			if !d.NextArg() {
				return d.ArgErr()
			}
			var decimals int64
			if _, err := fmt.Sscanf(d.Val(), "%d", &decimals); err != nil {
				return d.Errf("invalid token_decimals: %v", err)
			}
			config.TokenDecimals = decimals

		case "token_type":
			if !d.NextArg() {
				return d.ArgErr()
			}
			config.TokenType = d.Val()

		default:
			return d.Errf("unknown chain_network subdirective: %s", d.Val())
		}
	}
	return nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler for X402SellerMiddleware. Syntax:
//
//	x402seller [<pattern>] {
//	    scheme exact
//	    network localhost
//	    resource premium-data-api
//	    description "Access to premium market data"
//	    max_amount_required 1000000
//	    pay_to 0x93866dBB587db8b9f2C36570Ae083E3F9814e508
//	}
func (m *X402SellerMiddleware) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume directive name
	if d.NextArg() {
		return d.ArgErr()
	}

	for d.NextBlock(0) {
		switch d.Val() {
		case "scheme":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.Scheme = d.Val()

		case "network":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.Network = d.Val()

		case "resource":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.Resource = d.Val()

		case "description":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.Description = d.Val()

		case "max_amount_required":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.MaxAmountRequired = d.Val()

		case "pay_to":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.PayTo = d.Val()

		default:
			return d.Errf("unknown subdirective: %s", d.Val())
		}
	}

	return nil
}

// parseX402Buyer parses the x402buyer handler directive.
func parseX402Buyer(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m X402BuyerMiddleware
	// Import global chain networks
	if len(globalChainNetworks) > 0 {
		m.ChainNetworks = make([]ChainNetworkConfig, len(globalChainNetworks))
		copy(m.ChainNetworks, globalChainNetworks)
	}
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return &m, err
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler for X402BuyerMiddleware. Syntax:
//
//	x402buyer {
//	    private_key {$X402_BUYER_PRIVATE_KEY}
//	    max_amount_pay 2000000
//	    max_retries 1
//	}
func (m *X402BuyerMiddleware) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume directive name
	if d.NextArg() {
		return d.ArgErr()
	}

	for d.NextBlock(0) {
		switch d.Val() {
		case "private_key":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.PrivateKeyHex = d.Val()

		case "max_amount_pay":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.MaxAmountPay = d.Val()

		case "max_retries":
			if !d.NextArg() {
				return d.ArgErr()
			}
			var maxRetries int
			if _, err := fmt.Sscanf(d.Val(), "%d", &maxRetries); err != nil {
				return d.Errf("invalid max_retries: %v", err)
			}
			m.MaxRetries = maxRetries

		default:
			return d.Errf("unknown subdirective: %s", d.Val())
		}
	}

	return nil
}
