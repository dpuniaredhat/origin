package monitor

import (
	"fmt"
	"strings"
	"sync"

	"github.com/openshift/origin/pkg/monitor/monitorapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

func GetMonitorRESTConfig() (*rest.Config, error) {
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
	clusterConfig, err := cfg.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("could not load client configuration: %v", err)
	}

	return clusterConfig, nil
}

func findContainerStatus(status []corev1.ContainerStatus, name string, position int) *corev1.ContainerStatus {
	if position < len(status) {
		if status[position].Name == name {
			return &status[position]
		}
	}
	for i := range status {
		if status[i].Name == name {
			return &status[i]
		}
	}
	return nil
}

func findNodeCondition(status []corev1.NodeCondition, name corev1.NodeConditionType, position int) *corev1.NodeCondition {
	if position < len(status) {
		if status[position].Type == name {
			return &status[position]
		}
	}
	for i := range status {
		if status[i].Type == name {
			return &status[i]
		}
	}
	return nil
}

func locateEvent(event *corev1.Event) string {
	if len(event.InvolvedObject.Namespace) > 0 {
		if len(event.Source.Host) > 0 && event.InvolvedObject.Kind != "Node" {
			return fmt.Sprintf("ns/%s %s/%s node/%s", event.InvolvedObject.Namespace, strings.ToLower(event.InvolvedObject.Kind), event.InvolvedObject.Name, event.Source.Host)
		}
		return fmt.Sprintf("ns/%s %s/%s", event.InvolvedObject.Namespace, strings.ToLower(event.InvolvedObject.Kind), event.InvolvedObject.Name)
	}
	if len(event.Source.Host) > 0 && event.InvolvedObject.Kind != "Node" {
		return fmt.Sprintf("%s/%s node/%s", strings.ToLower(event.InvolvedObject.Kind), event.InvolvedObject.Name, event.Source.Host)
	}
	return fmt.Sprintf("%s/%s", strings.ToLower(event.InvolvedObject.Kind), event.InvolvedObject.Name)
}

type errorRecordingListWatcher struct {
	lw cache.ListerWatcher

	recorder monitorapi.Recorder

	lock          sync.Mutex
	receivedError bool
}

func NewErrorRecordingListWatcher(recorder monitorapi.Recorder, lw cache.ListerWatcher) cache.ListerWatcher {
	return &errorRecordingListWatcher{
		lw:       lw,
		recorder: recorder,
	}
}

func (w *errorRecordingListWatcher) List(options metav1.ListOptions) (runtime.Object, error) {
	obj, err := w.lw.List(options)
	w.handle(err)
	return obj, err
}

func (w *errorRecordingListWatcher) Watch(options metav1.ListOptions) (watch.Interface, error) {
	obj, err := w.lw.Watch(options)
	w.handle(err)
	return obj, err
}

func (w *errorRecordingListWatcher) handle(err error) {
	w.lock.Lock()
	defer w.lock.Unlock()
	if err != nil {
		if !w.receivedError {
			w.recorder.Record(monitorapi.Condition{
				Level:   monitorapi.Error,
				Locator: "kube-apiserver",
				Message: fmt.Sprintf("failed contacting the API: %v", err),
			})
		}
		w.receivedError = true
	} else {
		w.receivedError = false
	}
}
