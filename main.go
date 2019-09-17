package main

import (
	"context"
	"fmt"
	"log"
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

var mutex = &sync.Mutex{}
//var domains = []string{"www.google.com", "www.cloudflare.com"}
var domains = []string{"www.google.com"}

var interval = 3   //seconds
var period = 15    //seconds
var digTimeout = 5 //seconds
var digRetries = 1

const MAX = 3


var (
	queryTime = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_time_ms",
			Help: "Time taken for dns query in milliseconds",
		},
		[]string{"domain"},
	)
	querySuccess = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_success",
			Help: "DNS responded OK(1) or NOT OK/Timeout(0)",
		},
		[]string{"domain"},
	)
	queryTotalCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_total_count",
			Help: "DNS queries total count",
		},
		[]string{"domain"},
	)
	querySuccessCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_success_count",
			Help: "DNS queries success count",
		},
		[]string{"domain"},
	)
	queryFailCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_fail_count",
			Help: "DNS queries fail count",
		},
		[]string{"domain"},
	)
)

//func handler(w http.ResponseWriter, r *http.Request) {
//	query := r.URL.Query()
//	name := query.Get("name")
//	if name == "" {
//		name = "Guest"
//	}
//	log.Printf("Received request for %s\n", name)
//	w.Write([]byte(fmt.Sprintf("Hello, %s\n", name)))
//}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	mutex.Lock()
	h := promhttp.Handler()
	h.ServeHTTP(w, r)
	mutex.Unlock()
}
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func queryDomains() {
	now := time.Now()
	var wg sync.WaitGroup
	sem := make(chan int, MAX)
	sem <- 1 // will block if there is MAX ints in sem
	for _, d := range domains {
		wg.Add(1)
		go func(domain string) {
			//routine
			fmt.Printf("executing dig for domain :  %s\n", domain)
			//cmd := exec.Command("dig", "@1.2.3.1", "+time=5", "+tries=1", domain)
			cmd := exec.Command("dig", "+time=5", "+tries=1", domain)
			out, err := cmd.CombinedOutput()
			mutex.Lock()
			queryTotalCount.With(prometheus.Labels{"domain": domain}).Inc()
			if err != nil {
				fmt.Printf("cmd.Run() failed with %s\n", err)
				querySuccess.With(prometheus.Labels{"domain": domain}).Set(0)
				queryFailCount.With(prometheus.Labels{"domain": domain}).Inc()
			} else {
				querySuccess.With(prometheus.Labels{"domain": domain}).Set(1)
				querySuccessCount.With(prometheus.Labels{"domain": domain}).Inc()
			}
			elapsed := time.Since(now).Milliseconds()
			fmt.Printf("elapsed %d ms\n", elapsed)
			queryTime.With(prometheus.Labels{"domain": domain}).Set(float64(elapsed))
			mutex.Unlock()
			fmt.Printf("combined out:\n%s\n", string(out))

			<-sem // removes an int from sem, allowing another to proceed
			wg.Done() //if we do for,and need to wait for group
		}(d)
	}

	wg.Wait()

	//go func(i int) {
	//	defer wg.Done()
	//	val := slice[i]
	//	fmt.Printf("i: %v, val: %v\n", i, val)
	//}(i)

	//queryTime.With(prometheus.Labels{"domain":domain}).Set(rand.Float64());
}

func main() {
	resetCounters()
	//getEnvAsInt("RETRIES",)
	fmt.Printf("Using Config:\n")
	fmt.Printf("\tdomains: %v\n", domains)
	fmt.Printf("\tinterval: %d seconds\n", interval)

	go func() {
		defer starTimer();
		queryDomains();
	}()

	// Create Server and Route Handlers
	r := mux.NewRouter()

	//r.HandleFunc("/", handler)
	r.HandleFunc("/live", healthHandler)
	r.HandleFunc("/ready", readinessHandler)
	r.HandleFunc("/metrics", metricsHandler)

	srv := &http.Server{
		Handler:      r,
		Addr:         ":8080",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}



	// Start Server
	go func() {
		log.Println("Starting Server")
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	//var period = getEnvAsInt("PERIOD", period) * 1000;
	//resetTicker := time.NewTicker(time.Duration(period) * time.Millisecond);
	//go func() {
	//	for {
	//		select {
	//		case t := <-resetTicker.C:
	//			fmt.Println("Reset Tick at", t.Format(time.RFC3339))
	//			resetCounters();
	//		}
	//	}
	//}()

	// Graceful Shutdown
	waitForShutdown(srv)
}

func starTimer() {
	var interval = getEnvAsInt("INTERVAL", interval) * 1000
	ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
	go func() {
		for {
			select {
			case t := <-ticker.C:
				fmt.Println("Tick at", t.Format(time.RFC3339))
				queryDomains()
			}
		}
	}()
}

func resetCounters() {
	mutex.Lock()

	for _, domain := range domains {
		queryTotalCount.With(prometheus.Labels{"domain": domain}).Set(0)
		querySuccessCount.With(prometheus.Labels{"domain": domain}).Set(0)
		queryFailCount.With(prometheus.Labels{"domain": domain}).Set(0)
	}

	mutex.Unlock()
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
