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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var mutex = &sync.Mutex{}

//var default_domains = "www.google.com,www.cloudflare.com";
var default_domains = "www.google.com";

var default_interval = 3   //seconds
var default_period = 15    //seconds
var default_digTimeout = 5 //seconds
var default_digRetries = 1

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

func queryDomains(domains []string, useDnsServer bool, dnsServer string) {

	var digTimeout = getEnvAsInt("TIMEOUT", default_digTimeout);
	var digRetries = getEnvAsInt("TRIES", default_digRetries);
	now := time.Now()
	var wg sync.WaitGroup
	sem := make(chan int, MAX)
	sem <- 1 // will block if there is MAX ints in sem
	for _, d := range domains {
		wg.Add(1)
		go func(domain string) {
			//routine
			//cmd := exec.Command("dig", "@1.2.3.1", "+time=5", "+tries=1", domain)
			var digArgs = []string{};
			if (useDnsServer) {
				digArgs = append(digArgs, "@"+dnsServer);
			}
			digArgs = append(digArgs, "+noall");
			digArgs = append(digArgs, "+answer");
			digArgs = append(digArgs, "+stats");
			digArgs = append(digArgs, "+time="+strconv.Itoa(digTimeout));
			digArgs = append(digArgs, "+tries="+strconv.Itoa(digRetries));
			digArgs = append(digArgs, domain);
			fmt.Printf("executing 'dig %v'\n", digArgs);
			//
			cmd := exec.Command("dig", digArgs...);
			out, err := cmd.CombinedOutput()
			mutex.Lock()
			queryTotalCount.With(prometheus.Labels{"domain": domain}).Inc()
			fmt.Printf("combined out:\n%s\n", string(out))
			if err != nil {
				fmt.Printf("'dig %v' failed with %s\n", digArgs,err);
				querySuccess.With(prometheus.Labels{"domain": domain}).Set(0)
				queryFailCount.With(prometheus.Labels{"domain": domain}).Inc()
			} else {
				querySuccess.With(prometheus.Labels{"domain": domain}).Set(1)
				querySuccessCount.With(prometheus.Labels{"domain": domain}).Inc()
			}
			elapsed := time.Since(now).Milliseconds()
			//fmt.Printf("elapsed %d ms\n", elapsed)
			queryTime.With(prometheus.Labels{"domain": domain}).Set(float64(elapsed))
			mutex.Unlock()

			<-sem     // removes an int from sem, allowing another to proceed
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
	var digTimeout = getEnvAsInt("TIMEOUT", default_digTimeout);
	var digRetries = getEnvAsInt("TRIES", default_digRetries);
	var interval = getEnvAsInt("INTERVAL", default_interval)
	var domainsStr = getEnv("DOMAINS", default_domains)
	var domains = strings.Split(strings.ReplaceAll(domainsStr, " ", ""), ",");
	var dnsServer, useDnsServer = os.LookupEnv("DNS_SERVER");
	resetCounters(domains);
	//
	fmt.Printf("Using Config :\n")
	if (useDnsServer) {
		fmt.Printf("\tDNS Server : %v\n", dnsServer)
	} else {
		fmt.Printf("\tDNS Server : default\n");
	}
	fmt.Printf("\tDomains    : %v\n", domains)
	fmt.Printf("\tDig Timeout: %v seconds\n", digTimeout)
	fmt.Printf("\tDig Retries: %v seconds\n", digRetries)
	fmt.Printf("\tInterval   : %d seconds\n", interval)
	fmt.Printf("\n\n")
	//
	time.Sleep(2 * time.Second);
	//
	go func() {
		defer starTimer(interval, domains, useDnsServer, dnsServer);
		queryDomains(domains, useDnsServer, dnsServer);
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

func starTimer(interval int, domains []string, useDnsServer bool, dnsServer string) {
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	go func() {
		for {
			select {
			//case t := <-ticker.C:
			//fmt.Println("Tick at", t.Format(time.RFC3339))
			case <-ticker.C:
				queryDomains(domains, useDnsServer, dnsServer);
			}
		}
	}()
}

func resetCounters(domains []string) {
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
