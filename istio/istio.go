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
	"service_mesh/model"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
)

var graphResponse model.GraphResponse

var mu sync.Mutex

func PollingIstio(js nats.JetStreamContext, nc *nats.Conn, jetStreamEnables bool) {
	graphType := os.Getenv("graphType")
	duration := os.Getenv("duration")
	namespaces := os.Getenv("namespaces")
	BASE_URL := os.Getenv("BASE_URL")

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

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	fmt.Printf("kiali url is %s\n", FINAL_KIALI_URL)

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

		//sending response to user
		fmt.Printf("Graphresponse is %v", graphResponse)

		// for _, Edge := range graphResponse.Elements.Edges {
		// 	fmt.Printf("Source :%s\n", Edge.Data.Source)
		// 	fmt.Printf("Destination :%s\n", Edge.Data.Target)

		// }

		// for _, Node := range graphResponse.Elements.Nodes {
		// 	fmt.Printf("node is %s\n", Node.Data.ID)
		// 	fmt.Printf("node namespace is %s\n", Node.Data.Namespace)
		// }

		// converting graph_response into bytes format

		fmt.Println("GraphResponse is:\n", graphResponse)
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

	var FinalGraphResponse model.GraphResponse

	mu.Lock()
	defer mu.Unlock()

	FinalGraphResponse = graphResponse

	c.JSON(http.StatusAccepted, gin.H{"message": FinalGraphResponse})

}
