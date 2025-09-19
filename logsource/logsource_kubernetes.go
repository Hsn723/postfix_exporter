//go:build !nokubernetes
// +build !nokubernetes

package logsource

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

// A KubernetesLogSource can read lines from Kubernetes pod logs.
type KubernetesLogSource struct {
	LogSourceDefaults
	clientset     *kubernetes.Clientset
	logStream     containerLogStream
	namespace     string
	serviceName   string
	podName       string
	containerName string
}

// containerLogStream represents a log stream from a specific container.
type containerLogStream struct {
	stream  io.ReadCloser
	scanner *bufio.Scanner
}

// NewKubernetesLogSource creates a new log source that reads from Kubernetes pod logs.
func NewKubernetesLogSource(ctx context.Context, namespace, serviceName, containerName string, pod corev1.Pod, clientset *kubernetes.Clientset) []*KubernetesLogSource {
	containers := pod.Spec.Containers
	if containerName != "" {
		for _, container := range pod.Spec.Containers {
			if container.Name == containerName {
				containers = []corev1.Container{container}
				break
			}
		}
	}
	var logSources []*KubernetesLogSource
	for _, container := range containers {
		logSource := createKubernetesLogSource(ctx, namespace, serviceName, container.Name, pod, clientset)
		logSources = append(logSources, logSource)
	}
	return logSources
}

func createKubernetesLogSource(ctx context.Context, namespace, serviceName, containerName string, pod corev1.Pod, clientset *kubernetes.Clientset) *KubernetesLogSource {
	cls, err := getContainerLogStream(ctx, namespace, pod.Name, containerName, clientset)
	// Even if we fail to get the log stream now, we can retry later in Read().
	if err != nil {
		log.Printf("Failed to get log stream for pod %s container %s: %v", pod.Name, containerName, err)
	}

	return &KubernetesLogSource{
		clientset:     clientset,
		namespace:     namespace,
		logStream:     cls,
		podName:       pod.Name,
		containerName: containerName,
		serviceName:   serviceName,
	}
}

func getContainerLogStream(ctx context.Context, namespace, podName, containerName string, clientset *kubernetes.Clientset) (containerLogStream, error) {
	logOptions := &corev1.PodLogOptions{
		Follow:    true,
		TailLines: ptr.To(int64(10)), // Start with last 10 lines
		Container: containerName,
	}

	// Wait for the pod to start
	for {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		if pod.Status.Phase == corev1.PodRunning {
			break
		}
		log.Printf("Waiting for pod %s to be running (current phase: %s)", podName, pod.Status.Phase)
		time.Sleep(5 * time.Second)
	}

	// Create log stream
	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, logOptions)
	logStream, err := req.Stream(ctx)
	if err != nil {
		return containerLogStream{}, fmt.Errorf("failed to create log stream for pod %s container %s: %v", podName, containerName, err)
	}
	log.Printf("Started log stream for pod %s container %s", podName, containerName)

	return containerLogStream{
		stream:  logStream,
		scanner: bufio.NewScanner(logStream),
	}, nil
}

func createClientset(kubeconfigPath string) (*kubernetes.Clientset, bool, error) {
	var config *rest.Config
	var err error
	var inCluster bool

	// Try in-cluster config first (when running inside Kubernetes)
	config, err = rest.InClusterConfig()
	if err != nil {
		inCluster = false
		// If in-cluster config fails, try to use local kubeconfig for development
		log.Printf("Failed to get in-cluster config, trying local kubeconfig: %v", err)

		// Use provided kubeconfig path or default location
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, inCluster, fmt.Errorf("failed to create kubernetes config from kubeconfig: %v", err)
		}
	} else {
		log.Printf("Using in-cluster kubernetes config")
		inCluster = true
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, inCluster, fmt.Errorf("failed to create kubernetes client: %v", err)
	}
	return clientset, inCluster, nil
}

func determineNamespace(ns string, inCluster bool) string {
	if ns != "" {
		return ns
	}
	// Default to "default" namespace if none specified.
	ns = "default"
	if !inCluster {
		return ns
	}
	// When running in-cluster, try to read the current namespace.
	if namespaceBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		ns = string(namespaceBytes)
	}
	return ns
}

func getLogTargets(ctx context.Context, clientset *kubernetes.Clientset, namespace, serviceName, podName string) ([]corev1.Pod, error) {
	var pods []corev1.Pod
	var err error

	if podName != "" {
		pods, err = getLogTargetsFromPodName(ctx, clientset, namespace, podName)
	} else if serviceName != "" {
		pods, err = getLogTargetsFromService(ctx, clientset, namespace, serviceName)
	}
	if err != nil {
		return nil, err
	}

	if len(pods) == 0 {
		return nil, fmt.Errorf("no pods found")
	}

	return pods, nil
}

func getLogTargetsFromPodName(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName string) ([]corev1.Pod, error) {
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod %s in namespace %s: %v", podName, namespace, err)
	}
	return []corev1.Pod{*pod}, nil
}

func getLogTargetsFromService(ctx context.Context, clientset *kubernetes.Clientset, namespace, serviceName string) ([]corev1.Pod, error) {
	svc, err := clientset.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get service %s in namespace %s: %v", serviceName, namespace, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return nil, fmt.Errorf("service %s in namespace %s has no selector", serviceName, namespace)
	}
	selector := labels.Set(svc.Spec.Selector).AsSelector().String()
	pods, err := getLogTargetsFromLabelSelector(ctx, clientset, namespace, selector)
	if err != nil {
		return nil, err
	}

	for {
		if len(pods) == 0 {
			continue
		}
		areReplicasReady, err := areReplicasReady(ctx, clientset, pods[0])
		if err != nil {
			return nil, err
		}
		if areReplicasReady {
			break
		}
		log.Printf("Waiting for pods of service %s to be ready", serviceName)
		time.Sleep(5 * time.Second)
	}
	return pods, nil
}

func getLogTargetsFromLabelSelector(ctx context.Context, clientset *kubernetes.Clientset, namespace, labelSelector string) ([]corev1.Pod, error) {
	podList, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods with label selector %s in namespace %s: %v", labelSelector, namespace, err)
	}
	return podList.Items, nil
}

func areReplicasReady(ctx context.Context, clientset *kubernetes.Clientset, pod corev1.Pod) (bool, error) {
	for _, owner := range pod.OwnerReferences {
		switch owner.Kind {
		case "ReplicaSet":
			return isReplicaSetReady(ctx, clientset, owner.Name, pod.Namespace)
		case "StatefulSet":
			return isStatefulSetReady(ctx, clientset, owner.Name, pod.Namespace)
		case "DaemonSet":
			return isDaemonSetReady(ctx, clientset, owner.Name, pod.Namespace)
		default:
			return true, nil // Not a controller we care about
		}
	}
	// sanity
	return true, nil
}

func isReplicaSetReady(ctx context.Context, clientset *kubernetes.Clientset, name, namespace string) (bool, error) {
	rs, err := clientset.AppsV1().ReplicaSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get ReplicaSet %s in namespace %s: %v", name, namespace, err)
	}
	return rs.Status.ReadyReplicas == *rs.Spec.Replicas, nil
}

func isStatefulSetReady(ctx context.Context, clientset *kubernetes.Clientset, name, namespace string) (bool, error) {
	ss, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get StatefulSet %s in namespace %s: %v", name, namespace, err)
	}
	return ss.Status.ReadyReplicas == *ss.Spec.Replicas, nil
}

func isDaemonSetReady(ctx context.Context, clientset *kubernetes.Clientset, name, namespace string) (bool, error) {
	ds, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get DaemonSet %s in namespace %s: %v", name, namespace, err)
	}
	return ds.Status.NumberReady == ds.Status.DesiredNumberScheduled, nil
}

func (s *KubernetesLogSource) initializeLogStream(ctx context.Context) error {
	if s.logStream.stream != nil {
		return nil // Already initialized
	}
	newStream, err := getContainerLogStream(ctx, s.namespace, s.podName, s.containerName, s.clientset)
	if err != nil {
		return fmt.Errorf("failed to initialize log stream for pod %s container %s: %v", s.podName, s.containerName, err)
	}
	s.logStream = newStream
	return nil
}

func (s *KubernetesLogSource) Close() error {
	if s.logStream.stream == nil {
		return nil
	}
	return s.logStream.stream.Close()
}

func (s *KubernetesLogSource) Path() string {
	namespace := s.namespace
	if namespace == "" {
		namespace = "default"
	}
	return fmt.Sprintf("kubernetes://%s/%s/%s", namespace, s.podName, s.containerName)
}

func (s *KubernetesLogSource) Read(ctx context.Context) (string, error) {
	// Ensure log stream is initialized
	if err := s.initializeLogStream(ctx); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
		if s.logStream.scanner.Scan() {
			line := s.logStream.scanner.Text()
			return line, nil
		}
		// The pod might have restarted or the stream might have been closed.
		// The stream will be re-initialized on the next Read() call.
		s.logStream.stream.Close()
		s.logStream.stream = nil
		s.logStream.scanner = nil
		return "", nil
	}
}

func (s *KubernetesLogSource) ConstLabels() prometheus.Labels {
	labels := prometheus.Labels{}
	if s.namespace != "" {
		labels["namespace"] = s.namespace
	}
	if s.podName != "" {
		labels["pod"] = s.podName
	}
	if s.containerName != "" {
		labels["container"] = s.containerName
	}
	return labels
}

func (s *KubernetesLogSource) RemoteAddr() string {
	return fmt.Sprintf("%s.%s.%s.svc.cluster.local", s.podName, s.serviceName, s.namespace)
}

// kubernetesLogSourceFactory is a factory that can create Kubernetes log sources
// from command line flags.
type kubernetesLogSourceFactory struct {
	LogSourceFactoryDefaults
	clientset      *kubernetes.Clientset
	namespace      string
	podName        string
	serviceName    string
	containerName  string
	kubeconfigPath string
	watchedPods    []string
	enable         bool
}

func (f *kubernetesLogSourceFactory) Init(app *kingpin.Application) {
	app.Flag("kubernetes.enable", "Read from Kubernetes pod logs instead of log").Default("false").BoolVar(&f.enable)
	app.Flag("kubernetes.namespace", "Kubernetes namespace to read logs from (optional, defaults to current namespace when in-cluster or 'default').").Envar("KUBERNETES_NAMESPACE").StringVar(&f.namespace)
	app.Flag("kubernetes.pod-name", "Specific pod name to read logs from (alternative to label-selector).").Envar("KUBERNETES_POD_NAME").StringVar(&f.podName)
	app.Flag("kubernetes.service", "Name of the service selecting the postfix pods").Envar("KUBERNETES_SERVICE").StringVar(&f.serviceName)
	app.Flag("kubernetes.container", "Container name to read logs from (optional, reads from all containers if not specified).").Envar("KUBERNETES_CONTAINER").StringVar(&f.containerName)
	app.Flag("kubernetes.kubeconfig", "Path to kubeconfig file").Envar("KUBERNETES_KUBECONFIG").Default("~/.kube/config").StringVar(&f.kubeconfigPath)
}

func (f *kubernetesLogSourceFactory) New(ctx context.Context) ([]LogSourceCloser, error) {
	if !f.enable {
		return nil, nil
	}
	// Must specify either pod name or label selector (but not both)
	if f.podName == "" && f.serviceName == "" {
		return nil, nil // Not configured
	}

	if f.podName != "" && f.serviceName != "" {
		return nil, fmt.Errorf("cannot specify both pod name and label selector, choose one")
	}

	// Create the clientset
	clientset, inCluster, err := createClientset(f.kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}
	f.clientset = clientset

	namespace := determineNamespace(f.namespace, inCluster)
	f.namespace = namespace
	log.Printf("Using namespace: %s, in-cluster: %t", namespace, inCluster)

	pods, err := getLogTargets(ctx, clientset, namespace, f.serviceName, f.podName)
	if err != nil {
		return nil, err
	}
	log.Printf("Found %d pods to read logs from", len(pods))

	var logSources []LogSourceCloser
	var logSourcesChan = make(chan LogSourceCloser)
	var wg sync.WaitGroup
	wg.Add(len(pods))
	watchedPods := make([]string, 0, len(pods))
	for _, pod := range pods {
		watchedPods = append(watchedPods, pod.Name)
		go func(pod corev1.Pod) {
			defer wg.Done()
			srcs := NewKubernetesLogSource(ctx, namespace, f.serviceName, f.containerName, pod, clientset)
			for _, src := range srcs {
				logSourcesChan <- src
			}
		}(pod)
	}
	slices.Sort(watchedPods)
	f.watchedPods = watchedPods
	go func() {
		wg.Wait()
		close(logSourcesChan)
	}()
	for src := range logSourcesChan {
		logSources = append(logSources, src)
	}
	return logSources, nil
}

func (f *kubernetesLogSourceFactory) Watchdog(ctx context.Context) bool {
	if !f.enable || f.clientset == nil {
		return false
	}

	pods, err := getLogTargets(ctx, f.clientset, f.namespace, f.serviceName, f.podName)
	if err != nil {
		log.Printf("Kubernetes watchdog: failed to get log targets: %v", err)
		// do not restart exporter if we cannot get log targets as this might be a transient error
		return false
	}

	var currentPodNames []string
	for _, pod := range pods {
		currentPodNames = append(currentPodNames, pod.Name)
	}
	slices.Sort(currentPodNames)
	return !slices.Equal(f.watchedPods, currentPodNames)
}

func init() {
	RegisterLogSourceFactory(&kubernetesLogSourceFactory{})
}
