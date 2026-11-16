package controllers

import (
	"app/internal/models/dto"
	"context"
)

// Other controller unit tests isolate their own domain behavior; PostgreSQL tests exercise real session tracking.
type browserSessionTestStore struct{}

func (browserSessionTestStore) Observe(_ context.Context, _, _, _, _ string, detected, _ bool, generation *int) (*dto.BrowserSessionResponse, string, error) {
	result := &dto.BrowserSessionResponse{IsSEB: detected, SEBNotDetected: !detected}
	if generation != nil {
		result.Generation = *generation
	}
	return result, "", nil
}
func (browserSessionTestStore) Reset(context.Context, string, string, string) (int, error) {
	return 1, nil
}
