package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type codexCancellationTestExecutor struct {
	chunks chan cliproxyexecutor.StreamChunk
}

func (*codexCancellationTestExecutor) Identifier() string { return "codex" }

func (*codexCancellationTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *codexCancellationTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{Chunks: e.chunks}, nil
}

func (*codexCancellationTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*codexCancellationTestExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, errors.New("not implemented")
}

func (*codexCancellationTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func runCodexStreamResultTest(t *testing.T, cancelBeforeTail bool, tail cliproxyexecutor.StreamChunk) *Auth {
	t.Helper()
	model := "codex-cancel-model-" + uuid.NewString()
	auth := &Auth{
		ID:       "codex-cancel-auth-" + uuid.NewString(),
		Provider: "codex",
		Metadata: map[string]any{"access_token": "test-token", "request_retry": float64(0)},
	}
	source := make(chan cliproxyexecutor.StreamChunk, 2)
	source <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
	executor := &codexCancellationTestExecutor{chunks: source}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, errStream := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	if chunk := <-stream.Chunks; chunk.Err != nil || string(chunk.Payload) != "first" {
		t.Fatalf("first chunk = %#v", chunk)
	}

	if cancelBeforeTail {
		cancel()
	} else {
		defer cancel()
	}
	if tail.Err != nil || len(tail.Payload) != 0 {
		source <- tail
	}
	close(source)
	for range stream.Chunks {
	}

	got, ok := manager.GetByID(auth.ID)
	if !ok || got == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	return got
}

func TestManagerCodexStreamTailCancellationDoesNotCountFailure(t *testing.T) {
	got := runCodexStreamResultTest(t, true, cliproxyexecutor.StreamChunk{Err: context.Canceled})
	if got.Failed != 0 {
		t.Fatalf("Failed = %d, want 0 for client cancellation", got.Failed)
	}
	if got.Success != 0 {
		t.Fatalf("Success = %d, want 0 for client cancellation", got.Success)
	}
}

func TestManagerCodexStreamUpstreamFailureStillCountsAfterClientCancellation(t *testing.T) {
	got := runCodexStreamResultTest(t, true, cliproxyexecutor.StreamChunk{Err: &Error{
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "upstream unavailable",
	}})
	if got.Failed != 1 {
		t.Fatalf("Failed = %d, want 1 for upstream failure", got.Failed)
	}
	if got.Success != 0 {
		t.Fatalf("Success = %d, want 0 for upstream failure", got.Success)
	}
	if len(got.ModelStates) != 1 {
		t.Fatalf("ModelStates = %#v, want one failed model state", got.ModelStates)
	}
	for _, state := range got.ModelStates {
		if state == nil || !state.Unavailable {
			t.Fatalf("model state = %#v, want unavailable cooldown state", state)
		}
	}
}

func TestManagerCodexStreamSuccessfulCompletionStillCountsSuccess(t *testing.T) {
	got := runCodexStreamResultTest(t, false, cliproxyexecutor.StreamChunk{})
	if got.Failed != 0 {
		t.Fatalf("Failed = %d, want 0 for successful completion", got.Failed)
	}
	if got.Success != 1 {
		t.Fatalf("Success = %d, want 1 for successful completion", got.Success)
	}
}
