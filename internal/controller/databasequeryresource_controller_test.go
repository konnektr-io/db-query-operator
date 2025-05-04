package controller

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func TestSetupWithManager(t *testing.T) {
	// Create a fake manager
	mgr := &fakeManager{}

	reconciler := &DatabaseQueryResourceReconciler{
		Client: fake.NewClientBuilder().Build(),
		Log:    zap.New(zap.UseDevMode(true)),
	}

	err := reconciler.SetupWithManager(mgr)
	assert.NoError(t, err, "SetupWithManager should not return an error")
}

// fakeManager is a mock implementation of manager.Manager for testing purposes.
type fakeManager struct {
	manager.Manager
}

func (f *fakeManager) GetCache() cache.Cache {
	c, _ := cache.New(nil, cache.Options{}) // Handle the returned error
	return c
}

func (f *fakeManager) GetRESTMapper() meta.RESTMapper {
	config := &rest.Config{}                                  // Mock configuration
	client := &http.Client{}                                  // Mock HTTP client
	mapper, _ := apiutil.NewDynamicRESTMapper(config, client) // Provide required arguments
	return mapper
}

// Mock the GetControllerOptions method on the fakeManager
func (f *fakeManager) GetControllerOptions() config.Controller {
	return config.Controller{} // Return a default Controller configuration
}
