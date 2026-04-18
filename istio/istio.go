package istio

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"service_mesh/model"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var graphResponse model.GraphResponse

var mu sync.Mutex

func PollingIstio(js nats.JetStreamContext, nc *nats.Conn, jetStreamEnables bool) {
	graphType := os.Getenv("graphType")
	duration := os.Getenv("duration")
	namespaces := os.Getenv("namespaces")
	BASE_URL := os.Getenv("BASE_URL")
	// apiBase := strings.TrimSuffix(BASE_URL, "/namespaces/graph")

	// building kiali url

	u, err := url.Parse(BASE_URL)

	if err != nil {
		log.Fatalf("Failed to parse the URL %v\n", err)
	}

	q := u.Query()
	q.Set("namespaces", namespaces)
	q.Set("duration", duration)
	q.Set("graphType", graphType)

	u.RawQuery = q.Encode()  

	FINAL_KIALI_URL := u.String()

	fmt.Printf("kiali url is %s\n", FINAL_KIALI_URL)

	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	var clientset kubernetes.Interface
	if err != nil {
		log.Printf("Failed to build kubeconfig: %v", err)
	} else {
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

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		// setting request context
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, FINAL_KIALI_URL, nil)

		//setting header as we want response in json format

		req.Header.Set("Accept", "application/json")

		//sending request to kiali using  http client

		resp, err := client.Do(req)

		if err != nil {
			log.Fatalf("Failed to send http request %v\n", err)
		}

		// reading response from kiali
		body, err := ioutil.ReadAll(resp.Body)

		if err != nil {
			log.Fatalf("Error in reading response %v\n", err)

		}

		resp.Body.Close()
		cancel()

		if err != nil {
			log.Fatalf("Error in creting request body with /GET method %v\n", err)
		}

		err = json.Unmarshal(body, &graphResponse)

		if err != nil {
			log.Fatalf("Error in unmarshalling graph response %v", err)
		}

		for i := range graphResponse.Elements.Nodes {
			node := &graphResponse.Elements.Nodes[i]
			if node.Data.App != "" && node.Data.Namespace != "" && clientset != nil {
				fmt.Printf("Pods fetching are started\n")
				pods, err := fetchPodsForApp(clientset, node.Data.Namespace, node.Data.App)
				if err != nil {
					log.Printf("Error fetching pods for app %s in namespace %s: %v\n", node.Data.App, node.Data.Namespace, err)
					continue
				}
				node.Data.Pod = pods
			}
		}

		fmt.Printf("Graphnode are  %v\n\n", graphResponse.Elements.Nodes)

		fmt.Printf("GraphEdges are  %v\n\n", graphResponse.Elements.Edges)

		// fmt.Println("Poda are::\n", graphResponse.Elements.Nodes)
		data, err := json.Marshal(graphResponse)

		if err != nil {
			log.Println("Error in marshalling graphResponse", err)

		}

		// publishing graph data to nats using jetstream Enabled

		if jetStreamEnables == true {
			_, err = js.Publish("graph.snapshot", data)
			log.Println("Send graph snapshot to nats using jetstream enabled")
			print("length of data is:", len(data))

		} else {

			err = nc.Publish("graph.snapshot", data)
			log.Println("Send graph snapshot using default nats")
			print("length of data is:", len(data))

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
		for _, c := range pod.Spec.Containers {
			containers = append(containers, model.Containers{
				ContainerName: c.Name,
			})
		}

		result = append(result, model.Pods{
			Name:      pod.Name,
			Container: containers,
		})
	}

	return result, nil
}
