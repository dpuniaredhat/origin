package disruptionimageregistry

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/openshift/origin/pkg/invariantlibrary/disruptionlibrary"

	routev1 "github.com/openshift/api/route/v1"
	routeclient "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/origin/pkg/invariants"
	"github.com/openshift/origin/pkg/monitor/backenddisruption"
	"github.com/openshift/origin/pkg/monitor/monitorapi"
	"github.com/openshift/origin/pkg/test/ginkgo/junitapi"
	"github.com/openshift/origin/test/extended/util/imageregistryutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	newConnectionTestName    = "[sig-imageregistry] Image registry remains available using new connections"
	reusedConnectionTestName = "[sig-imageregistry] Image registry remains available using reused connections"
)

type availability struct {
	kubeClient         kubernetes.Interface
	routeClient        routeclient.Interface
	imageRegistryRoute *routev1.Route

	disruptionChecker *disruptionlibrary.Availability
	suppressJunit     bool
}

func NewAvailabilityInvariant() invariants.InvariantTest {
	return &availability{}
}

func NewRecordAvailabilityOnly() invariants.InvariantTest {
	return &availability{
		suppressJunit: true,
	}
}

func (w *availability) StartCollection(ctx context.Context, adminRESTConfig *rest.Config, recorder monitorapi.RecorderWriter) error {
	var err error

	w.kubeClient, err = kubernetes.NewForConfig(adminRESTConfig)
	if err != nil {
		return err
	}
	w.routeClient, err = routeclient.NewForConfig(adminRESTConfig)
	if err != nil {
		return err
	}

	w.imageRegistryRoute, err = imageregistryutil.ExposeImageRegistryGenerateName(ctx, w.routeClient, "test-disruption-")
	if err != nil {
		return err
	}

	baseURL := fmt.Sprintf("https://%s", w.imageRegistryRoute.Status.Ingress[0].Host)
	namespace := "openshift-image-registry"
	disruptionBackendName := "image-registry"
	path := "/healthz"
	newConnectionDisruptionSampler := backenddisruption.NewSimpleBackendWithLocator(
		monitorapi.LocateRouteForDisruptionCheck(namespace, "test-disruption-new", disruptionBackendName, monitorapi.NewConnectionType),
		baseURL,
		disruptionBackendName,
		path,
		monitorapi.NewConnectionType)

	reusedConnectionDisruptionSampler := backenddisruption.NewSimpleBackendWithLocator(
		monitorapi.LocateRouteForDisruptionCheck(namespace, "test-disruption-reused", disruptionBackendName, monitorapi.ReusedConnectionType),
		baseURL,
		disruptionBackendName,
		path,
		monitorapi.ReusedConnectionType)

	w.disruptionChecker = disruptionlibrary.NewAvailabilityInvariant(
		newConnectionTestName, reusedConnectionTestName,
		newConnectionDisruptionSampler, reusedConnectionDisruptionSampler,
	)
	if err := w.disruptionChecker.StartCollection(ctx, adminRESTConfig, recorder); err != nil {
		return err
	}

	return nil
}

func (w *availability) CollectData(ctx context.Context, storageDir string, beginning, end time.Time) (monitorapi.Intervals, []*junitapi.JUnitTestCase, error) {
	return w.disruptionChecker.CollectData(ctx)
}

func (*availability) ConstructComputedIntervals(ctx context.Context, startingIntervals monitorapi.Intervals, recordedResources monitorapi.ResourcesMap, beginning, end time.Time) (monitorapi.Intervals, error) {
	return nil, nil
}

func (w *availability) EvaluateTestsFromConstructedIntervals(ctx context.Context, finalIntervals monitorapi.Intervals) ([]*junitapi.JUnitTestCase, error) {
	if w.suppressJunit {
		return nil, nil
	}

	return w.disruptionChecker.EvaluateTestsFromConstructedIntervals(ctx, finalIntervals)
}

func (*availability) WriteContentToStorage(ctx context.Context, storageDir, timeSuffix string, finalIntervals monitorapi.Intervals, finalResourceState monitorapi.ResourcesMap) error {
	return nil
}

func (w *availability) Cleanup(ctx context.Context) error {
	if w.imageRegistryRoute != nil {
		err := w.routeClient.RouteV1().Routes("openshift-image-registry").Delete(ctx, w.imageRegistryRoute.Name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete route: %w", err)
		}
	}

	return nil
}
