package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
)

type Gateway struct {
	config      Config
	verifier    *cellncapability.Verifier
	k8s         client.Reader
	budgets     BudgetStore
	authorities AuthorityStore
	sem         chan struct{}
	newClient   func(string, bool, time.Duration) (*http.Client, error)
}

func New(config Config, verifier *cellncapability.Verifier, k8s client.Reader, budgets BudgetStore, authorities AuthorityStore) (*Gateway, error) {
	if err := config.defaults(); err != nil {
		return nil, err
	}
	if verifier == nil || k8s == nil || budgets == nil || authorities == nil {
		return nil, fmt.Errorf("verifier, Kubernetes reader, durable budget store and authority store are required")
	}
	return &Gateway{config: config, verifier: verifier, k8s: k8s, budgets: budgets, authorities: authorities,
		sem: make(chan struct{}, config.MaxConcurrent), newClient: clientForEndpoint}, nil
}

func (g *Gateway) Ready(ctx context.Context) error {
	if err := g.config.AuthorityReady(ctx); err != nil {
		return fail(ReasonUnavailable, 503, nil)
	}
	if err := g.budgets.CheckReady(ctx); err != nil {
		return fail(ReasonUnavailable, 503, err)
	}
	if err := g.authorities.CheckReady(ctx); err != nil {
		return fail(ReasonUnavailable, 503, err)
	}
	return nil
}

// PinCredentialSource is restricted to the separately authenticated issuer
// endpoint. It returns identity metadata only, never Secret data.
func (g *Gateway) PinCredentialSource(ctx context.Context, in PinRequest) (PinResponse, error) {
	if in.Namespace == "" || in.ConnectionName == "" || in.ModelConnectionSpecSHA256 == "" {
		return PinResponse{}, fail(ReasonMalformed, 400, nil)
	}
	var connection api.ModelConnection
	if err := g.k8s.Get(ctx, types.NamespacedName{Namespace: in.Namespace, Name: in.ConnectionName}, &connection); err != nil {
		return PinResponse{}, fail(ReasonUnavailable, 503, err)
	}
	digest, err := connectionDigest(connection.Spec)
	if err != nil || digest != in.ModelConnectionSpecSHA256 || connection.Spec.Disabled || connection.DeletionTimestamp != nil {
		return PinResponse{}, fail(ReasonRouteChanged, 409, err)
	}
	if connection.Spec.SecretRef != in.CredentialSecretName || in.CredentialSecretKey == "" {
		return PinResponse{}, fail(ReasonCredentialChanged, 409, nil)
	}
	var secret corev1.Secret
	if err := g.k8s.Get(ctx, types.NamespacedName{Namespace: in.Namespace, Name: in.CredentialSecretName}, &secret); err != nil {
		return PinResponse{}, fail(ReasonUnavailable, 503, err)
	}
	if secret.DeletionTimestamp != nil || len(secret.Data[in.CredentialSecretKey]) == 0 {
		return PinResponse{}, fail(ReasonCredentialChanged, 409, nil)
	}
	return PinResponse{ModelConnectionUID: string(connection.UID), SecretUID: string(secret.UID)}, nil
}

func (g *Gateway) Register(ctx context.Context, in RegistrationRequest) error {
	decision, err := decodeDecision(in.Decision)
	if err != nil {
		return fail(ReasonMalformed, 400, err)
	}
	if decision.Operation != "execution.start" && decision.Operation != "execution.turn" {
		return fail(ReasonForbidden, 403, nil)
	}
	verifyCtx := contextFor(decision, decision.Operation)
	verifyCtx.ClusterID = g.config.ClusterID
	verified, err := g.verifier.Verify(in.ExecutionToken, in.Decision, verifyCtx)
	if err != nil {
		return fail(ReasonUnauthorized, 401, err)
	}
	connection, _, err := g.liveRoute(ctx, decision, in.ConnectionName, true)
	if err != nil {
		return err
	}
	original := decision
	originalDigest := verified.DecisionDigest
	if decision.Operation == "execution.turn" {
		initial, err := g.authorities.Authority(ctx, decision.Budget.BudgetID, decision.Run.UID)
		if err != nil {
			return err
		}
		original = initial.Decision
		if err := retainRunAuthority(original, decision); err != nil {
			return err
		}
		originalDigest = initial.DecisionDigest
	}
	routeDigest, err := digestJSON(decision.Route)
	if err != nil {
		return fail(ReasonMalformed, 400, err)
	}
	parentDeadline := time.Unix(decision.Budget.ParentDeadlineUnix, 0).UTC()
	if decision.Budget.ParentDeadlineUnix == 0 {
		parentDeadline = time.Unix(decision.Budget.TurnDeadlineUnix, 0).UTC()
	}
	if err := g.budgets.RegisterRun(ctx, modelbudget.RunRegistration{
		BudgetID: decision.Budget.BudgetID, ClusterID: decision.ClusterID, NamespaceUID: decision.Run.NamespaceUID,
		RunUID: decision.Run.UID, DecisionDigest: originalDigest, RouteDigest: routeDigest,
		MaxRequests: original.Budget.RunCap.Requests, MaxOutputTokens: original.Budget.RunCap.OutputTokens,
		MaxTurns: decision.Budget.MaxTurns, ParentDeadline: parentDeadline,
	}); err != nil {
		return err
	}
	tid := turnID(decision)
	if err := g.budgets.RegisterTurn(ctx, modelbudget.TurnRegistration{
		BudgetID: decision.Budget.BudgetID, TurnID: tid, DecisionDigest: verified.DecisionDigest,
		MaxRequests: decision.Budget.TurnCap.Requests, MaxOutputTokens: decision.Budget.TurnCap.OutputTokens,
		Deadline: time.Unix(decision.Budget.TurnDeadlineUnix, 0).UTC(),
	}); err != nil {
		return err
	}
	return g.authorities.RegisterAuthority(ctx, Authority{
		Decision: decision,
		BudgetID: decision.Budget.BudgetID, TurnID: tid, DecisionDigest: verified.DecisionDigest,
		ClusterID: decision.ClusterID, Namespace: decision.Run.Namespace, NamespaceUID: decision.Run.NamespaceUID,
		RunUID: decision.Run.UID, RunSpecSHA256: decision.Run.SpecSHA256, ConnectionName: in.ConnectionName,
		Endpoint: connection.Spec.Endpoint, AllowInsecure: connection.Spec.AllowInsecure, Route: decision.Route,
	})
}

func (g *Gateway) Invoke(ctx context.Context, token cellncapability.Token, in InvokeRequest) (InvokeResponse, error) {
	if int64(len(in.Decision)+len(in.Request)) > g.config.MaxRequestBytes || in.RequestID == "" {
		return InvokeResponse{}, fail(ReasonMalformed, 400, nil)
	}
	decision, err := decodeDecision(in.Decision)
	if err != nil {
		return InvokeResponse{}, fail(ReasonMalformed, 400, err)
	}
	verifyCtx := contextFor(decision, "model.invoke")
	verifyCtx.ClusterID = g.config.ClusterID
	verified, err := g.verifier.Verify(token, in.Decision, verifyCtx)
	if err != nil {
		return InvokeResponse{}, fail(ReasonUnauthorized, 401, err)
	}
	// Do not hold credentials/reservations in an unbounded capacity queue.
	// Authority is read only after obtaining a bounded local admission slot.
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	default:
		return InvokeResponse{}, fail(ReasonUnavailable, 503, nil)
	}
	tid := turnID(decision)
	authority, err := g.authorities.Authority(ctx, decision.Budget.BudgetID, tid)
	if err != nil {
		return InvokeResponse{}, err
	}
	if authority.DecisionDigest != verified.DecisionDigest || !sameAuthorityDecision(authority, decision) {
		return InvokeResponse{}, fail(ReasonForbidden, 403, nil)
	}
	connection, credential, err := g.liveRoute(ctx, decision, authority.ConnectionName, true)
	if err != nil {
		return InvokeResponse{}, err
	}
	if connection.Spec.Endpoint != authority.Endpoint || connection.Spec.AllowInsecure != authority.AllowInsecure {
		return InvokeResponse{}, fail(ReasonRouteChanged, 403, nil)
	}
	reservedOutput, digest, body, err := validateProviderRequest(decision.Route.Protocol, decision.Route.Model, in.Request, decision.Budget.TurnCap.OutputTokens)
	if err != nil {
		return InvokeResponse{}, err
	}
	reservation, err := g.budgets.Reserve(ctx, modelbudget.ReservationRequest{
		BudgetID: decision.Budget.BudgetID, TurnID: tid, RequestID: in.RequestID,
		RequestDigest: digest, ReservedOutputTokens: reservedOutput,
	})
	if err != nil {
		return InvokeResponse{}, err
	}
	if reservation.Existing {
		return InvokeResponse{}, fail(ReasonRequestConflict, 409, nil)
	}
	deadline := time.Unix(decision.Budget.TurnDeadlineUnix, 0)
	providerCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	providerCtx, stopWatching, err := g.watchBudget(providerCtx, decision.Budget.BudgetID, tid)
	if err != nil {
		return InvokeResponse{}, err
	}
	defer stopWatching()
	allowPrivate := authority.AllowInsecure && privateOriginAllowed(authority.Endpoint, g.config.AllowPrivateOrigins)
	httpClient, err := g.newClient(authority.Endpoint, allowPrivate, g.config.MaxProviderDuration)
	if err != nil {
		return InvokeResponse{}, err
	}
	req, err := http.NewRequestWithContext(providerCtx, http.MethodPost, authority.Endpoint, bytes.NewReader(body))
	if err != nil {
		return InvokeResponse{}, fail(ReasonMalformed, 400, err)
	}
	req.Header.Set("Content-Type", "application/json")
	switch decision.Route.Protocol {
	case "openai-chat":
		if len(credential) > 0 {
			req.Header.Set("Authorization", "Bearer "+string(credential))
		}
	case "anthropic-messages":
		if len(credential) > 0 {
			req.Header.Set("x-api-key", string(credential))
		}
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		return InvokeResponse{}, fail(ReasonProtocol, 400, nil)
	}
	if err := g.budgets.MarkInFlight(providerCtx, decision.Budget.BudgetID, tid, in.RequestID); err != nil {
		return InvokeResponse{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		_ = g.budgets.Reconcile(context.Background(), decision.Budget.BudgetID, tid, in.RequestID, 0, "uncertain", "provider-outcome-unknown")
		return InvokeResponse{}, fail(ReasonProviderUnavailable, 502, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, g.config.MaxResponseBytes+1))
	if err != nil || int64(len(responseBody)) > g.config.MaxResponseBytes {
		_ = g.budgets.Reconcile(context.Background(), decision.Budget.BudgetID, tid, in.RequestID, 0, "terminal", "response-invalid")
		return InvokeResponse{}, fail(ReasonResponseTooLarge, 502, err)
	}
	// Provider errors can echo request headers or keys. Never expose their body
	// to a tenant, even when the provider labels it application/json.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = g.budgets.Reconcile(context.Background(), decision.Budget.BudgetID, tid, in.RequestID, 0, "terminal", "provider-error")
		return InvokeResponse{}, fail(ReasonProviderUnavailable, 502, nil)
	}
	if !json.Valid(responseBody) || (len(credential) > 0 && bytes.Contains(responseBody, credential)) || bytes.Contains(responseBody, []byte(token.Bearer())) {
		_ = g.budgets.Reconcile(context.Background(), decision.Budget.BudgetID, tid, in.RequestID, 0, "terminal", "response-invalid")
		return InvokeResponse{}, fail(ReasonProviderUnavailable, 502, nil)
	}
	observed := observedOutput(decision.Route.Protocol, responseBody)
	if observed < 0 {
		_ = g.budgets.Reconcile(context.Background(), decision.Budget.BudgetID, tid, in.RequestID, 0, "terminal", "usage-invalid")
		return InvokeResponse{}, fail(ReasonProviderUnavailable, 502, nil)
	}
	outcome := fmt.Sprintf("provider-http-%d", resp.StatusCode)
	if err := g.budgets.Reconcile(context.Background(), decision.Budget.BudgetID, tid, in.RequestID, observed, "terminal", outcome); err != nil {
		return InvokeResponse{}, err
	}
	return InvokeResponse{StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: responseBody}, nil
}

func contextFor(decision cellncapability.Decision, operation string) cellncapability.VerifyContext {
	return cellncapability.VerifyContext{ExpectedAudience: map[bool]string{true: cellncapability.AudienceModelGateway, false: cellncapability.AudienceExecution}[operation == "model.invoke"],
		ExpectedOperation: operation, ClusterID: decision.ClusterID, Namespace: decision.Run.Namespace,
		NamespaceUID: decision.Run.NamespaceUID, RunUID: decision.Run.UID, RunSpecSHA256: decision.Run.SpecSHA256,
		Parent: decision.Parent, RequestDigest: decision.RequestDigest, Route: decision.Route, BudgetID: decision.Budget.BudgetID}
}

func sameAuthorityDecision(a Authority, d cellncapability.Decision) bool {
	return a.ClusterID == d.ClusterID && a.Namespace == d.Run.Namespace && a.NamespaceUID == d.Run.NamespaceUID &&
		a.RunUID == d.Run.UID && a.RunSpecSHA256 == d.Run.SpecSHA256 && a.BudgetID == d.Budget.BudgetID && a.TurnID == turnID(d) && reflect.DeepEqual(a.Route, d.Route)
}

func (g *Gateway) liveRoute(ctx context.Context, decision cellncapability.Decision, connectionName string, readCredential bool) (api.ModelConnection, []byte, error) {
	var namespace corev1.Namespace
	if err := g.k8s.Get(ctx, types.NamespacedName{Name: decision.Run.Namespace}, &namespace); err != nil {
		return api.ModelConnection{}, nil, fail(ReasonRouteChanged, 503, err)
	}
	if string(namespace.UID) != decision.Run.NamespaceUID || namespace.DeletionTimestamp != nil {
		return api.ModelConnection{}, nil, fail(ReasonRouteChanged, 403, nil)
	}
	var connection api.ModelConnection
	if err := g.k8s.Get(ctx, types.NamespacedName{Namespace: decision.Run.Namespace, Name: connectionName}, &connection); err != nil {
		return connection, nil, fail(ReasonRouteChanged, 503, err)
	}
	if connection.DeletionTimestamp != nil {
		return connection, nil, fail(ReasonRouteChanged, 403, nil)
	}
	if err := validateConnection(decision, connectionName, string(connection.UID), connection.Spec); err != nil {
		return connection, nil, err
	}
	if !readCredential || decision.Route.Auth == "none" {
		return connection, nil, nil
	}
	source := decision.Route.CredentialSource
	var secret corev1.Secret
	if err := g.k8s.Get(ctx, types.NamespacedName{Namespace: decision.Run.Namespace, Name: source.SecretName}, &secret); err != nil {
		return connection, nil, fail(ReasonCredentialChanged, 503, err)
	}
	credential := secret.Data[source.SecretKey]
	if secret.DeletionTimestamp != nil || string(secret.UID) != source.SecretUID || len(credential) == 0 {
		return connection, nil, fail(ReasonCredentialChanged, 403, nil)
	}
	return connection, append([]byte(nil), credential...), nil
}

func observedOutput(protocol string, raw []byte) int64 {
	var response struct {
		Usage struct {
			CompletionTokens int64 `json:"completion_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return 0
	}
	if protocol == "anthropic-messages" {
		return response.Usage.OutputTokens
	}
	return response.Usage.CompletionTokens
}
