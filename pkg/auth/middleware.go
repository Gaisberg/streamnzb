package auth

import (
	"context"
	"net/http"
)

type contextKey string

const userContextKey contextKey = "user"

func DeviceFromContext(r *http.Request) (*Device, bool) {
	device, ok := r.Context().Value(userContextKey).(*Device)
	return device, ok
}

func ContextWithDevice(ctx context.Context, device *Device) context.Context {
	return context.WithValue(ctx, userContextKey, device)
}
