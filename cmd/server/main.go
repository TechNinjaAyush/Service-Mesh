package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"service_mesh/istio"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/nats-io/nats.go"
)

func CheckHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}
func main() {

	r := gin.Default()

	// loading env file
	err := godotenv.Load()
	if err != nil {
		log.Fatalf("failed to load .env file: %v", err)

	}

	fmt.Print("Connecting to nats server...")
	nc, err := nats.Connect(nats.DefaultURL, nats.MaxReconnects(-1), nats.ReconnectWait(2*time.Second))

	if err != nil {
		log.Println("Error in connecting nats", err)
	}

	// creating a jetstream context

	js, err := nc.JetStream()

	if err != nil {
		log.Println("Error in creating jetstream context:", err)
	}

	// this function will run in background so we can periodically pool the lates graph

	go istio.PollingIstio(js, nc)

	r.GET("/health", CheckHealth)

	r.GET("/kiali", istio.GetIstioGraph)

	fmt.Println("Running server on port 8080")
	// running server on default port 8080

	r.Run()
}
