package singbox

import "context"

// Validator validates rendered sing-box config bytes.
// Future implementation may call `sing-box check`.
type Validator interface {
	Validate(ctx context.Context, config []byte) error
}

// NoopValidator is a default validator implementation for MVP.
type NoopValidator struct{}

func (NoopValidator) Validate(_ context.Context, _ []byte) error {
	return nil
}
