package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestManagerMarkResultSuccessOnOtherModelClearsOnlyExpiredTransientError(t *testing.T) {
	tests := []struct {
		name              string
		status            int
		expireCooldown    bool
		removeDeadline    bool
		disableState      bool
		activeQuota       bool
		message           string
		wantStatus        Status
		wantStatusMessage string
	}{
		{name: "expired transient", status: http.StatusServiceUnavailable, expireCooldown: true, wantStatus: StatusActive},
		{name: "active transient", status: http.StatusServiceUnavailable, wantStatus: StatusError, wantStatusMessage: "model failure"},
		{name: "no deadline transient", status: http.StatusServiceUnavailable, removeDeadline: true, wantStatus: StatusError, wantStatusMessage: "model failure"},
		{name: "unauthorized", status: http.StatusUnauthorized, expireCooldown: true, wantStatus: StatusError, wantStatusMessage: "model failure"},
		{name: "quota", status: http.StatusTooManyRequests, expireCooldown: true, wantStatus: StatusError, wantStatusMessage: "model failure"},
		{name: "transient with active quota", status: http.StatusServiceUnavailable, expireCooldown: true, activeQuota: true, wantStatus: StatusError, wantStatusMessage: "model failure"},
		{name: "disabled", status: http.StatusServiceUnavailable, expireCooldown: true, disableState: true, wantStatus: StatusError, wantStatusMessage: "model failure"},
		{name: "cloudflare challenge", status: http.StatusForbidden, expireCooldown: true, message: "cloudflare challenge", wantStatus: StatusError, wantStatusMessage: "cloudflare challenge"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-1", Provider: "codex", Status: StatusActive}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			message := tt.message
			if message == "" {
				message = "model failure"
			}
			manager.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    "model-a",
				Success:  false,
				Error:    &Error{HTTPStatus: tt.status, Message: message},
			})

			manager.mu.Lock()
			failedState := manager.auths[auth.ID].ModelStates["model-a"]
			if failedState == nil {
				manager.mu.Unlock()
				t.Fatal("failed model state missing")
			}
			if tt.expireCooldown {
				failedState.NextRetryAfter = time.Now().Add(-time.Second)
				if failedState.Quota.Exceeded {
					failedState.Quota.NextRecoverAt = time.Now().Add(-time.Second)
				}
			}
			if tt.removeDeadline {
				failedState.NextRetryAfter = time.Time{}
			}
			if tt.disableState {
				failedState.Status = StatusDisabled
			}
			if tt.activeQuota {
				failedState.Quota = QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Now().Add(time.Hour)}
			}
			manager.mu.Unlock()

			manager.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    "model-b",
				Success:  true,
			})

			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatal("updated auth missing")
			}
			if updated.Status != tt.wantStatus || updated.StatusMessage != tt.wantStatusMessage {
				t.Fatalf("auth status = %q message %q, want %q message %q", updated.Status, updated.StatusMessage, tt.wantStatus, tt.wantStatusMessage)
			}
			if updated.Success != 1 || updated.Failed != 1 {
				t.Fatalf("auth counters = success %d failed %d, want 1 and 1", updated.Success, updated.Failed)
			}
			retained := updated.ModelStates["model-a"]
			if retained == nil || retained.LastError == nil || retained.StatusMessage != message {
				t.Fatalf("model diagnostics were not retained: %+v", retained)
			}
			if tt.wantStatus == StatusActive {
				if updated.Unavailable {
					t.Fatal("auth unavailable after expired transient cooldown")
				}
				if !retained.NextRetryAfter.IsZero() {
					t.Fatalf("expired model retry deadline = %v, want zero", retained.NextRetryAfter)
				}
			}
		})
	}
}

func TestManagerMarkResultRepeatedExpiredTransientCyclesStayRecoverable(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-1", Provider: "codex", Status: StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	for _, failedModel := range []string{"model-a", "model-c"} {
		manager.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    failedModel,
			Success:  false,
			Error:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "model failure"},
		})

		manager.mu.Lock()
		manager.auths[auth.ID].ModelStates[failedModel].NextRetryAfter = time.Now().Add(-time.Second)
		manager.mu.Unlock()

		manager.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    "model-b",
			Success:  true,
		})

		updated, ok := manager.GetByID(auth.ID)
		if !ok || updated == nil {
			t.Fatal("updated auth missing")
		}
		if updated.Status != StatusActive || updated.StatusMessage != "" || updated.Unavailable {
			t.Fatalf("auth status after %s cycle = %q message %q unavailable %v, want active, empty, false", failedModel, updated.Status, updated.StatusMessage, updated.Unavailable)
		}
	}

	updated, _ := manager.GetByID(auth.ID)
	if updated.Success != 2 || updated.Failed != 2 {
		t.Fatalf("auth counters = success %d failed %d, want 2 and 2", updated.Success, updated.Failed)
	}
	for _, model := range []string{"model-a", "model-c"} {
		state := updated.ModelStates[model]
		if state == nil || state.LastError == nil || state.StatusMessage != "model failure" {
			t.Fatalf("model %s diagnostics were not retained: %+v", model, state)
		}
	}
}

func TestUpdateAggregatedAvailability_UnavailableWithoutNextRetryDoesNotBlockAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusError,
				Unavailable: true,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if auth.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
}

func TestUpdateAggregatedAvailability_FutureNextRetryBlocksAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	next := now.Add(5 * time.Minute)
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = zero, want %v", next)
	}
	if auth.NextRetryAfter.Sub(next) > time.Second || next.Sub(auth.NextRetryAfter) > time.Second {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", auth.NextRetryAfter, next)
	}
}

func TestManager_AvailableProvidersAndHasProviderAuth_ExcludeDisabled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	if _, err := manager.Register(ctx, &Auth{ID: "active", Provider: "claude", Status: StatusActive}); err != nil {
		t.Fatalf("register active auth: %v", err)
	}
	// Provider gemini only has an auth with the Disabled flag set.
	if _, err := manager.Register(ctx, &Auth{ID: "flag-disabled", Provider: "gemini", Disabled: true}); err != nil {
		t.Fatalf("register flag-disabled auth: %v", err)
	}
	// Provider codex only has an auth whose Status is StatusDisabled.
	if _, err := manager.Register(ctx, &Auth{ID: "status-disabled", Provider: "codex", Status: StatusDisabled}); err != nil {
		t.Fatalf("register status-disabled auth: %v", err)
	}

	providers := manager.AvailableProviders()
	present := make(map[string]bool, len(providers))
	for _, p := range providers {
		present[p] = true
	}
	if !present["claude"] {
		t.Errorf("AvailableProviders() = %v, want to include active provider claude", providers)
	}
	if present["gemini"] {
		t.Errorf("AvailableProviders() = %v, want to exclude Disabled provider gemini", providers)
	}
	if present["codex"] {
		t.Errorf("AvailableProviders() = %v, want to exclude StatusDisabled provider codex", providers)
	}

	if !manager.HasProviderAuth("claude") {
		t.Errorf("HasProviderAuth(claude) = false, want true")
	}
	if manager.HasProviderAuth("gemini") {
		t.Errorf("HasProviderAuth(gemini) = true, want false (only Disabled auth registered)")
	}
	if manager.HasProviderAuth("codex") {
		t.Errorf("HasProviderAuth(codex) = true, want false (only StatusDisabled auth registered)")
	}
}

func TestManager_ResetQuotaClearsRuntimeAndRegistryState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "reset-quota-auth"
	model := "reset-quota-model"
	next := time.Now().Add(time.Hour)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:             authID,
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
				UpdatedAt:      next,
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg.SetModelQuotaExceeded(authID, model)
	reg.SuspendClientModel(authID, model, "quota")
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count before reset = %d, want 0", count)
	}

	updated, models, errReset := manager.ResetQuota(ctx, authID)
	if errReset != nil {
		t.Fatalf("ResetQuota() error = %v", errReset)
	}
	if updated == nil {
		t.Fatalf("ResetQuota() updated auth is nil")
	}
	if len(models) != 1 || models[0] != model {
		t.Fatalf("ResetQuota() models = %v, want [%s]", models, model)
	}
	if updated.Status != StatusActive || updated.StatusMessage != "" || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("updated auth state = status %q message %q unavailable %v next %v", updated.Status, updated.StatusMessage, updated.Unavailable, updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason != "" || !updated.Quota.NextRecoverAt.IsZero() || updated.Quota.BackoffLevel != 0 {
		t.Fatalf("updated auth quota = %+v, want cleared", updated.Quota)
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("updated model state missing")
	}
	if state.Status != StatusActive || state.StatusMessage != "" || state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("updated model state = status %q message %q unavailable %v next %v", state.Status, state.StatusMessage, state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		t.Fatalf("updated model quota = %+v, want cleared", state.Quota)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count after reset = %d, want 1", count)
	}
}
