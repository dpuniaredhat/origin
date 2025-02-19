package externalservice

import (
	"context"

	"github.com/openshift/origin/pkg/disruption/backend"
	"github.com/openshift/origin/pkg/monitor/backenddisruption"
	"github.com/openshift/origin/pkg/monitor/monitorapi"
	"k8s.io/client-go/rest"
)

const (
	LivenessProbeBackend = "ci-cluster-network-liveness"
	externalServiceURL   = "http://static.redhat.com/test/rhel-networkmanager.txt"
)

// StartExternalServiceMonitoring runs a monitor against a public http endpoint we can poll to ensure the cluster
// running the tests can reach and external service. This is used to compare to disruption observed against the
// ephemeral cluster under test, and compare to see if the build cluster where the tests are running is having
// network issues, or if we're seeing real disruption.
func StartExternalServiceMonitoring(ctx context.Context, m monitorapi.Recorder, clusterConfig *rest.Config, _ backend.LoadBalancerType) error {
	if err := startExternalServiceMonitoringWithNewConnections(ctx, m, clusterConfig); err != nil {
		return err
	}
	if err := startExternalServiceMonitoringWithReusedConnections(ctx, m, clusterConfig); err != nil {
		return err
	}
	return nil
}

func startExternalServiceMonitoringWithNewConnections(ctx context.Context, m monitorapi.Recorder, clusterConfig *rest.Config) error {
	backendSampler := backenddisruption.NewSimpleBackend(
		externalServiceURL,
		LivenessProbeBackend,
		"",
		monitorapi.NewConnectionType)
	return backendSampler.StartEndpointMonitoring(ctx, m, nil)
}

func startExternalServiceMonitoringWithReusedConnections(ctx context.Context, m monitorapi.Recorder, clusterConfig *rest.Config) error {
	backendSampler := backenddisruption.NewSimpleBackend(
		externalServiceURL,
		LivenessProbeBackend,
		"",
		monitorapi.ReusedConnectionType)
	return backendSampler.StartEndpointMonitoring(ctx, m, nil)
}
