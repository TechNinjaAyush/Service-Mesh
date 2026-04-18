package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCheckHealth(t *testing.T) {
	// Setup gin in test mode
	gin.SetMode(gin.TestMode)
	router := gin.Default()
	router.GET("/health", CheckHealth)

	// Create a response recorder
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health", nil)

	// Perform the request
	router.ServeHTTP(w, req)

	// Assert response code
	if w.Code != http.StatusOK {
		t.Errorf("Expected status OK; got %v", w.Code)
	}

	// Assert response body
	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response["status"] != "ok" {
		t.Errorf("Expected status 'ok'; got %v", response["status"])
	}
}
