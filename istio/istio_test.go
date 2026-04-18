package istio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"service_mesh/model"
	"testing"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetIstioGraph(t *testing.T) {
	// Setup gin in test mode
	gin.SetMode(gin.TestMode)
	router := gin.Default()
	router.GET("/v1/graph", GetIstioGraph)

	// Set mocked data in the global variable
	mu.Lock()
	graphResponse = model.GraphResponse{
		GraphType: "versionedApp",
		Elements: model.Elements{
			Nodes: []model.Node{
				{Data: model.NodeData{ID: "node1", App: "app1"}},
			},
		},
	}
	mu.Unlock()

	// Create a response recorder
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/graph", nil)

	// Perform the request
	router.ServeHTTP(w, req)

	// Assertions
	if w.Code != http.StatusOK {
		t.Errorf("Expected status OK; got %v", w.Code)
	}

	var response model.GraphResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response.GraphType != "versionedApp" {
		t.Errorf("Expected graphType 'versionedApp'; got %v", response.GraphType)
	}
}

func TestFetchPodsForApp(t *testing.T) {
	// Create a fake clientset
	clientset := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-ns",
			Labels:    map[string]string{"app": "test-app"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "test-container"},
			},
		},
	})

	pods, err := fetchPodsForApp(clientset, "test-ns", "test-app")
	if err != nil {
		t.Fatalf("fetchPodsForApp failed: %v", err)
	}

	if len(pods) != 1 {
		t.Errorf("Expected 1 pod; got %v", len(pods))
	}

	if pods[0].Name != "test-pod" {
		t.Errorf("Expected pod name 'test-pod'; got %v", pods[0].Name)
	}

	if len(pods[0].Container) != 1 || pods[0].Container[0].ContainerName != "test-container" {
		t.Errorf("Expected 1 container named 'test-container'; got %v", pods[0].Container)
	}
}

func TestFetchPodsForApp_NoMatch(t *testing.T) {
	// Create a fake clientset with no pods matching the label
	clientset := fake.NewSimpleClientset()

	pods, err := fetchPodsForApp(clientset, "test-ns", "test-app")
	if err != nil {
		t.Fatalf("fetchPodsForApp failed: %v", err)
	}

	if len(pods) != 0 {
		t.Errorf("Expected 0 pods; got %v", len(pods))
	}
}
