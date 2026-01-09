package x402pay

import (
	"fmt"

	"github.com/agent-guide/go-x402-facilitator/pkg/facilitator"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(&X402FacilitatorApp{})
}

// X402FacilitatorApp is an app-level module that provides facilitator services.
// It runs an HTTP server with verify and settle endpoints.
type X402FacilitatorApp struct {
	// Facilitator configuration
	PrivateKey       string               `json:"private_key,omitempty"`
	SupportedSchemes []string             `json:"supported_schemes,omitempty"`
	GasLimit         uint64               `json:"gas_limit,omitempty"`
	GasPrice         uint64               `json:"gas_price,omitempty"`
	ChainNetworks    []ChainNetworkConfig `json:"chain_networks,omitempty"`

	// Runtime fields
	facilitator facilitator.PaymentFacilitator
}

// ChainNetworkConfig represents a blockchain network configuration.
type ChainNetworkConfig struct {
	Name          string `json:"name,omitempty"`
	RPC           string `json:"rpc,omitempty"`
	ID            uint64 `json:"id,omitempty"`
	TokenAddress  string `json:"token_address,omitempty"`
	TokenName     string `json:"token_name,omitempty"`
	TokenVersion  string `json:"token_version,omitempty"`
	TokenDecimals int64  `json:"token_decimals,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
}

// CaddyModule returns the Caddy module information.
func (X402FacilitatorApp) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "x402.facilitator",
		New: func() caddy.Module { return new(X402FacilitatorApp) },
	}
}

// Provision sets up the module.
func (m *X402FacilitatorApp) Provision(ctx caddy.Context) error {
	ctx.Logger(m).Info("provisioning x402 facilitator app",
		zap.String("private_key_set", fmt.Sprintf("%t", m.PrivateKey != "")),
		zap.Int("chain_networks_count", len(m.ChainNetworks)),
	)
	return nil
}

// Validate validates the module configuration.
func (m *X402FacilitatorApp) Validate() error {
	if m.PrivateKey == "" {
		return fmt.Errorf("private_key is required")
	}
	if len(m.ChainNetworks) == 0 {
		return fmt.Errorf("at least one chain_network is required")
	}
	return nil
}

// Start starts the application.
func (m *X402FacilitatorApp) Start() error {
	// Initialize facilitator
	if err := m.initFacilitator(); err != nil {
		return fmt.Errorf("failed to initialize facilitator: %w", err)
	}

	return nil
}

// Stop stops the application.
func (m *X402FacilitatorApp) Stop() error {
	// Close facilitator
	if m.facilitator != nil {
		m.facilitator.Close()
	}

	return nil
}

// Name returns the name of the app.
func (X402FacilitatorApp) Name() string {
	return "x402.facilitator"
}

// GetFacilitator returns the facilitator instance.
// This allows middleware to access the facilitator for payment verification.
func (m *X402FacilitatorApp) GetFacilitator() facilitator.PaymentFacilitator {
	return m.facilitator
}

// initFacilitator initializes the X402 facilitator instance.
func (m *X402FacilitatorApp) initFacilitator() error {
	// Build networks map from configuration
	networks := make(map[string]facilitator.NetworkConfig)
	for _, chainNetwork := range m.ChainNetworks {
		networks[chainNetwork.Name] = facilitator.NetworkConfig{
			ChainRPC:      chainNetwork.RPC,
			ChainID:       chainNetwork.ID,
			TokenAddress:  chainNetwork.TokenAddress,
			TokenName:     chainNetwork.TokenName,
			TokenVersion:  chainNetwork.TokenVersion,
			TokenDecimals: chainNetwork.TokenDecimals,
			TokenType:     chainNetwork.TokenType,
		}
	}

	// Create facilitator config
	facilitatorConfig := &facilitator.FacilitatorConfig{
		Networks:         networks,
		PrivateKey:       m.PrivateKey,
		SupportedSchemes: []string{"exact"},
	}

	// Create facilitator instance
	f, err := facilitator.New(facilitatorConfig)
	if err != nil {
		return fmt.Errorf("failed to create facilitator: %w", err)
	}

	m.facilitator = f
	return nil
}

// Interface guards
var (
	_ caddy.Provisioner     = (*X402FacilitatorApp)(nil)
	_ caddy.Validator       = (*X402FacilitatorApp)(nil)
	_ caddy.App             = (*X402FacilitatorApp)(nil)
	_ caddyfile.Unmarshaler = (*X402FacilitatorApp)(nil)
)
