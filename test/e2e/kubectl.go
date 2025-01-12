/*
Copyright 2015 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	nautilusImage            = "gcr.io/google_containers/update-demo:nautilus"
	kittenImage              = "gcr.io/google_containers/update-demo:kitten"
	updateDemoSelector       = "name=update-demo"
	updateDemoContainer      = "update-demo"
	frontendSelector         = "name=frontend"
	redisMasterSelector      = "name=redis-master"
	redisSlaveSelector       = "name=redis-slave"
	kubectlProxyPort         = 8011
	guestbookStartupTimeout  = 10 * time.Minute
	guestbookResponseTimeout = 3 * time.Minute
	simplePodSelector        = "name=nginx"
	simplePodName            = "nginx"
	nginxDefaultOutput       = "Welcome to nginx!"
	simplePodPort            = 80
)

var (
	portForwardRegexp = regexp.MustCompile("Forwarding from 127.0.0.1:([0-9]+) -> 80")
	proxyRegexp       = regexp.MustCompile("Starting to serve on 127.0.0.1:([0-9]+)")
)

var _ = Describe("Kubectl client", func() {
	defer GinkgoRecover()
	var c *client.Client
	var ns string
	var testingNs *api.Namespace
	BeforeEach(func() {
		var err error
		c, err = loadClient()
		expectNoError(err)
		testingNs, err = createTestingNS("kubectl", c)
		ns = testingNs.Name
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		By(fmt.Sprintf("Destroying namespace for this suite %v", ns))
		if err := c.Namespaces().Delete(ns); err != nil {
			Failf("Couldn't delete ns %s", err)
		}
	})

	Describe("Update Demo", func() {
		var updateDemoRoot, nautilusPath, kittenPath string
		BeforeEach(func() {
			updateDemoRoot = filepath.Join(testContext.RepoRoot, "docs/user-guide/update-demo")
			nautilusPath = filepath.Join(updateDemoRoot, "nautilus-rc.yaml")
			kittenPath = filepath.Join(updateDemoRoot, "kitten-rc.yaml")
		})

		It("should create and stop a replication controller", func() {
			defer cleanup(nautilusPath, ns, updateDemoSelector)

			By("creating a replication controller")
			runKubectl("create", "-f", nautilusPath, fmt.Sprintf("--namespace=%v", ns))
			validateController(c, nautilusImage, 2, "update-demo", updateDemoSelector, getUDData("nautilus.jpg", ns), ns)
		})

		It("should scale a replication controller", func() {
			defer cleanup(nautilusPath, ns, updateDemoSelector)

			By("creating a replication controller")
			runKubectl("create", "-f", nautilusPath, fmt.Sprintf("--namespace=%v", ns))
			validateController(c, nautilusImage, 2, "update-demo", updateDemoSelector, getUDData("nautilus.jpg", ns), ns)
			By("scaling down the replication controller")
			runKubectl("scale", "rc", "update-demo-nautilus", "--replicas=1", "--timeout=5m", fmt.Sprintf("--namespace=%v", ns))
			validateController(c, nautilusImage, 1, "update-demo", updateDemoSelector, getUDData("nautilus.jpg", ns), ns)
			By("scaling up the replication controller")
			runKubectl("scale", "rc", "update-demo-nautilus", "--replicas=2", "--timeout=5m", fmt.Sprintf("--namespace=%v", ns))
			validateController(c, nautilusImage, 2, "update-demo", updateDemoSelector, getUDData("nautilus.jpg", ns), ns)
		})

		It("should do a rolling update of a replication controller", func() {
			By("creating the initial replication controller")
			runKubectl("create", "-f", nautilusPath, fmt.Sprintf("--namespace=%v", ns))
			validateController(c, nautilusImage, 2, "update-demo", updateDemoSelector, getUDData("nautilus.jpg", ns), ns)
			By("rolling-update to new replication controller")
			runKubectl("rolling-update", "update-demo-nautilus", "--update-period=1s", "-f", kittenPath, fmt.Sprintf("--namespace=%v", ns))
			validateController(c, kittenImage, 2, "update-demo", updateDemoSelector, getUDData("kitten.jpg", ns), ns)
			// Everything will hopefully be cleaned up when the namespace is deleted.
		})
	})

	Describe("Guestbook application", func() {
		var guestbookPath string

		BeforeEach(func() {
			guestbookPath = filepath.Join(testContext.RepoRoot, "examples/guestbook")

			// requires ExternalLoadBalancer support
			SkipUnlessProviderIs("gce", "gke", "aws")
		})

		It("should create and stop a working application", func() {
			defer cleanup(guestbookPath, ns, frontendSelector, redisMasterSelector, redisSlaveSelector)

			By("creating all guestbook components")
			runKubectl("create", "-f", guestbookPath, fmt.Sprintf("--namespace=%v", ns))

			By("validating guestbook app")
			validateGuestbookApp(c, ns)
		})
	})

	Describe("Simple pod", func() {
		var podPath string

		BeforeEach(func() {
			podPath = filepath.Join(testContext.RepoRoot, "docs/user-guide/pod.yaml")
			By("creating the pod")
			runKubectl("create", "-f", podPath, fmt.Sprintf("--namespace=%v", ns))
			checkPodsRunningReady(c, ns, []string{simplePodName}, podStartTimeout)

		})
		AfterEach(func() {
			cleanup(podPath, ns, simplePodSelector)
		})

		It("should support exec", func() {
			By("executing a command in the container")
			execOutput := runKubectl("exec", fmt.Sprintf("--namespace=%v", ns), simplePodName, "echo", "running", "in", "container")
			expectedExecOutput := "running in container"
			if execOutput != expectedExecOutput {
				Failf("Unexpected kubectl exec output. Wanted '%s', got '%s'", execOutput, expectedExecOutput)
			}
		})
		It("should support port-forward", func() {
			By("forwarding the container port to a local port")
			cmd := kubectlCmd("port-forward", fmt.Sprintf("--namespace=%v", ns), "-p", simplePodName, fmt.Sprintf(":%d", simplePodPort))
			defer tryKill(cmd)
			// This is somewhat ugly but is the only way to retrieve the port that was picked
			// by the port-forward command. We don't want to hard code the port as we have no
			// way of guaranteeing we can pick one that isn't in use, particularly on Jenkins.
			Logf("starting port-forward command and streaming output")
			stdout, stderr, err := startCmdAndStreamOutput(cmd)
			if err != nil {
				Failf("Failed to start port-forward command: %v", err)
			}
			defer stdout.Close()
			defer stderr.Close()

			buf := make([]byte, 128)
			var n int
			Logf("reading from `kubectl port-forward` command's stderr")
			if n, err = stderr.Read(buf); err != nil {
				Failf("Failed to read from kubectl port-forward stderr: %v", err)
			}
			portForwardOutput := string(buf[:n])
			match := portForwardRegexp.FindStringSubmatch(portForwardOutput)
			if len(match) != 2 {
				Failf("Failed to parse kubectl port-forward output: %s", portForwardOutput)
			}
			By("curling local port output")
			localAddr := fmt.Sprintf("http://localhost:%s", match[1])
			body, err := curl(localAddr)
			Logf("got: %s", body)
			if err != nil {
				Failf("Failed http.Get of forwarded port (%s): %v", localAddr, err)
			}
			if !strings.Contains(body, nginxDefaultOutput) {
				Failf("Container port output missing expected value. Wanted:'%s', got: %s", nginxDefaultOutput, body)
			}
		})
	})

	Describe("Kubectl api-versions", func() {
		It("should check if v1 is in available api versions", func() {
			By("validating api verions")
			output := runKubectl("api-versions")
			if !strings.Contains(output, "Available Server Api Versions:") {
				Failf("Missing caption in kubectl api-versions")
			}
			if !strings.Contains(output, "v1") {
				Failf("No v1 in kubectl api-versions")
			}
		})
	})

	Describe("Kubectl cluster-info", func() {
		It("should check if Kubernetes master services is included in cluster-info", func() {
			By("validating cluster-info")
			output := runKubectl("cluster-info")
			// Can't check exact strings due to terminal controll commands (colors)
			requiredItems := []string{"Kubernetes master", "is running at"}
			if providerIs("gce", "gke") {
				requiredItems = append(requiredItems, "KubeDNS", "Heapster")
			}
			for _, item := range requiredItems {
				if !strings.Contains(output, item) {
					Failf("Missing %s in kubectl cluster-info", item)
				}
			}
		})
	})

	Describe("Kubectl describe", func() {
		It("should check if kubectl describe prints relevant information for rc and pods", func() {
			mkpath := func(file string) string {
				return filepath.Join(testContext.RepoRoot, "examples/guestbook-go", file)
			}
			controllerJson := mkpath("redis-master-controller.json")
			serviceJson := mkpath("redis-master-service.json")

			nsFlag := fmt.Sprintf("--namespace=%v", ns)
			runKubectl("create", "-f", controllerJson, nsFlag)
			runKubectl("create", "-f", serviceJson, nsFlag)

			// Pod
			forEachPod(c, ns, "app", "redis", func(pod api.Pod) {
				output := runKubectl("describe", "pod", pod.Name, nsFlag)
				requiredStrings := [][]string{
					{"Name:", "redis-master-"},
					{"Namespace:", ns},
					{"Image(s):", "redis"},
					{"Node:"},
					{"Labels:", "app=redis", "role=master"},
					{"Status:", "Running"},
					{"Reason:"},
					{"Message:"},
					{"IP:"},
					{"Replication Controllers:", "redis-master"}}
				checkOutput(output, requiredStrings)
			})

			// Rc
			output := runKubectl("describe", "rc", "redis-master", nsFlag)
			requiredStrings := [][]string{
				{"Name:", "redis-master"},
				{"Namespace:", ns},
				{"Image(s):", "redis"},
				{"Selector:", "app=redis,role=master"},
				{"Labels:", "app=redis,role=master"},
				{"Replicas:", "1 current", "1 desired"},
				{"Pods Status:", "1 Running", "0 Waiting", "0 Succeeded", "0 Failed"},
				{"Events:"}}
			checkOutput(output, requiredStrings)

			// Service
			output = runKubectl("describe", "service", "redis-master", nsFlag)
			requiredStrings = [][]string{
				{"Name:", "redis-master"},
				{"Namespace:", ns},
				{"Labels:", "app=redis", "role=master"},
				{"Selector:", "app=redis", "role=master"},
				{"Type:", "ClusterIP"},
				{"IP:"},
				{"Port:", "<unnamed>", "6379/TCP"},
				{"Endpoints:"},
				{"Session Affinity:", "None"}}
			checkOutput(output, requiredStrings)

			// Node
			minions, err := c.Nodes().List(labels.Everything(), fields.Everything())
			Expect(err).NotTo(HaveOccurred())
			node := minions.Items[0]
			output = runKubectl("describe", "node", node.Name)
			requiredStrings = [][]string{
				{"Name:", node.Name},
				{"Labels:"},
				{"CreationTimestamp:"},
				{"Conditions:"},
				{"Type", "Status", "LastHeartbeatTime", "LastTransitionTime", "Reason", "Message"},
				{"Addresses:"},
				{"Capacity:"},
				{"Version:"},
				{"Kernel Version:"},
				{"OS Image:"},
				{"Container Runtime Version:"},
				{"Kubelet Version:"},
				{"Kube-Proxy Version:"},
				{"Pods:"}}
			checkOutput(output, requiredStrings)

			// Namespace
			output = runKubectl("describe", "namespace", ns)
			requiredStrings = [][]string{
				{"Name:", ns},
				{"Labels:"},
				{"Status:", "Active"}}
			checkOutput(output, requiredStrings)

			// Quota and limitrange are skipped for now.
		})
	})

	Describe("Kubectl expose", func() {
		It("should create services for rc", func() {
			mkpath := func(file string) string {
				return filepath.Join(testContext.RepoRoot, "examples/guestbook-go", file)
			}
			controllerJson := mkpath("redis-master-controller.json")
			nsFlag := fmt.Sprintf("--namespace=%v", ns)

			redisPort := 6379
			serviceTimeout := 30 * time.Second

			By("creating Redis RC")
			runKubectl("create", "-f", controllerJson, nsFlag)
			forEachPod(c, ns, "app", "redis", func(pod api.Pod) {
				lookForStringInLog(ns, pod.Name, "redis-master", "The server is now ready to accept connections", podStartTimeout)
			})
			validateService := func(name string, servicePort int, timeout time.Duration) {
				endpointFound := false
				for t := time.Now(); time.Since(t) < timeout; time.Sleep(poll) {
					endpoints, err := c.Endpoints(ns).Get(name)
					Expect(err).NotTo(HaveOccurred())

					ipToPort := getPortsByIp(endpoints.Subsets)
					if len(ipToPort) != 1 {
						Logf("No IP found, retrying")
						continue
					}
					for _, port := range ipToPort {
						if port[0] != redisPort {
							Failf("Wrong endpoint port: %d", port[0])
						}
					}
					endpointFound = true
					break
				}
				if !endpointFound {
					Failf("1 endpoint is expected")
				}
				service, err := c.Services(ns).Get(name)
				Expect(err).NotTo(HaveOccurred())

				if len(service.Spec.Ports) != 1 {
					Failf("1 port is expected")
				}
				port := service.Spec.Ports[0]
				if port.Port != servicePort {
					Failf("Wrong service port: %d", port.Port)
				}
				if port.TargetPort.IntVal != redisPort {
					Failf("Wrong target port: %d")
				}
			}

			By("exposing RC")
			runKubectl("expose", "rc", "redis-master", "--name=rm2", "--port=1234", fmt.Sprintf("--target-port=%d", redisPort), nsFlag)
			waitForService(c, ns, "rm2", true, poll, serviceTimeout)
			validateService("rm2", 1234, serviceTimeout)

			By("exposing service")
			runKubectl("expose", "service", "rm2", "--name=rm3", "--port=2345", fmt.Sprintf("--target-port=%d", redisPort), nsFlag)
			waitForService(c, ns, "rm3", true, poll, serviceTimeout)
			validateService("rm3", 2345, serviceTimeout)
		})
	})

	Describe("Kubectl label", func() {
		var podPath string
		var nsFlag string
		BeforeEach(func() {
			podPath = filepath.Join(testContext.RepoRoot, "docs/user-guide/pod.yaml")
			By("creating the pod")
			nsFlag = fmt.Sprintf("--namespace=%v", ns)
			runKubectl("create", "-f", podPath, nsFlag)
			checkPodsRunningReady(c, ns, []string{simplePodName}, podStartTimeout)
		})
		AfterEach(func() {
			cleanup(podPath, ns, simplePodSelector)
		})

		It("should update the label on a resource", func() {
			labelName := "testing-label"
			labelValue := "testing-label-value"

			By("adding the label " + labelName + " with value " + labelValue + " to a pod")
			runKubectl("label", "pods", simplePodName, labelName+"="+labelValue, nsFlag)
			By("verifying the pod has the label " + labelName + " with the value " + labelValue)
			output := runKubectl("get", "pod", simplePodName, "-L", labelName, nsFlag)
			if !strings.Contains(output, labelValue) {
				Failf("Failed updating label " + labelName + " to the pod " + simplePodName)
			}

			By("removing the label " + labelName + " of a pod")
			runKubectl("label", "pods", simplePodName, labelName+"-", nsFlag)
			By("verifying the pod doesn't have the label " + labelName)
			output = runKubectl("get", "pod", simplePodName, "-L", labelName, nsFlag)
			if strings.Contains(output, labelValue) {
				Failf("Failed removing label " + labelName + " of the pod " + simplePodName)
			}
		})
	})

	Describe("Kubectl logs", func() {
		It("should find a string in pod logs", func() {
			mkpath := func(file string) string {
				return filepath.Join(testContext.RepoRoot, "examples/guestbook-go", file)
			}
			controllerJson := mkpath("redis-master-controller.json")
			nsFlag := fmt.Sprintf("--namespace=%v", ns)
			By("creating Redis RC")
			runKubectl("create", "-f", controllerJson, nsFlag)
			By("checking logs")
			forEachPod(c, ns, "app", "redis", func(pod api.Pod) {
				_, err := lookForStringInLog(ns, pod.Name, "redis-master", "The server is now ready to accept connections", podStartTimeout)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("Kubectl patch", func() {
		It("should add annotations for pods in rc", func() {
			mkpath := func(file string) string {
				return filepath.Join(testContext.RepoRoot, "examples/guestbook-go", file)
			}
			controllerJson := mkpath("redis-master-controller.json")
			nsFlag := fmt.Sprintf("--namespace=%v", ns)
			By("creating Redis RC")
			runKubectl("create", "-f", controllerJson, nsFlag)
			By("patching all pods")
			forEachPod(c, ns, "app", "redis", func(pod api.Pod) {
				runKubectl("patch", "pod", pod.Name, nsFlag, "-p", "{\"metadata\":{\"annotations\":{\"x\":\"y\"}}}")
			})

			By("checking annotations")
			forEachPod(c, ns, "app", "redis", func(pod api.Pod) {
				found := false
				for key, val := range pod.Annotations {
					if key == "x" && val == "y" {
						found = true
					}
				}
				if !found {
					Failf("Added annation not found")
				}
			})
		})
	})

	Describe("Kubectl version", func() {
		It("should check is all data is printed", func() {
			version := runKubectl("version")
			requiredItems := []string{"Client Version:", "Server Version:", "Major:", "Minor:", "GitCommit:"}
			for _, item := range requiredItems {
				if !strings.Contains(version, item) {
					Failf("Required item %s not found in %s", item, version)
				}
			}
		})
	})

	Describe("Kubectl run", func() {
		var nsFlag string
		var rcName string

		BeforeEach(func() {
			nsFlag = fmt.Sprintf("--namespace=%v", ns)
			rcName = "e2e-test-nginx-rc"
		})

		AfterEach(func() {
			runKubectl("stop", "rc", rcName, nsFlag)
		})

		It("should create an rc from an image", func() {
			image := "nginx"

			By("running the image " + image)
			runKubectl("run", rcName, "--image="+image, nsFlag)
			By("verifying the rc " + rcName + " was created")
			rc, err := c.ReplicationControllers(ns).Get(rcName)
			if err != nil {
				Failf("Failed getting rc %s: %v", rcName, err)
			}
			containers := rc.Spec.Template.Spec.Containers
			if containers == nil || len(containers) != 1 || containers[0].Image != image {
				Failf("Failed creating rc %s for 1 pod with expected image %s", rcName, image)
			}

			By("verifying the pod controlled by rc " + rcName + " was created")
			label := labels.SelectorFromSet(labels.Set(map[string]string{"run": rcName}))
			podlist, err := waitForPodsWithLabel(c, ns, label)
			if err != nil {
				Failf("Failed getting pod controlled by rc %s: %v", rcName, err)
			}
			pods := podlist.Items
			if pods == nil || len(pods) != 1 || len(pods[0].Spec.Containers) != 1 || pods[0].Spec.Containers[0].Image != image {
				runKubectl("get", "pods", "-L", "run", nsFlag)
				Failf("Failed creating 1 pod with expected image %s. Number of pods = %v", image, len(pods))
			}
		})

	})

	Describe("Proxy server", func() {
		// TODO: test proxy options (static, prefix, etc)
		It("should support proxy with --port 0", func() {
			By("starting the proxy server")
			port, cmd, err := startProxyServer()
			if cmd != nil {
				defer tryKill(cmd)
			}
			if err != nil {
				Failf("Failed to start proxy server: %v", err)
			}
			By("curling proxy /api/ output")
			localAddr := fmt.Sprintf("http://localhost:%d/api/", port)
			apiVersions, err := getAPIVersions(localAddr)
			if len(apiVersions.Versions) < 1 {
				Failf("Expected at least one supported apiversion, got %v", apiVersions)
			}
		})
	})

})

// Checks whether the output split by line contains the required elements.
func checkOutput(output string, required [][]string) {
	outputLines := strings.Split(output, "\n")
	currentLine := 0
	for _, requirement := range required {
		for currentLine < len(outputLines) && !strings.Contains(outputLines[currentLine], requirement[0]) {
			currentLine++
		}
		if currentLine == len(outputLines) {
			Failf("Failed to find %s in %s", requirement[0], output)
		}
		for _, item := range requirement[1:] {
			if !strings.Contains(outputLines[currentLine], item) {
				Failf("Failed to find %s in %s", item, outputLines[currentLine])
			}
		}
	}
}

func getAPIVersions(apiEndpoint string) (*api.APIVersions, error) {
	body, err := curl(apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("Failed http.Get of %s: %v", apiEndpoint, err)
	}
	var apiVersions api.APIVersions
	if err := json.Unmarshal([]byte(body), &apiVersions); err != nil {
		return nil, fmt.Errorf("Failed to parse /api output %s: %v", body, err)
	}
	return &apiVersions, nil
}

func startProxyServer() (int, *exec.Cmd, error) {
	// Specifying port 0 indicates we want the os to pick a random port.
	cmd := kubectlCmd("proxy", "-p", "0")
	stdout, stderr, err := startCmdAndStreamOutput(cmd)
	if err != nil {
		return -1, nil, err
	}
	defer stdout.Close()
	defer stderr.Close()
	buf := make([]byte, 128)
	var n int
	if n, err = stdout.Read(buf); err != nil {
		return -1, cmd, fmt.Errorf("Failed to read from kubectl proxy stdout: %v", err)
	}
	output := string(buf[:n])
	match := proxyRegexp.FindStringSubmatch(output)
	if len(match) == 2 {
		if port, err := strconv.Atoi(match[1]); err == nil {
			return port, cmd, nil
		}
	}
	return -1, cmd, fmt.Errorf("Failed to parse port from proxy stdout: %s", output)
}

func curl(addr string) (string, error) {
	resp, err := http.Get(addr)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body[:]), nil
}

func validateGuestbookApp(c *client.Client, ns string) {
	Logf("Waiting for frontend to serve content.")
	if !waitForGuestbookResponse(c, "get", "", `{"data": ""}`, guestbookStartupTimeout, ns) {
		Failf("Frontend service did not start serving content in %v seconds.", guestbookStartupTimeout.Seconds())
	}

	Logf("Trying to add a new entry to the guestbook.")
	if !waitForGuestbookResponse(c, "set", "TestEntry", `{"message": "Updated"}`, guestbookResponseTimeout, ns) {
		Failf("Cannot added new entry in %v seconds.", guestbookResponseTimeout.Seconds())
	}

	Logf("Verifying that added entry can be retrieved.")
	if !waitForGuestbookResponse(c, "get", "", `{"data": "TestEntry"}`, guestbookResponseTimeout, ns) {
		Failf("Entry to guestbook wasn't correctly added in %v seconds.", guestbookResponseTimeout.Seconds())
	}
}

// Returns whether received expected response from guestbook on time.
func waitForGuestbookResponse(c *client.Client, cmd, arg, expectedResponse string, timeout time.Duration, ns string) bool {
	for start := time.Now(); time.Since(start) < timeout; time.Sleep(5 * time.Second) {
		res, err := makeRequestToGuestbook(c, cmd, arg, ns)
		if err == nil && res == expectedResponse {
			return true
		}
	}
	return false
}

func makeRequestToGuestbook(c *client.Client, cmd, value string, ns string) (string, error) {
	result, err := c.Get().
		Prefix("proxy").
		Namespace(ns).
		Resource("services").
		Name("frontend").
		Suffix("/index.php").
		Param("cmd", cmd).
		Param("key", "messages").
		Param("value", value).
		Do().
		Raw()
	return string(result), err
}

type updateDemoData struct {
	Image string
}

// getUDData creates a validator function based on the input string (i.e. kitten.jpg).
// For example, if you send "kitten.jpg", this function veridies that the image jpg = kitten.jpg
// in the container's json field.
func getUDData(jpgExpected string, ns string) func(*client.Client, string) error {

	// getUDData validates data.json in the update-demo (returns nil if data is ok).
	return func(c *client.Client, podID string) error {
		Logf("validating pod %s", podID)
		body, err := c.Get().
			Prefix("proxy").
			Namespace(ns).
			Resource("pods").
			Name(podID).
			Suffix("data.json").
			Do().
			Raw()
		if err != nil {
			return err
		}
		Logf("got data: %s", body)
		var data updateDemoData
		if err := json.Unmarshal(body, &data); err != nil {
			return err
		}
		Logf("Unmarshalled json jpg/img => %s , expecting %s .", data, jpgExpected)
		if strings.Contains(data.Image, jpgExpected) {
			return nil
		} else {
			return errors.New(fmt.Sprintf("data served up in container is innaccurate, %s didn't contain %s", data, jpgExpected))
		}
	}
}
