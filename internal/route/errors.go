package route

import "errors"

var (
	ErrRoutingOverloaded         = errors.New("routing overloaded")
	ErrRoutingTimeout            = errors.New("routing backend timeout")
	ErrRouteNotFound             = errors.New("route not found")
	ErrRoutingBackendUnavailable = errors.New("routing backend unavailable")
)
