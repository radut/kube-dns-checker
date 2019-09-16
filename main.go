package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	queryTime = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_queries_time",
			Help: "Time taken for dns query",
		},
		[]string{"domain"},
	)
	querySuccess = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_queries_success",
			Help: "DNS responded OK(1) or NOT OK/Timeout(0)",
		},
		[]string{"domain"},
	)
)
var domain = "www.google.com";

//func handler(w http.ResponseWriter, r *http.Request) {
//	query := r.URL.Query()
//	name := query.Get("name")
//	if name == "" {
//		name = "Guest"
//	}
//	log.Printf("Received request for %s\n", name)
//	w.Write([]byte(fmt.Sprintf("Hello, %s\n", name)))
//}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func queryDomain() {
	now := time.Now()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		//routine
		//cmd := exec.Command("dig", "@1.2.3.1", "+time=5", "+tries=1", domain)
		cmd := exec.Command("dig", "+time=5", "+tries=1", domain)
		out, err := cmd.CombinedOutput();
		if err != nil {
			fmt.Printf("cmd.Run() failed with %s\n", err)
			querySuccess.With(prometheus.Labels{"domain": domain}).Set(0);
		} else {
			querySuccess.With(prometheus.Labels{"domain": domain}).Set(1);
		}
		elapsed := int(math.Round(float64(time.Since(now) / 1_000_000)));
		fmt.Printf("elapsed %d ms\n", elapsed)
		queryTime.With(prometheus.Labels{"domain": domain}).Set(float64(elapsed));
		fmt.Printf("combined out:\n%s\n", string(out))

		wg.Done() //if we do for,and need to wait for group

	}()

	wg.Wait()

	//go func(i int) {
	//	defer wg.Done()
	//	val := slice[i]
	//	fmt.Printf("i: %v, val: %v\n", i, val)
	//}(i)

	//queryTime.With(prometheus.Labels{"domain":domain}).Set(rand.Float64());
}

func main() {
	go func(){
		queryDomain();
	}()

	// Create Server and Route Handlers
	r := mux.NewRouter()

	//r.HandleFunc("/", handler)
	r.HandleFunc("/live", healthHandler)
	r.HandleFunc("/ready", readinessHandler)
	r.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Handler:      r,
		Addr:         ":8080",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	var interval = getEnvAsInt("INTERVAL", 3000)
	ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
	go func() {
		for {
			select {
			case t := <-ticker.C:
				fmt.Println("Tick at", t)
				queryDomain();
			}
		}
	}()

	// Start Server
	go func() {
		log.Println("Starting Server")
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	// Graceful Shutdown
	waitForShutdown(srv)
}

func waitForShutdown(srv *http.Server) {
	interruptChan := make(chan os.Signal, 1)
	signal.Notify(interruptChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	// Block until we receive our signal.
	<-interruptChan

	// Create a deadline to wait for.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	srv.Shutdown(ctx)

	log.Println("Shutting down")
	os.Exit(0)
}

func getEnv(key string, defaultVal string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}

	return defaultVal
}

// Simple helper function to read an environment variable into integer or return a default value
func getEnvAsInt(name string, defaultVal int) int {
	valueStr := getEnv(name, "")
	if value, err := strconv.Atoi(valueStr); err == nil {
		return value
	}

	return defaultVal
}

// Helper to read an environment variable into a bool or return default value
func getEnvAsBool(name string, defaultVal bool) bool {
	valStr := getEnv(name, "")
	if val, err := strconv.ParseBool(valStr); err == nil {
		return val
	}

	return defaultVal
}
