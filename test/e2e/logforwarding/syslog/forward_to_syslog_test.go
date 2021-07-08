package syslog

import (
	"fmt"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/ViaQ/logerr/log"
	logging "github.com/openshift/cluster-logging-operator/pkg/apis/logging/v1"
	"github.com/openshift/cluster-logging-operator/test/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// grep "pod_name"=>"<generator-pod-name>" <syslog-log-filename>
	rsyslogFormatStr = `grep %s %%s| grep pod_name | tail -n 1 | awk -F' ' '{print %s}'`
)

var _ = Describe("[ClusterLogForwarder] Forwards logs", func() {
	_, filename, _, _ := runtime.Caller(0)
	log.Info("Running ", "filename", filename)
	var (
		err              error
		syslogDeployment *apps.Deployment
		e2e              = helpers.NewE2ETestFramework()
		testDir          string
		forwarder        *logging.ClusterLogForwarder
		generatorPayload map[string]string
		waitlogs         string
		logGenPod        string
		logGenNS         string
	)
	BeforeEach(func() {
		generatorPayload = map[string]string{
			"msgcontent": "My life is my message",
		}
	})
	JustBeforeEach(func() {
		if logGenNS, logGenPod, err = e2e.DeployJsonLogGenerator(generatorPayload); err != nil {
			log.Error(err, "unable to deploy log generator.")
		}
		log.Info("log generator pod: ", "podname", logGenPod)
		testDir = filepath.Dir(filename)
		// wait for current log-generator's logs to appear in syslog
		waitlogs = fmt.Sprintf(`[ $(grep %s %%s |grep pod_name| wc -l) -gt 0 ]`, logGenPod)
	})
	Describe("when the output is a third-party managed syslog", func() {
		BeforeEach(func() {
			cr := helpers.NewClusterLogging(helpers.ComponentTypeCollector)
			if err := e2e.CreateClusterLogging(cr); err != nil {
				Fail(fmt.Sprintf("Unable to create an instance of cluster logging: %v", err))
			}
		})

		Describe("with rfc3164", func() {
			BeforeEach(func() {
				forwarder = &logging.ClusterLogForwarder{
					TypeMeta: metav1.TypeMeta{
						Kind:       logging.ClusterLogForwarderKind,
						APIVersion: logging.SchemeGroupVersion.String(),
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "instance",
					},
					Spec: logging.ClusterLogForwarderSpec{
						Outputs: []logging.OutputSpec{
							{
								Name: "syslogout",
								Type: "syslog",
								OutputTypeSpec: logging.OutputTypeSpec{
									Syslog: &logging.Syslog{
										Facility: "user",
										Severity: "debug",
										RFC:      "RFC3164",
										Tag:      "mytag",
									},
								},
							},
						},
						Pipelines: []logging.PipelineSpec{
							{
								Name:       "test-app",
								OutputRefs: []string{"syslogout"},
								InputRefs:  []string{"application"},
							},
						},
					},
				}
			})
			Describe("syslog payload", func() {
				Context("with addLogSourceToMessage flag", func() {
					Context("for infra logs", func() {
						BeforeEach(func() {
							forwarder.Spec.Pipelines[0].InputRefs = []string{"infrastructure"}
						})
						It("should add the originating process name and id to journal log message", func() {
							if syslogDeployment, err = e2e.DeploySyslogReceiver(testDir, corev1.ProtocolTCP, false, helpers.RFC3164); err != nil {
								Fail(fmt.Sprintf("Unable to deploy syslog receiver: %v", err))
							}
							forwarder.Spec.Outputs[0].URL = fmt.Sprintf("tls://%s.%s.svc:24224", syslogDeployment.ObjectMeta.Name, syslogDeployment.Namespace)
							forwarder.Spec.Outputs[0].Syslog.RFC = helpers.RFC3164.String()
							forwarder.Spec.Outputs[0].Syslog.PayloadKey = "message"
							forwarder.Spec.Outputs[0].Syslog.AddLogSource = true
							if err := e2e.CreateClusterLogForwarder(forwarder); err != nil {
								Fail(fmt.Sprintf("Unable to create an instance of logforwarder: %v", err))
							}
							components := []helpers.LogComponentType{helpers.ComponentTypeCollector}
							for _, component := range components {
								if err := e2e.WaitFor(component); err != nil {
									Fail(fmt.Sprintf("Failed waiting for component %s to be ready: %v", component, err))
								}
							}
							logStore := e2e.LogStores[syslogDeployment.GetName()]
							Expect(logStore.HasInfraStructureLogs(helpers.DefaultWaitForLogsTimeout)).To(BeTrue(), "Expected to find stored infrastructure logs")
							_, _ = logStore.GrepLogs(waitlogs, helpers.DefaultWaitForLogsTimeout)
							grepMsgContent := fmt.Sprintf(`grep %s %%s | tail -n 1 | awk -F' ' '{ print $4; }'`, logGenPod)
							Expect(logStore.GrepLogs(grepMsgContent, helpers.DefaultWaitForLogsTimeout)).To(Equal("mytag"), "Expected: mytag")
							waitjournallogs := `[ $(grep -v pod_name %s | wc -l) -gt 0 ]`
							_, _ = logStore.GrepLogs(waitjournallogs, helpers.DefaultWaitForLogsTimeout)
							grepMsgContent = `grep -v pod_name %s | tail -n 1 | awk -F' ' '{ print $4; }'`
							Expect(logStore.GrepLogs(grepMsgContent, helpers.DefaultWaitForLogsTimeout)).To(Not(Equal("mytag")), "Expected: not mytag")
						})
					})
				})
			})
		})
		AfterEach(func() {
			e2e.Cleanup()
			e2e.WaitForCleanupCompletion(logGenNS, []string{"test"})
			e2e.WaitForCleanupCompletion(helpers.OpenshiftLoggingNS, []string{"fluentd", "syslog-receiver"})
			generatorPayload = map[string]string{}
		})
	})
})

