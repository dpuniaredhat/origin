package ginkgo

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openshift/origin/pkg/defaultinvariants"
	"github.com/openshift/origin/pkg/duplicateevents"
	"github.com/openshift/origin/pkg/monitor"
	"github.com/openshift/origin/pkg/monitor/intervalcreation"
	"github.com/openshift/origin/pkg/monitor/monitorapi"
	"github.com/openshift/origin/pkg/monitor/nodedetails"
	"github.com/openshift/origin/test/extended/util/disruption/controlplane"
	"github.com/openshift/origin/test/extended/util/disruption/externalservice"
	"github.com/openshift/origin/test/extended/util/disruption/frontends"
	"github.com/sirupsen/logrus"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

type RunDataWriter interface {
	WriteRunData(artifactDir string, recordedResources monitorapi.ResourcesMap, events monitorapi.Intervals, timeSuffix string) error
}

type RunDataWriterFunc func(artifactDir string, recordedResources monitorapi.ResourcesMap, events monitorapi.Intervals, timeSuffix string) error

func (fn RunDataWriterFunc) WriteRunData(artifactDir string, recordedResources monitorapi.ResourcesMap, events monitorapi.Intervals, timeSuffix string) error {
	return fn(artifactDir, recordedResources, events, timeSuffix)
}

type MonitorEventsOptions struct {
	monitor   *monitor.Monitor
	startTime *time.Time
	endTime   *time.Time
	// recordedEvents is written during Stop
	recordedEvents monitorapi.Intervals
	// recordedResource is written during Stop
	recordedResources monitorapi.ResourcesMap
	// auditLogSummary is written during Stop
	auditLogSummary *nodedetails.AuditLogSummary

	genericclioptions.IOStreams
	storageDir                 string
	clusterStabilityDuringTest defaultinvariants.ClusterStabilityDuringTest
}

func NewMonitorEventsOptions(streams genericclioptions.IOStreams, storageDir string, clusterStabilityDuringTest defaultinvariants.ClusterStabilityDuringTest) *MonitorEventsOptions {
	return &MonitorEventsOptions{
		IOStreams:                  streams,
		storageDir:                 storageDir,
		clusterStabilityDuringTest: clusterStabilityDuringTest,
	}
}

func (o *MonitorEventsOptions) Start(ctx context.Context, restConfig *rest.Config) (monitorapi.Recorder, error) {
	if o.monitor != nil {
		return nil, fmt.Errorf("already started")
	}
	t := time.Now()
	o.startTime = &t

	m := monitor.NewMonitor(
		restConfig,
		o.storageDir,
		[]monitor.StartEventIntervalRecorderFunc{
			controlplane.StartAPIMonitoringUsingNewBackend,
			frontends.StartAllIngressMonitoring,
			externalservice.StartExternalServiceMonitoring,
		},
		defaultinvariants.NewInvariantsFor(o.clusterStabilityDuringTest),
	)
	err := m.Start(ctx)
	if err != nil {
		return nil, err
	}
	o.monitor = m

	return m, nil
}

func (o *MonitorEventsOptions) SetIOStreams(streams genericclioptions.IOStreams) {
	o.IOStreams = streams
}

var removeNTimes = regexp.MustCompile(`\s+\(\d+ times\)`)
var removeHmsg = regexp.MustCompile(`\s+(hmsg/[0-9a-f]+)`)

// markMissedPathologicalEvents goes through the list of events looking for all events marked
// as "pathological/true" (this implies the event was previously unknown and happened > 20 times).
// For each of those events, this function looks for previous occurrences of that event (with
// times < 20) and marks them as "pathological/true" so that all occurences of that event will
// show in the spyglass chart.
func markMissedPathologicalEvents(events monitorapi.Intervals) {
	// Get the list of events already marked (abbreviated as "am") as pathological/true (this implies times > 20).
	amPathoEvents := map[string]string{}

	for _, pathologicalEvent := range events {
		if !strings.Contains(pathologicalEvent.Message, duplicateevents.PathologicalMark) {
			// We only are interested in those EventIntervals with pathological/true
			continue
		}
		if strings.Contains(pathologicalEvent.Message, duplicateevents.InterestingMark) {
			// If this message is known, we don't need to process it because we already
			// created an interval when it came in initially.
			continue
		}

		// Events marked as pathological/true have the mark and "n times" number on the message
		// and the locator ends with hmsg/xxxxxxxxxx.
		// Events that are to be marked don't have the pathological/true mark and don't have the hmsg (message hash).
		msgWithoutTimes := removeNTimes.ReplaceAllString(pathologicalEvent.Message, "")
		locWithoutHmsg := removeHmsg.ReplaceAllString(pathologicalEvent.Locator, "")
		amPathoEvents[msgWithoutTimes+locWithoutHmsg] = pathologicalEvent.Locator
	}
	logrus.Infof("Number of pathological keys: %d", len(amPathoEvents))

	for i, scannedEvent := range events {
		msgWithPathoMark := fmt.Sprintf("%s %s", duplicateevents.PathologicalMark, removeNTimes.ReplaceAllString(scannedEvent.Message, ""))
		if pLocator, ok := amPathoEvents[msgWithPathoMark+scannedEvent.Locator]; ok {
			// This is a match, so update the event with the pathological/true mark and locator that contains the hmsg (message hash).
			events[i].Message = fmt.Sprintf("%s %s", duplicateevents.PathologicalMark, scannedEvent.Message)
			events[i].Locator = pLocator
			logrus.Infof("Found a times match: Locator=%s Message=%s", events[i].Locator, events[i].Message)
		}
	}
}

// Stop mutates the method receiver so you shouldn't call it multiple times.
func (o *MonitorEventsOptions) Stop(ctx context.Context, restConfig *rest.Config, artifactDir string) error {
	if o.monitor == nil {
		return fmt.Errorf("not started")
	}
	if o.endTime != nil {
		return fmt.Errorf("already ended")
	}

	t := time.Now()
	o.endTime = &t
	o.recordedResources = o.monitor.CurrentResourceState()

	var err error

	cleanupContext, cleanupCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cleanupCancel()
	if err := o.monitor.Stop(cleanupContext); err != nil {
		fmt.Fprintf(os.Stderr, "error cleaning up, still reporting as best as possible: %v\n", err)
	}

	// make sure that the end time is *after* the final availability samples.
	fromTime, endTime := *o.startTime, *o.endTime

	events := o.monitor.Intervals(fromTime, endTime)

	markMissedPathologicalEvents(events)

	// this happens before calculation because events collected here could be used to drive later calculations
	o.auditLogSummary, events, err = intervalcreation.InsertIntervalsFromCluster(ctx, restConfig, events, o.recordedResources, fromTime, endTime)
	if err != nil {
		fmt.Fprintf(o.ErrOut, "InsertIntervalsFromCluster error but continuing processing: %v", err)
	}
	o.monitor.AddIntervals(events...) // add intervals to the recorded events, not just the random copy
	// add events from alerts so we can create the intervals
	alertEventIntervals, err := monitor.FetchEventIntervalsForAllAlerts(ctx, restConfig, *o.startTime)
	if err != nil {
		fmt.Fprintf(o.ErrOut, "FetchEventIntervalsForAllAlerts error but continuing processing: %v", err)
	}
	events = append(events, alertEventIntervals...)
	o.monitor.AddIntervals(alertEventIntervals...) // add intervals to the recorded events, not just the random copy
	o.monitor.AddIntervals(intervalcreation.CalculateMoreIntervals(events, o.recordedResources, fromTime, endTime)...)

	sort.Sort(events)
	events.Clamp(*o.startTime, *o.endTime)

	o.recordedEvents = events

	return nil
}

func (m *MonitorEventsOptions) SerializeResults(ctx context.Context, junitSuiteName, timeSuffix string) error {
	return m.monitor.SerializeResults(ctx, junitSuiteName, timeSuffix)
}

func (o *MonitorEventsOptions) GetEvents() monitorapi.Intervals {
	return o.recordedEvents
}

func (o *MonitorEventsOptions) GetRecordedResources() monitorapi.ResourcesMap {
	return o.recordedResources
}

func (o *MonitorEventsOptions) GetStartTime() *time.Time {
	return o.startTime
}

// WriteRunDataToArtifactsDir attempts to write useful run data to the specified directory.
func (o *MonitorEventsOptions) WriteRunDataToArtifactsDir(artifactDir string, timeSuffix string) error {
	if o.endTime == nil {
		return fmt.Errorf("not ended")
	}

	errs := []error{}

	// use custom sorting here so that we can prioritize the sort order to make the intervals html page as readable
	// as possible. This makes the events *not* sorted by time.
	events := make([]monitorapi.Interval, len(o.recordedEvents))
	for i := range o.recordedEvents {
		events[i] = o.recordedEvents[i]
	}
	sort.Stable(monitorapi.ByTimeWithNamespacedPods(events))

	// TODO: Re-sort for loki, where we need these to go    in chronologically, and based on the
	// above comments that would not be the case otherwise.
	sort.Sort(monitorapi.Intervals(events))
	err := monitor.UploadIntervalsToLoki(events)
	if err != nil {
		// Best effort, we do not want to error out here:
		logrus.WithError(err).Warn("unable to upload intervals to loki")
	}

	if o.auditLogSummary != nil {
		if currErr := nodedetails.WriteAuditLogSummary(artifactDir, timeSuffix, o.auditLogSummary); currErr != nil {
			errs = append(errs, currErr)
		}
	}

	return utilerrors.NewAggregate(errs)
}
