package serverconfig

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFakeClientWithSecret(t *testing.T, ns, name string, data map[string][]byte) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return scheme
}

func TestLoadIdracCredentials_HappyPath(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "idrac-credentials", Namespace: "default"},
		Data: map[string][]byte{
			"username": []byte("root"),
			"password": []byte("hunter2"),
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	got, err := loadIdracCredentials(context.Background(), cli, "default", "idrac-credentials")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Username != "root" || got.Password != "hunter2" {
		t.Errorf("got %+v, want {root, hunter2}", got)
	}
}

func TestLoadIdracCredentials_NotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err := loadIdracCredentials(context.Background(), cli, "default", "missing")
	if err == nil {
		t.Fatalf("expected not-found error, got nil")
	}
}

func TestLoadIdracCredentials_MissingKeys(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	// Secret exists but has no `username` key.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "idrac-credentials", Namespace: "default"},
		Data:       map[string][]byte{"password": []byte("hunter2")},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	_, err := loadIdracCredentials(context.Background(), cli, "default", "idrac-credentials")
	if err == nil {
		t.Fatalf("expected missing-keys error, got nil")
	}
	if !strings.Contains(err.Error(), "username") && !strings.Contains(err.Error(), "password") {
		t.Errorf("error should name the missing key(s): %v", err)
	}
}
