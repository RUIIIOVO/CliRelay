package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
)

// ModelGate decides whether one model may be served on the current request.
// It returns nil to allow, or the error the caller must surface to the client.
//
// The HTTP model-restriction middleware evaluates the same rules (catalog
// disable toggle, API-key allowed-models, channel-group scope, CC Switch
// mapping) against the JSON body of a POST. A WebSocket upgrade carries no
// body: every turn's model arrives later, inside a frame, after the middleware
// has already run. The middleware therefore publishes its verdict function on
// the gin context, and frame-driven handlers apply it per turn.
type ModelGate func(model string) *interfaces.ErrorMessage

// ModelGateContextKey is the gin context key the middleware stores a ModelGate under.
const ModelGateContextKey = "cliproxy.modelGate"

// ModelGateFromGin returns the gate published for this request, or nil when no
// restriction applies (the middleware did not run, or nothing is restricted).
func ModelGateFromGin(c *gin.Context) ModelGate {
	if c == nil {
		return nil
	}
	value, ok := c.Get(ModelGateContextKey)
	if !ok {
		return nil
	}
	gate, _ := value.(ModelGate)
	return gate
}
