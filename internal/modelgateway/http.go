package modelgateway

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
)

// Handler must be served over authenticated TLS. Registration additionally
// requires its separate operator transport credential AND an execution permit.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := g.Ready(r.Context()); err != nil {
			writeFailure(w, err)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /internal/pin", func(w http.ResponseWriter, r *http.Request) {
		if !g.issuerAuthenticated(r) {
			writeFailure(w, fail(ReasonUnauthorized, 401, nil))
			return
		}
		var in PinRequest
		if err := g.readRequest(w, r, &in); err != nil {
			writeFailure(w, err)
			return
		}
		out, err := g.PinCredentialSource(r.Context(), in)
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /internal/register", func(w http.ResponseWriter, r *http.Request) {
		if !g.issuerAuthenticated(r) {
			writeFailure(w, fail(ReasonUnauthorized, 401, nil))
			return
		}
		var in RegistrationRequest
		if err := g.readRequest(w, r, &in); err != nil {
			writeFailure(w, err)
			return
		}
		in.ExecutionToken = cellncapability.NewToken(r.Header.Get("X-Celln-Execution-Permit"))
		if err := g.Register(r.Context(), in); err != nil {
			writeFailure(w, err)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /v1/invoke", func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token.Empty() {
			writeFailure(w, fail(ReasonUnauthorized, 401, nil))
			return
		}
		var in InvokeRequest
		if err := g.readRequest(w, r, &in); err != nil {
			writeFailure(w, err)
			return
		}
		out, err := g.Invoke(r.Context(), token, in)
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(out.StatusCode)
		_, _ = w.Write(out.Body)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.RawQuery != "" {
			writeFailure(w, fail(ReasonMalformed, 400, nil))
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func bearer(r *http.Request) cellncapability.Token {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return cellncapability.Token{}
	}
	return cellncapability.NewToken(strings.TrimPrefix(values[0], "Bearer "))
}
func (g *Gateway) issuerAuthenticated(r *http.Request) bool {
	t := bearer(r)
	return !t.Empty() && subtle.ConstantTimeCompare([]byte(t.Bearer()), []byte(g.config.RegistrationToken.Bearer())) == 1
}
func (g *Gateway) readRequest(w http.ResponseWriter, r *http.Request, out any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.config.MaxRequestBytes))
	if err != nil {
		return fail(ReasonMalformed, 413, nil)
	}
	if err = decodeStrict(raw, out); err != nil {
		return fail(ReasonMalformed, 400, nil)
	}
	return nil
}
func writeFailure(w http.ResponseWriter, err error) {
	status, reason := 503, ReasonUnavailable
	var e *Error
	if errors.As(err, &e) {
		status, reason = e.Status, e.Reason
	} else {
		reason = modelbudget.Reason(err)
		switch reason {
		case modelbudget.ReasonExhausted:
			status = 429
		case modelbudget.ReasonRequestConflict, modelbudget.ReasonRegisterConflict:
			status = 409
		case modelbudget.ReasonClosed, modelbudget.ReasonNotFound, modelbudget.ReasonDeadline:
			status = 403
		default:
			status = 503
			reason = ReasonUnavailable
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"reason": reason})
}
