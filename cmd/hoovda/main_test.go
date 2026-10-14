package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPBudgetCoversValidLongPollAndFinish(t *testing.T) {
	for _, actionBudget := range []time.Duration{15 * time.Second, time.Minute} {
		api := controlHTTPServer(context.Background(), http.NotFoundHandler(), actionBudget)
		// Event polling accepts 60 seconds. Finish may consume two 30-second
		// synthesis/recording stages and two action-sized export/refresh stages.
		for _, operationBudget := range []time.Duration{time.Minute, 60*time.Second + 2*actionBudget} {
			if api.WriteTimeout <= operationBudget {
				t.Errorf("HTTP deadline %s truncates valid operation budget %s", api.WriteTimeout, operationBudget)
			}
		}
	}
}

func TestRuntimeCancellationReachesActiveHTTPRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, canceled := make(chan struct{}), make(chan struct{})
	api := controlHTTPServer(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}), 15*time.Second)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	go api.Serve(listener)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("HTTP request never reached handler")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("runtime shutdown did not cancel active request")
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("canceled HTTP request did not complete")
	}
}
