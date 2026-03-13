package apply

import (
	"context"
	"encoding/json"
	"fmt"
)

type JSONValidator struct{}

func (JSONValidator) Name() string { return "json-syntax" }

func (JSONValidator) Validate(_ context.Context, artifact ConfigArtifact) error {
	if !json.Valid(artifact.Content) {
		return fmt.Errorf("config payload is not valid JSON")
	}
	return nil
}
