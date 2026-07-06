package serverconfig

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// idracCredentials is the (username, password) pair the controller uses for
// Redfish basic auth. The prototype assumes a single shared credential for all
// Dell iDRACs in the fleet — sourced from a single K8s Secret.
type idracCredentials struct {
	Username string
	Password string
}

// loadIdracCredentials reads a Secret with keys "username" and "password" from
// the given namespace. Both keys are required. Returns a clear error message
// so the controller can surface "MissingCredentials" without a status update.
func loadIdracCredentials(ctx context.Context, c client.Client, namespace, name string) (*idracCredentials, error) {
	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &sec); err != nil {
		return nil, fmt.Errorf("get Secret %s/%s: %w", namespace, name, err)
	}
	user, hasUser := sec.Data["username"]
	pass, hasPass := sec.Data["password"]
	if !hasUser || !hasPass {
		return nil, fmt.Errorf("Secret %s/%s missing required keys (username, password)", namespace, name)
	}
	return &idracCredentials{Username: string(user), Password: string(pass)}, nil
}
