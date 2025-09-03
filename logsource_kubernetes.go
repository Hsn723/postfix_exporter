package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// A KubernetesLogSource can read lines from Kubernetes pod logs.
type KubernetesLogSource struct {
	LogSourceDefaults
	logStream     io.ReadCloser
	ctx           context.Context
	clientset     *kubernetes.Clientset
	scanner       *bufio.Scanner
	cancel        context.CancelFunc
	namespace     string
	labelSelector string
	podName       string
	containerName string
	logStreams    []containerLogStream
	logChan       chan string
}

// containerLogStream represents a log stream from a specific container.
type containerLogStream struct {
	stream        io.ReadCloser
	scanner       *bufio.Scanner
	podName       string
	containerName string
}

// NewKubernetesLogSource creates a new log source that reads from Kubernetes pod logs.
func NewKubernetesLogSource(namespace, labelSelector, podName, containerName, kubeconfigPath string) (*KubernetesLogSource, error) {
	var config *rest.Config
	var err error
	var inCluster bool

	// Try in-cluster config first (when running inside Kubernetes)
	config, err = rest.InClusterConfig()
	if err != nil {
		// If in-cluster config fails, try to use local kubeconfig for development
		log.Printf("Failed to get in-cluster config, trying local kubeconfig: %v", err)

		// Use provided kubeconfig path or default location
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes config from kubeconfig: %v", err)
		}
		if kubeconfigPath != "" {
			log.Printf("Using kubeconfig from: %s", kubeconfigPath)
		} else {
			log.Printf("Using default kubeconfig for development")
		}
		inCluster = false
	} else {
		log.Printf("Using in-cluster kubernetes config")
		inCluster = true
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// If namespace is not specified, determine the appropriate default
	if namespace == "" {
		if inCluster {
			// When running in-cluster, read the current namespace
			if namespaceBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
				namespace = string(namespaceBytes)
				log.Printf("Using current in-cluster namespace: %s", namespace)
			} else {
				log.Printf("Failed to read current namespace, falling back to 'default': %v", err)
				namespace = "default"
			}
		} else {
			// When running outside cluster (development), use default
			namespace = "default"
			log.Printf("Using default namespace for development: %s", namespace)
		}
	}

	return &KubernetesLogSource{
		clientset:     clientset,
		namespace:     namespace,
		labelSelector: labelSelector,
		podName:       podName,
		containerName: containerName,
		logChan:       make(chan string, 100), // Buffered channel for log lines
		ctx:           ctx,
		cancel:        cancel,
	}, nil
}

// initLogStreams initializes log streams from all containers in all matching pods.
func (s *KubernetesLogSource) initLogStreams() error {
	if len(s.logStreams) > 0 {
		return nil // Already initialized
	}

	var pods []corev1.Pod

	// Use the namespace determined during initialization
	namespace := s.namespace

	// Choose between pod name or label selector
	if s.podName != "" {
		// Get specific pod by name
		pod, err := s.clientset.CoreV1().Pods(namespace).Get(s.ctx, s.podName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get pod %s in namespace %s: %v", s.podName, namespace, err)
		}
		pods = []corev1.Pod{*pod}
		log.Printf("Found pod by name: %s/%s", namespace, s.podName)
	} else if s.labelSelector != "" {
		// List pods by label selector
		podList, err := s.clientset.CoreV1().Pods(namespace).List(s.ctx, metav1.ListOptions{
			LabelSelector: s.labelSelector,
		})
		if err != nil {
			return fmt.Errorf("failed to list pods with label selector %s in namespace %s: %v", s.labelSelector, namespace, err)
		}
		pods = podList.Items
		log.Printf("Found %d pods with label selector %s in namespace %s", len(pods), s.labelSelector, namespace)
	} else {
		return fmt.Errorf("either pod name or label selector must be specified")
	}

	if len(pods) == 0 {
		if s.podName != "" {
			return fmt.Errorf("pod %s not found in namespace %s", s.podName, namespace)
		} else {
			return fmt.Errorf("no pods found with label selector %s in namespace %s", s.labelSelector, namespace)
		}
	}

	// Collect all running pods
	var runningPods []corev1.Pod
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning {
			runningPods = append(runningPods, pod)
		}
	}

	if len(runningPods) == 0 {
		if s.podName != "" {
			return fmt.Errorf("pod %s in namespace %s is not running", s.podName, namespace)
		} else {
			return fmt.Errorf("no running pods found with label selector %s in namespace %s", s.labelSelector, namespace)
		}
	}

	// Create log streams for all containers in all pods
	for _, pod := range runningPods {
		containers := pod.Spec.Containers

		// If a specific container is requested, filter to just that container
		if s.containerName != "" {
			var filteredContainers []corev1.Container
			for _, container := range containers {
				if container.Name == s.containerName {
					filteredContainers = append(filteredContainers, container)
					break
				}
			}
			containers = filteredContainers
		}

		// Create log stream for each container
		for _, container := range containers {
			logOptions := &corev1.PodLogOptions{
				Follow:    true,
				TailLines: int64Ptr(10), // Start with last 10 lines
				Container: container.Name,
			}

			// Create log stream
			req := s.clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, logOptions)
			logStream, err := req.Stream(s.ctx)
			if err != nil {
				log.Printf("Failed to create log stream for pod %s container %s: %v", pod.Name, container.Name, err)
				continue
			}

			containerStream := containerLogStream{
				podName:       pod.Name,
				containerName: container.Name,
				stream:        logStream,
				scanner:       bufio.NewScanner(logStream),
			}

			s.logStreams = append(s.logStreams, containerStream)

			// Start goroutine to read from this container's log stream
			go s.readFromContainer(containerStream)

			log.Printf("Reading log events from Kubernetes pod %s/%s (container: %s)", namespace, pod.Name, container.Name)
		}
	}

	if len(s.logStreams) == 0 {
		return fmt.Errorf("no log streams could be created")
	}

	return nil
}

// readFromContainer reads log lines from a single container and sends them to the main channel.
func (s *KubernetesLogSource) readFromContainer(containerStream containerLogStream) {
	defer containerStream.stream.Close()

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			if containerStream.scanner.Scan() {
				line := containerStream.scanner.Text()
				// Prefix the line with pod and container info for better debugging
				prefixedLine := fmt.Sprintf("[%s/%s] %s", containerStream.podName, containerStream.containerName, line)

				select {
				case s.logChan <- prefixedLine:
				case <-s.ctx.Done():
					return
				}
			} else {
				// Check for scanner error
				if err := containerStream.scanner.Err(); err != nil {
					log.Printf("Error reading from container %s/%s: %v", containerStream.podName, containerStream.containerName, err)
				}
				// Stream ended, exit this goroutine
				log.Printf("Log stream ended for container %s/%s", containerStream.podName, containerStream.containerName)
				return
			}
		}
	}
}

func (s *KubernetesLogSource) Close() error {
	if s.cancel != nil {
		s.cancel()
	}

	// Close all log streams
	for _, stream := range s.logStreams {
		if stream.stream != nil {
			stream.stream.Close()
		}
	}

	// Close the log channel
	close(s.logChan)

	return nil
}

func (s *KubernetesLogSource) Path() string {
	namespace := s.namespace
	if namespace == "" {
		namespace = "default"
	}

	if s.podName != "" {
		return fmt.Sprintf("kubernetes://%s/%s", namespace, s.podName)
	}
	return fmt.Sprintf("kubernetes://%s/%s", namespace, s.labelSelector)
}

func (s *KubernetesLogSource) Read(ctx context.Context) (string, error) {
	// Initialize log streams if not already done
	if err := s.initLogStreams(); err != nil {
		return "", err
	}

	// Read from the aggregated log channel
	select {
	case line, ok := <-s.logChan:
		if !ok {
			// Channel closed, try to reinitialize after a delay
			time.Sleep(5 * time.Second)
			s.logStreams = nil // Reset streams to force reinitialization
			return "", io.EOF
		}
		return line, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.ctx.Done():
		return "", s.ctx.Err()
	}
}

// kubernetesLogSourceFactory is a factory that can create Kubernetes log sources
// from command line flags.
type kubernetesLogSourceFactory struct {
	namespace      string
	labelSelector  string
	podName        string
	containerName  string
	kubeconfigPath string
}

func (f *kubernetesLogSourceFactory) Init(app *kingpin.Application) {
	app.Flag("kubernetes.namespace", "Kubernetes namespace to read logs from (optional, defaults to current namespace when in-cluster or 'default').").Envar("KUBERNETES_NAMESPACE").StringVar(&f.namespace)
	app.Flag("kubernetes.label-selector", "Label selector to find pods (e.g., app=postfix).").Envar("KUBERNETES_LABEL_SELECTOR").StringVar(&f.labelSelector)
	app.Flag("kubernetes.pod-name", "Specific pod name to read logs from (alternative to label-selector).").Envar("KUBERNETES_POD_NAME").StringVar(&f.podName)
	app.Flag("kubernetes.container", "Container name to read logs from (optional, reads from all containers if not specified).").Envar("KUBERNETES_CONTAINER").StringVar(&f.containerName)
	app.Flag("kubernetes.kubeconfig", "Path to kubeconfig file for development (optional, uses ~/.kube/config if not specified).").Envar("KUBERNETES_KUBECONFIG").StringVar(&f.kubeconfigPath)
}

func (f *kubernetesLogSourceFactory) New(ctx context.Context) ([]LogSourceCloser, error) {
	// Must specify either pod name or label selector (but not both)
	if f.podName == "" && f.labelSelector == "" {
		return nil, nil // Not configured
	}

	if f.podName != "" && f.labelSelector != "" {
		return nil, fmt.Errorf("cannot specify both pod name and label selector, choose one")
	}

	// Validate label selector format if provided
	if f.labelSelector != "" {
		if !strings.Contains(f.labelSelector, "=") && !strings.Contains(f.labelSelector, " in ") {
			return nil, fmt.Errorf("invalid label selector format: %s (expected format: key=value or key in (value1,value2))", f.labelSelector)
		}
	}

	src, err := NewKubernetesLogSource(f.namespace, f.labelSelector, f.podName, f.containerName, f.kubeconfigPath)
	if err != nil {
		return nil, err
	}
	return []LogSourceCloser{src}, nil
}

// Helper function to create int64 pointer.
func int64Ptr(i int64) *int64 {
	return &i
}

func init() {
	RegisterLogSourceFactory(&kubernetesLogSourceFactory{})
}
