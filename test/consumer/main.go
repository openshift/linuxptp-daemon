package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	port := flag.Int("port", 9090, "Port for the consumer HTTP server")
	apiURL := flag.String("api-url", "http://localhost:9043", "Cloud-event-proxy API base URL")
	resource := flag.String("resource", "", "O-RAN resource address to subscribe to")
	currentState := flag.Bool("current-state", false, "Query CurrentState and exit")
	flag.Parse()

	if *resource == "" {
		log.Fatal("--resource is required")
	}

	baseURL := strings.TrimRight(*apiURL, "/")

	if *currentState {
		queryCurrentState(baseURL, *resource)
		return
	}

	subscribe(baseURL, *resource, *port)
}

func queryCurrentState(baseURL, resource string) {
	url := fmt.Sprintf("%s/api/ocloudNotifications/v2%s/CurrentState", baseURL, resource)
	resp, err := http.Get(url)
	if err != nil {
		log.Fatalf("CurrentState request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("CurrentState returned %d", resp.StatusCode)
	}

	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
}

func subscribe(baseURL, resource string, port int) {
	http.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprintln(os.Stdout, string(body))
		w.WriteHeader(http.StatusNoContent)
	})

	go func() {
		addr := fmt.Sprintf(":%d", port)
		log.Printf("consumer listening on %s", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatalf("consumer server error: %v", err)
		}
	}()

	endpointURI := fmt.Sprintf("http://localhost:%d/event", port)
	subBody := fmt.Sprintf(`{"EndpointUri":"%s","ResourceAddress":"%s"}`, endpointURI, resource)
	resp, err := http.Post(
		baseURL+"/api/ocloudNotifications/v2/subscriptions",
		"application/json",
		strings.NewReader(subBody),
	)
	if err != nil {
		log.Fatalf("subscription request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		log.Fatalf("subscription returned %d", resp.StatusCode)
	}
	log.Printf("subscribed to %s", resource)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
