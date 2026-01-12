package x402pay

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/agent-guide/go-x402-facilitator/pkg/types"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(&X402SellerMiddleware{})
}

// X402SellerMiddleware is a Caddy HTTP middleware that intercepts requests
// and requires X402 payment verification before allowing access to resources.
type X402SellerMiddleware struct {
	// Payment requirements configuration
	Scheme            string `json:"scheme,omitempty"`
	Network           string `json:"network,omitempty"`
	Resource          string `json:"resource,omitempty"`
	Description       string `json:"description,omitempty"`
	MaxAmountRequired string `json:"max_amount_required,omitempty"`
	PayTo             string `json:"pay_to,omitempty"`

	// Facilitator app reference
	facilitatorApp *X402FacilitatorApp
	ctx            caddy.Context
}

// CaddyModule returns the Caddy module information.
func (X402SellerMiddleware) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.x402seller",
		New: func() caddy.Module { return new(X402SellerMiddleware) },
	}
}

// Provision sets up the middleware.
func (m *X402SellerMiddleware) Provision(ctx caddy.Context) error {
	m.ctx = ctx

	// Get the X402FacilitatorApp instance
	appVal, err := ctx.App("x402.facilitator")
	if err != nil {
		return fmt.Errorf("failed to get x402.facilitator app: %w", err)
	}

	var ok bool
	m.facilitatorApp, ok = appVal.(*X402FacilitatorApp)
	if !ok {
		return fmt.Errorf("x402.facilitator app is not of type *X402FacilitatorApp")
	}

	ctx.Logger(m).Info("provisioning x402 seller middleware",
		zap.String("network", m.Network),
		zap.String("resource", m.Resource),
	)

	return nil
}

// Validate validates the middleware configuration.
func (m *X402SellerMiddleware) Validate() error {
	if m.Scheme == "" {
		return fmt.Errorf("scheme is required")
	}
	if m.Network == "" {
		return fmt.Errorf("network is required")
	}
	if m.Resource == "" {
		return fmt.Errorf("resource is required")
	}
	if m.PayTo == "" {
		return fmt.Errorf("pay_to is required")
	}
	if m.MaxAmountRequired == "" {
		return fmt.Errorf("max_amount_required is required")
	}
	return nil
}

// ServeHTTP implements the caddyhttp.MiddlewareHandler interface.
func (m *X402SellerMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// Check for X-Payment header
	paymentHeader := r.Header.Get("X-Payment")
	if paymentHeader == "" {
		// No payment provided, return 402 Payment Required
		if err := m.returnPaymentRequired(w); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(types.ErrorResponse{
				Error:   "payment_failed",
				Message: err.Error(),
				Code:    http.StatusInternalServerError,
			})
		}
		return nil
	}

	// Parse and validate payment
	if err := m.processPayment(paymentHeader); err != nil {
		m.ctx.Logger(m).Error("payment processing failed",
			zap.Error(err),
		)

		// Return payment required with error details
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(types.ErrorResponse{
			Error:   "payment_failed",
			Message: err.Error(),
			Code:    http.StatusPaymentRequired,
		})
		return nil
	}

	// Payment successful, continue to next handler
	return next.ServeHTTP(w, r)
}

// returnPaymentRequired returns a 402 Payment Required response with payment requirements.
func (m *X402SellerMiddleware) returnPaymentRequired(w http.ResponseWriter) error {
	facilitatorInstance := m.facilitatorApp.GetFacilitator()
	if facilitatorInstance == nil {
		return fmt.Errorf("facilitator is not initialized")
	}

	requirements, err := facilitatorInstance.CreatePaymentRequirements(m.Resource, m.Description, m.Network, m.PayTo, m.MaxAmountRequired)
	if err != nil {
		return fmt.Errorf("create payment requirements failed: %w", err)
	}

	w.Header().Set("X-Payment-Required", "true")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)

	return json.NewEncoder(w).Encode(map[string]interface{}{
		"error":               "payment_required",
		"message":             "Payment is required to access this resource",
		"code":                http.StatusPaymentRequired,
		"paymentRequirements": *requirements,
	})
}

// processPayment processes the X-Payment header and verifies/settles the payment.
func (m *X402SellerMiddleware) processPayment(paymentHeader string) error {
	// Get facilitator instance
	facilitatorInstance := m.facilitatorApp.GetFacilitator()
	if facilitatorInstance == nil {
		return fmt.Errorf("facilitator is not initialized")
	}

	// Parse X-Payment header (should be JSON)
	var paymentPayload types.PaymentPayload
	if err := json.Unmarshal([]byte(paymentHeader), &paymentPayload); err != nil {
		return fmt.Errorf("failed to parse X-Payment header: %w", err)
	}

	// Verify scheme and network match
	if paymentPayload.Scheme != m.Scheme || paymentPayload.Network != m.Network {
		return fmt.Errorf("payment scheme/network mismatch: expected scheme=%s network=%s, got scheme=%s network=%s",
			m.Scheme, m.Network, paymentPayload.Scheme, paymentPayload.Network)
	}

	requirements, err := facilitatorInstance.CreatePaymentRequirements(m.Resource, m.Description, m.Network, m.PayTo, m.MaxAmountRequired)
	if err != nil {
		return fmt.Errorf("create payment requirements failed: %w", err)
	}

	// Create verify request
	verifyReq := types.VerifyRequest{
		PaymentPayload:      paymentPayload,
		PaymentRequirements: *requirements,
	}

	// Verify payment
	verifyResp, err := facilitatorInstance.Verify(m.ctx, &verifyReq)
	if err != nil {
		return fmt.Errorf("payment verification failed: %w", err)
	}

	if !verifyResp.IsValid {
		return fmt.Errorf("payment is invalid: %s", verifyResp.InvalidReason)
	}

	// Settle payment
	settleResp, err := facilitatorInstance.Settle(m.ctx, &verifyReq)
	if err != nil {
		return fmt.Errorf("payment settlement failed: %w", err)
	}

	if !settleResp.Success {
		return fmt.Errorf("payment settlement failed: %s", settleResp.ErrorReason)
	}

	m.ctx.Logger(m).Info("payment processed successfully",
		zap.String("resource", m.Resource),
		zap.String("payer", settleResp.Payer),
		zap.String("transaction", settleResp.Transaction),
	)

	return nil
}

// Interface guards
var (
	_ caddy.Provisioner           = (*X402SellerMiddleware)(nil)
	_ caddy.Validator             = (*X402SellerMiddleware)(nil)
	_ caddyhttp.MiddlewareHandler = (*X402SellerMiddleware)(nil)
	_ caddyfile.Unmarshaler       = (*X402SellerMiddleware)(nil)
)
