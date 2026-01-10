package x402pay

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-guide/go-x402-facilitator/pkg/client"
	"github.com/agent-guide/go-x402-facilitator/pkg/types"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(&X402BuyerMiddleware{})
}

// X402BuyerMiddleware is a Caddy HTTP middleware that intercepts 402 Payment Required
// responses from upstream handlers and automatically creates and submits payment.
type X402BuyerMiddleware struct {
	// Payment configuration
	PrivateKeyHex string `json:"private_key,omitempty"`
	MaxAmountPay  string `json:"max_amount_pay,omitempty"`
	MaxRetries    int    `json:"max_retries,omitempty"`

	// Runtime fields
	privateKey         *ecdsa.PrivateKey
	parsedMaxAmountPay int64
	ctx                caddy.Context
}

// CaddyModule returns the Caddy module information.
func (X402BuyerMiddleware) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.x402buyer",
		New: func() caddy.Module { return new(X402BuyerMiddleware) },
	}
}

// Provision sets up the middleware.
func (m *X402BuyerMiddleware) Provision(ctx caddy.Context) error {
	m.ctx = ctx

	if m.PrivateKeyHex == "" {
		return fmt.Errorf("buyer private key is indispensable")
	}
	privateKey, err := crypto.HexToECDSA(m.PrivateKeyHex)
	if err != nil {
		return fmt.Errorf("invalid buyer private key: %w", err)
	}
	m.privateKey = privateKey

	// Parse max_amount_pay if specified
	if m.MaxAmountPay == "" {
		m.MaxAmountPay = "1000000"
	}
	maxAmount, err := strconv.ParseInt(m.MaxAmountPay, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid max_amount_pay: %w", err)
	}
	m.parsedMaxAmountPay = maxAmount

	// Set default max retries
	if m.MaxRetries == 0 {
		m.MaxRetries = 1
	}

	ctx.Logger(m).Info("provisioning x402 buyer middleware",
		zap.Int("max_retries", m.MaxRetries),
		zap.Int64("max_amount_pay", m.parsedMaxAmountPay),
		zap.String("buyer_private_key_set", fmt.Sprintf("%t", m.PrivateKeyHex != "")),
	)

	return nil
}

// Validate validates the middleware configuration.
func (m *X402BuyerMiddleware) Validate() error {
	if m.privateKey == nil {
		return fmt.Errorf("buyer private key is required")
	}
	return nil
}

// ServeHTTP implements the caddyhttp.MiddlewareHandler interface.
func (m *X402BuyerMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// Use response capture to intercept the response
	rec := &responseCapture{ResponseWriter: w, statusCode: http.StatusOK}

	// Call next handler
	err := next.ServeHTTP(rec, r)
	if err != nil {
		return m.flushResponse(rec, w)
	}

	// Check if the response is 402 Payment Required
	if rec.statusCode != http.StatusPaymentRequired {
		return m.flushResponse(rec, w)
	}

	m.ctx.Logger(m).Info("received 402 Payment Required, attempting automatic payment")

	// Parse payment requirements from response body
	var paymentResp paymentRequiredResponse
	if err := json.Unmarshal(rec.body.Bytes(), &paymentResp); err != nil {
		m.ctx.Logger(m).Error("failed to parse 402 response",
			zap.Error(err),
		)
		// Return the original 402 response
		return m.flushResponse(rec, w)
	}

	// Check if max amount is specified and validate
	if m.parsedMaxAmountPay > 0 {
		requiredAmount, err := strconv.ParseInt(paymentResp.PaymentRequirements.MaxAmountRequired, 10, 64)
		if err != nil {
			m.ctx.Logger(m).Error("failed to parse max_amount_required from payment requirements",
				zap.Error(err),
			)
			return m.writeError(w, http.StatusBadRequest, "invalid_payment_requirements", "Invalid max_amount_required in payment requirements")
		}

		if requiredAmount > m.parsedMaxAmountPay {
			m.ctx.Logger(m).Warn("required payment amount exceeds max_amount_pay",
				zap.Int64("required", requiredAmount),
				zap.Int64("max_allowed", m.parsedMaxAmountPay),
			)
			return m.writeError(w, http.StatusPaymentRequired, "amount_limit_exceeded",
				fmt.Sprintf("Required payment amount %d exceeds max allowed amount %d", requiredAmount, m.parsedMaxAmountPay))
		}
	}

	// Create payment payload
	paymentPayload, err := m.createPaymentPayload(&paymentResp.PaymentRequirements)
	if err != nil {
		m.ctx.Logger(m).Error("failed to create payment payload",
			zap.Error(err),
		)
		return m.writeError(w, http.StatusInternalServerError, "payment_creation_failed",
			fmt.Sprintf("Failed to create payment: %s", err.Error()))
	}

	// Serialize payment payload to JSON
	paymentJSON, err := json.Marshal(paymentPayload)
	if err != nil {
		m.ctx.Logger(m).Error("failed to marshal payment payload",
			zap.Error(err),
		)
		return m.writeError(w, http.StatusInternalServerError, "payment_serialization_failed",
			fmt.Sprintf("Failed to serialize payment: %s", err.Error()))
	}

	m.ctx.Logger(m).Info("payment payload created, retrying request with payment")

	// Create a new request with X-Payment header
	var bodyReader io.Reader
	if r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		// Restore original body for retry
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	retryReq, err := http.NewRequestWithContext(
		r.Context(),
		r.Method,
		r.URL.String(),
		bodyReader,
	)
	if err != nil {
		m.ctx.Logger(m).Error("failed to create retry request",
			zap.Error(err),
		)
		return m.writeError(w, http.StatusInternalServerError, "retry_request_failed",
			fmt.Sprintf("Failed to create retry request: %s", err.Error()))
	}

	// Copy headers
	for k, v := range r.Header {
		retryReq.Header[k] = v
	}

	// Add X-Payment header
	retryReq.Header.Set("X-Payment", string(paymentJSON))

	return next.ServeHTTP(w, retryReq)
}

// writeError writes an error response to the writer.
func (m *X402BuyerMiddleware) writeError(w http.ResponseWriter, status int, errType, message string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(types.ErrorResponse{
		Error:   errType,
		Message: message,
		Code:    status,
	})
}

// createPaymentPayload creates a payment payload using the configured private key.
func (m *X402BuyerMiddleware) createPaymentPayload(requirements *types.PaymentRequirements) (*types.PaymentPayload, error) {
	// Generate payment payload
	var validDuration int64 = 300
	now := time.Now().Unix()
	validAfter := now - 600000
	validBefore := now + validDuration
	walletAddress := crypto.PubkeyToAddress(m.privateKey.PublicKey)

	// Generate nonce
	nonce := fmt.Sprintf(
		"0x%x",
		crypto.Keccak256Hash([]byte(fmt.Sprintf("%d-%s-%s", now, walletAddress.Hex(), requirements.PayTo))).Hex(),
	)

	return client.CreatePaymentPayload(
		requirements,
		m.privateKey,
		validAfter,
		validBefore,
		uint64(1337),
		nonce,
	)
}

// flushResponse writes the captured response to the actual writer.
func (m *X402BuyerMiddleware) flushResponse(rec *responseCapture, w http.ResponseWriter) error {
	// Copy headers
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}

	// Write status code
	w.WriteHeader(rec.statusCode)

	// Write body
	if _, err := w.Write(rec.body.Bytes()); err != nil {
		return fmt.Errorf("failed to write response body: %w", err)
	}

	return nil
}

// responseCapture is a response writer that captures the response status code and body
// without writing to the underlying writer until flushResponse is called.
type responseCapture struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
	written    bool
	headers    http.Header
}

// WriteHeader captures the status code without writing to the underlying writer.
func (r *responseCapture) WriteHeader(statusCode int) {
	if !r.written {
		r.statusCode = statusCode
		r.written = true
	}
	// Don't write to underlying writer - wait for flushResponse
}

// Write captures the body without writing to the underlying writer.
func (r *responseCapture) Write(data []byte) (int, error) {
	if !r.written {
		r.statusCode = http.StatusOK
		r.written = true
	}
	return r.body.Write(data)
}

// Header returns the headers for this response.
func (r *responseCapture) Header() http.Header {
	if r.headers == nil {
		r.headers = make(http.Header)
		// Copy existing headers from underlying writer
		for k, v := range r.ResponseWriter.Header() {
			r.headers[k] = v
		}
	}
	return r.headers
}

// paymentRequiredResponse represents the 402 Payment Required response.
type paymentRequiredResponse struct {
	Error               string                    `json:"error"`
	Message             string                    `json:"message"`
	Code                int                       `json:"code"`
	PaymentRequirements types.PaymentRequirements `json:"paymentRequirements"`
}

// Interface guards
var (
	_ caddy.Provisioner           = (*X402BuyerMiddleware)(nil)
	_ caddy.Validator             = (*X402BuyerMiddleware)(nil)
	_ caddyhttp.MiddlewareHandler = (*X402BuyerMiddleware)(nil)
	_ caddyfile.Unmarshaler       = (*X402BuyerMiddleware)(nil)
)
