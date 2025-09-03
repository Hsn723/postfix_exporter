package main

import (
	"context"
	"os"
	"testing"

	"github.com/alecthomas/kingpin/v2"
	"github.com/stretchr/testify/assert"
)

func TestKubernetesLogSourceFactory_Init(t *testing.T) {
	app := kingpin.New("test", "test")
	factory := &kubernetesLogSourceFactory{}

	factory.Init(app)

	// Parse some test flags
	args := []string{
		"--kubernetes.namespace", "default",
		"--kubernetes.label-selector", "app=postfix",
		"--kubernetes.container", "postfix",
		"--kubernetes.kubeconfig", "/path/to/kubeconfig",
	}

	_, err := app.Parse(args)
	assert.NoError(t, err)

	assert.Equal(t, "default", factory.namespace)
	assert.Equal(t, "app=postfix", factory.labelSelector)
	assert.Equal(t, "postfix", factory.containerName)
	assert.Equal(t, "/path/to/kubeconfig", factory.kubeconfigPath)
}

func TestKubernetesLogSourceFactory_New_NoConfig(t *testing.T) {
	ctx := context.Background()
	factory := &kubernetesLogSourceFactory{}

	// Should return nil when not configured
	src, err := factory.New(ctx)
	assert.NoError(t, err)
	assert.Nil(t, src)
}

func TestKubernetesLogSourceFactory_New_InvalidLabelSelector(t *testing.T) {
	ctx := context.Background()
	factory := &kubernetesLogSourceFactory{
		namespace:     "default",
		labelSelector: "invalid-selector",
	}

	// Should return error for invalid label selector
	src, err := factory.New(ctx)
	assert.Error(t, err)
	assert.Nil(t, src)
	assert.Contains(t, err.Error(), "invalid label selector format")
}

func TestKubernetesLogSource_Path(t *testing.T) {
	source := &KubernetesLogSource{
		namespace:     "test-namespace",
		labelSelector: "app=test",
	}

	expected := "kubernetes://test-namespace/app=test"
	assert.Equal(t, expected, source.Path())
}

func TestKubernetesLogSourceFactory_EnvironmentVariables(t *testing.T) {
	// Set environment variables
	os.Setenv("KUBERNETES_POD_NAME", "test-pod-from-env")
	os.Setenv("KUBERNETES_NAMESPACE", "test-ns-from-env")
	os.Setenv("KUBERNETES_LABEL_SELECTOR", "app=test-from-env")
	os.Setenv("KUBERNETES_CONTAINER", "test-container-from-env")
	
	defer func() {
		// Clean up environment variables
		os.Unsetenv("KUBERNETES_POD_NAME")
		os.Unsetenv("KUBERNETES_NAMESPACE") 
		os.Unsetenv("KUBERNETES_LABEL_SELECTOR")
		os.Unsetenv("KUBERNETES_CONTAINER")
	}()
	
	// Create a new kingpin app and factory
	app := kingpin.New("test", "test")
	factory := &kubernetesLogSourceFactory{}
	factory.Init(app)
	
	// Parse empty args (should use environment variables)
	_, err := app.Parse([]string{})
	assert.NoError(t, err)
	
	// Verify environment variables were used
	assert.Equal(t, "test-pod-from-env", factory.podName)
	assert.Equal(t, "test-ns-from-env", factory.namespace)
	assert.Equal(t, "app=test-from-env", factory.labelSelector)
	assert.Equal(t, "test-container-from-env", factory.containerName)
}
