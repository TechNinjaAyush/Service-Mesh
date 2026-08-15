package istio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"service_mesh/model"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var graphResponse model.GraphResponse

var mu sync.Mutex

func PollingIstio(js nats.JetStreamContext, nc *nats.Conn, jetStreamEnables bool) {
	baseURL := strings.TrimSpace(os.Getenv("BASE_URL"))
	namespaces := strings.TrimSpace(os.Getenv("namespaces"))
	graphType := strings.TrimSpace(os.Getenv("graphType"))
	duration := strings.TrimSpace(os.Getenv("duration"))

	u, err := url.Parse(baseURL)

	if err != nil {
		log.Fatalf("Failed to parse the URL %v\n", err)
	}

	q := u.Query()
	q.Set("namespaces", namespaces)
	q.Set("duration", duration)
	q.Set("graphType", graphType)

	u.RawQuery = q.Encode()

	FINAL_KIALI_URL := u.String()
	println("final url is:\n", FINAL_KIALI_URL)

	// Fallback to in-cluster config if running inside a pod
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Printf("Failed to build kubeconfig: %v", err)
		}
	}

	var clientset kubernetes.Interface
	if config != nil {
		clientset, err = kubernetes.NewForConfig(config)
		if err != nil {
			log.Printf("Failed to create kubernetes client: %v", err)
		}
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	//Building request body

	ticker := time.NewTicker(20 * time.Second)
	prevHashString := ""

	// Map to track alerted pods to prevent duplicate NATS events
	alertedPods := make(map[string]string)

	// Map to track alerted traffic to prevent duplicate NATS events
	alertedTraffic := make(map[string]bool)

	for range ticker.C {
		currentPods := make(map[string]bool)
		currentTrafficAlerts := make(map[string]bool)
		forceSnapshot := false
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		// setting request context
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, FINAL_KIALI_URL, nil)

		//setting header as we want response in json format

		req.Header.Set("Accept", "application/json")

		//sending request to kiali using  http client

		resp, err := client.Do(req)

		if err != nil {
			log.Printf("Failed to send http request %v\n", err)
			cancel()
			continue
		}

		// reading response from kiali
		body, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()

		if err != nil {
			log.Printf("Error in reading response %v\n", err)
			continue
		}

		err = json.Unmarshal(body, &graphResponse)

		if err != nil {
			log.Printf("Error in unmarshalling graph response %v", err)
			continue
		}

		nodeMap := make(map[string]string)
		for i := range graphResponse.Elements.Nodes {
			node := &graphResponse.Elements.Nodes[i]

			if node.Data.App != "" {
				nodeMap[node.Data.ID] = node.Data.App
			}

			if node.Data.App != "" && node.Data.Namespace != "" && clientset != nil {
				pods, err := fetchPodsForApp(clientset, node.Data.Namespace, node.Data.App)
				if err != nil {
					log.Printf("Error fetching pods for app %s in namespace %s: %v\n", node.Data.App, node.Data.Namespace, err)
					continue
				}
				node.Data.Pod = pods
			}
		}

		// Add Edge / Traffic analysis to check for Istio Flags (like UF or UH)
		for i := range graphResponse.Elements.Edges {
			edge := &graphResponse.Elements.Edges[i]

			// Loop through all responses (could be HTTP "503", gRPC "14", etc.)
			for responseCode, responseDetail := range edge.Data.Traffic.Responses {

				// Loop through the flags inside this response
				for flag := range responseDetail.Flags {

					if flag == "UF" || flag == "UH" || flag == "NR" {

						// Extract the host if available
						hostName := ""
						for h := range responseDetail.Hosts {
							hostName = strings.TrimSuffix(h, ".default.svc.cluster.local")

							break // Just grab the first one
						}

						sourceName := nodeMap[edge.Data.Source]
						if sourceName == "" {
							sourceName = edge.Data.Source
						}

						targetName := nodeMap[edge.Data.Target]
						if targetName == "" {
							targetName = edge.Data.Target
						}

						alertKey := fmt.Sprintf("%s-%s-%s-%s", sourceName, targetName, responseCode, flag)
						currentTrafficAlerts[alertKey] = true

						if alertedTraffic[alertKey] {
							continue // already alerted
						}

						alert := model.TrafficAlert{
							IncidentID: uuid.New().String(),

							Source:       sourceName,
							Target:       targetName,
							ResponseCode: responseCode,
							Flag:         flag,
							Host:         hostName,
							Protocol:     edge.Data.Traffic.Protocol,
							ResponseTime: edge.Data.ResponseTime,
							Rates:        edge.Data.Traffic.Rates,
							FlagPercent:  responseDetail.Flags[flag],
							IsMTLS:       edge.Data.IsMTLS,
							Message:      fmt.Sprintf("Traffic failure detected from %s to %s (Host: %s) with protocol %s, flag %s (%s%%), code %s", sourceName, targetName, hostName, edge.Data.Traffic.Protocol, flag, responseDetail.Flags[flag], responseCode),
							Timestamp:    time.Now().Unix(),
						}

						// Publish the alert to NATS
						alertData, err := json.Marshal(alert)
						if err == nil {
							if jetStreamEnables {

								_, err = js.Publish("traffic.alert", alertData)

							} else {
								err = nc.Publish("traffic.alert", alertData)
							}
							if err != nil {
								log.Printf("Error publishing traffic.alert: %v", err)
							} else {
								log.Printf("Published traffic.alert: %s", alert.Message)
								alertedTraffic[alertKey] = true
								forceSnapshot = true
							}
						}
					}
				}
			}
		}

		data, err := json.Marshal(graphResponse)
		if err != nil {
			log.Println("Error in marshalling graphResponse", err)

		}

		// adding hashing feature for infratopology so to reduce redundancy for ai agent

		hash := sha256.Sum256([]byte(data))

		NewHashString := hex.EncodeToString(hash[:])

		// prevHashString = NewHashString

		if err != nil {
			log.Printf("Error in creating hash for service topology %v", err)
			continue
		}

		// Publishing graph data to NATS.

		shouldPublish := (NewHashString != prevHashString) || forceSnapshot

		if shouldPublish && jetStreamEnables {
			_, err = js.Publish("graph.snapshot", data)
			if err != nil {
				log.Printf("ERROR publishing graph.snapshot using JetStream: %v", err)
			} else {
				log.Printf("Published graph.snapshot successfully using JetStream")
				prevHashString = NewHashString
			}

		} else if shouldPublish {
			err = nc.Publish("graph.snapshot", data)
			if err != nil {
				log.Printf("ERROR publishing graph.snapshot using Core NATS: %v", err)
			} else {
				log.Printf("Published graph.snapshot successfully using Core NATS")
				prevHashString = NewHashString
			}

		} else {
			log.Printf("SKIPPING Graph Snapshot...")
		}

		// Cleanup alertedPods for pods that no longer exist
		for podName := range alertedPods {
			if !currentPods[podName] {
				delete(alertedPods, podName)
			}
		}

		// Cleanup alertedTraffic for traffic alerts that are no longer active
		for key := range alertedTraffic {
			if !currentTrafficAlerts[key] {
				delete(alertedTraffic, key)
			}
		}

	}

}

func GetIstioGraph(c *gin.Context) {

	//calling istio graph

	mu.Lock()
	defer mu.Unlock()

	c.JSON(http.StatusOK, graphResponse)

}
func fetchPodsForApp(clientset kubernetes.Interface, namespace, app string) ([]model.Pods, error) {

	var result []model.Pods

	podlist, err := clientset.CoreV1().
		Pods(namespace).
		List(context.TODO(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", app), // ✅ filter here
		})

	if err != nil {
		return nil, err
	}

	for _, pod := range podlist.Items {
		var containers []model.Containers

		// Map container statuses by name for easy lookup
		statusMap := make(map[string]bool)

		for _, cs := range pod.Status.ContainerStatuses {
			mContainer := model.Containers{
				ContainerName: cs.Name,
			}

			if cs.State.Running != nil {
				mContainer.Status = "Running"
			} else if cs.State.Waiting != nil {
				mContainer.Status = "Waiting"
				mContainer.Reason = cs.State.Waiting.Reason
			} else if cs.State.Terminated != nil {
				mContainer.Status = "Terminated"
				mContainer.Reason = cs.State.Terminated.Reason
				mContainer.ExitCode = int32(cs.State.Terminated.ExitCode)
			}

			containers = append(containers, mContainer)
			statusMap[cs.Name] = true
		}

		for _, c := range pod.Spec.Containers {
			if !statusMap[c.Name] {
				containers = append(containers, model.Containers{
					ContainerName: c.Name,
					Status:        "Unknown",
				})
			}
		}

		result = append(result, model.Pods{
			Name:          pod.Name,
			Status:        string(pod.Status.Phase),
			StatusMessage: pod.Status.Message,
			Container:     containers,
		})
	}

	return result, nil
}

func StartPodStatusObserver(nc *nats.Conn) {
	// Fallback to in-cluster config if running inside a pod
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Printf("Failed to build kubeconfig for observer: %v", err)
			return
		}
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("Failed to create kubernetes client for observer: %v", err)
		return
	}

	_, err = nc.Subscribe("observer.pod.status", func(msg *nats.Msg) {
		var req struct {
			Action      string `json:"action"`
			PodName     string `json:"pod_name"`
			ServiceName string `json:"service_name"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			log.Printf("Error unmarshaling pod status request: %v", err)
			return
		}

		if req.Action == "check_pod" && req.PodName != "" {
			pod, err := fetchPodStatusDetails(clientset, req.PodName, req.ServiceName)
			var resp []byte
			if err != nil {
				resp, _ = json.Marshal(map[string]string{"error": err.Error()})
			} else {
				resp, _ = json.Marshal(pod)
			}
			msg.Respond(resp)
		}
	})
	if err != nil {
		log.Printf("Failed to subscribe to observer.pod.status: %v", err)
	}

	_, err = nc.Subscribe("observer.pod.logs", func(msg *nats.Msg) {
		var req struct {
			Action  string `json:"action"`
			PodName string `json:"pod_name"`
		}
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			log.Printf("Error unmarshaling pod logs request: %v", err)
			return
		}

		if req.Action == "fetch_logs" && req.PodName != "" {
			logs, err := fetchPodLogsDetails(clientset, req.PodName)
			var resp []byte
			if err != nil {
				resp, _ = json.Marshal(map[string]string{"error": err.Error()})
			} else {
				resp, _ = json.Marshal(logs)
			}
			msg.Respond(resp)
		}
	})
	if err != nil {
		log.Printf("Failed to subscribe to observer.pod.logs: %v", err)
	}
}

type PodStatusResponse struct {
	PodName           string               `json:"pod_name"`
	Phase             string               `json:"phase"`
	ContainerStatuses []ContainerDetails   `json:"container_statuses"`
	RecentEvents      []EventDetails       `json:"recent_events"`
	ServiceMatch      *ServiceMatchDetails `json:"service_match,omitempty"`
}

type ServiceMatchDetails struct {
	ServiceName     string            `json:"service_name"`
	ServiceSelector map[string]string `json:"service_selector"`
	PodLabels       map[string]string `json:"pod_labels"`
	SelectorMatch   bool              `json:"selector_match"`
	ServicePorts    []ServicePortInfo `json:"service_ports"`
	PodPorts        []int32           `json:"pod_ports"`
}

type ServicePortInfo struct {
	Port       int32  `json:"port"`
	TargetPort string `json:"target_port"`
}

type ContainerDetails struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	RestartCount int32  `json:"restart_count"`
	ExitCode     int32  `json:"exit_code,omitempty"`
}

type EventDetails struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func fetchPodStatusDetails(clientset kubernetes.Interface, podName string, serviceName string) (*PodStatusResponse, error) {
	podlist, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", podName),
	})
	if err != nil {
		return nil, err
	}

	for _, pod := range podlist.Items {
		if pod.Name == podName {
			var containers []ContainerDetails

			for _, cs := range pod.Status.ContainerStatuses {
				cDetails := ContainerDetails{
					Name:         cs.Name,
					RestartCount: cs.RestartCount,
				}
				if cs.State.Running != nil {
					cDetails.State = "running"
				} else if cs.State.Waiting != nil {
					cDetails.State = "waiting"
					cDetails.Reason = cs.State.Waiting.Reason
				} else if cs.State.Terminated != nil {
					cDetails.State = "terminated"
					cDetails.Reason = cs.State.Terminated.Reason
					cDetails.ExitCode = cs.State.Terminated.ExitCode
				}
				containers = append(containers, cDetails)
			}

			for _, c := range pod.Spec.Containers {
				found := false
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.Name == c.Name {
						found = true
						break
					}
				}
				if !found {
					containers = append(containers, ContainerDetails{
						Name:  c.Name,
						State: "unknown",
					})
				}
			}

			var recentEvents []EventDetails
			eventsList, err := clientset.CoreV1().Events(pod.Namespace).List(context.TODO(), metav1.ListOptions{
				FieldSelector: fmt.Sprintf("involvedObject.name=%s", podName),
			})
			if err == nil {
				for _, event := range eventsList.Items {
					recentEvents = append(recentEvents, EventDetails{
						Reason:  event.Reason,
						Message: event.Message,
					})
				}
			}

			var serviceMatch *ServiceMatchDetails
			if serviceName != "" {
				svc, err := clientset.CoreV1().Services(pod.Namespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
				if err == nil {
					match := true
					for k, v := range svc.Spec.Selector {
						if pod.Labels[k] != v {
							match = false
							break
						}
					}
					var ports []ServicePortInfo
					for _, p := range svc.Spec.Ports {
						ports = append(ports, ServicePortInfo{
							Port:       p.Port,
							TargetPort: p.TargetPort.String(),
						})
					}
					var podPorts []int32
					for _, c := range pod.Spec.Containers {
						for _, p := range c.Ports {
							podPorts = append(podPorts, p.ContainerPort)
						}
					}
					serviceMatch = &ServiceMatchDetails{
						ServiceName:     svc.Name,
						ServiceSelector: svc.Spec.Selector,
						PodLabels:       pod.Labels,
						SelectorMatch:   match,
						ServicePorts:    ports,
						PodPorts:        podPorts,
					}
				}
			}

			return &PodStatusResponse{
				PodName:           pod.Name,
				Phase:             string(pod.Status.Phase),
				ContainerStatuses: containers,
				RecentEvents:      recentEvents,
				ServiceMatch:      serviceMatch,
			}, nil
		}
	}
	return &PodStatusResponse{
		PodName:           podName,
		Phase:             "ScaledToZero/NotFound",
		ContainerStatuses: []ContainerDetails{},
		RecentEvents: []EventDetails{
			{
				Reason:  "PodNotFound",
				Message: "The requested pod was not found in the cluster. The deployment or replica set may be scaled to zero.",
			},
		},
	}, nil
}

type PodLogsResponse struct {
	PodName string            `json:"pod_name"`
	Logs    map[string]string `json:"logs"`
}

func fetchPodLogsDetails(clientset kubernetes.Interface, podName string) (*PodLogsResponse, error) {
	podlist, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", podName),
	})
	if err != nil {
		return nil, err
	}

	var targetPod *corev1.Pod
	for _, p := range podlist.Items {
		if p.Name == podName {
			podCopy := p
			targetPod = &podCopy
			break
		}
	}

	if targetPod == nil {
		return &PodLogsResponse{
			PodName: podName,
			Logs: map[string]string{
				"error": "The requested pod was not found in the cluster. It may have been deleted or scaled to zero.",
			},
		}, nil
	}

	logsMap := make(map[string]string)
	tailLines := int64(50)

	for _, container := range targetPod.Spec.Containers {
		req := clientset.CoreV1().Pods(targetPod.Namespace).GetLogs(podName, &corev1.PodLogOptions{
			Container: container.Name,
			TailLines: &tailLines,
		})
		podLogs, err := req.Stream(context.TODO())
		if err != nil {
			logsMap[container.Name] = fmt.Sprintf("Error fetching logs: %v", err)
			continue
		}

		buf := new(bytes.Buffer)
		_, err = io.Copy(buf, podLogs)
		podLogs.Close()

		if err != nil {
			logsMap[container.Name] = fmt.Sprintf("Error reading logs: %v", err)
		} else {
			logsMap[container.Name] = buf.String()
		}
	}

	return &PodLogsResponse{
		PodName: podName,
		Logs:    logsMap,
	}, nil
}
