package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"

	//"net"
	"net/http"
	"os"
	//"os/exec"
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

var default_interval = "3s"
var default_timeout = "5s"

const MAX_CONCURRENT = 5

var (
	queryTime = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_time_ms",
			Help: "Time taken for dns query in milliseconds",
		},
		[]string{"dns_server", "domain"},
	)
	querySuccess = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_success",
			Help: "DNS responded OK(1) or NOT OK/Timeout(0)",
		},
		[]string{"dns_server", "domain"},
	)
	queryTotalCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_total_count",
			Help: "DNS queries total count",
		},
		[]string{"dns_server", "domain"},
	)
	querySuccessCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_success_count",
			Help: "DNS queries success count",
		},
		[]string{"dns_server", "domain"},
	)
	queryFailCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_fail_count",
			Help: "DNS queries fail count",
		},
		[]string{"dns_server", "domain"},
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

func queryDomains(domains []string, dnsServers []string, timeout Duration) {

	var wg sync.WaitGroup
	sem := make(chan int, MAX_CONCURRENT)
	sem <- 1 // will block if there is MAX_CONCURRENT ints in sem
	for _, d := range domains {
		for _, n := range dnsServers {
			wg.Add(1)
			go func(domain string, nameserver string) {
				now := time.Now()
				var resolver *net.Resolver
				if nameserver == "DEFAULT" {
					resolver = net.DefaultResolver
				} else {
					var ip = net.ParseIP(nameserver);
					if (ip == nil) {
						log.Printf("skipping invalid nameserver: `%s`\n", nameserver)
						wg.Done();
						return;
					}
					resolver = &net.Resolver{
						PreferGo: true,
						Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
							d := net.Dialer{}
							return d.DialContext(ctx, "udp", net.JoinHostPort(ip.String(), "53"))
						},
					}
				}
				fmt.Printf("Lookup Start 'dnsServer=%v domain=%v'\n", nameserver, domain);
				ctx, _ := context.WithTimeout(context.Background(), timeout);
				ips, err := resolver.LookupIPAddr(ctx, domain)
				mutex.Lock()
				fmt.Printf("Lookup Done 'dnsServer=%v domain=%v'\n -> %v / err=%v", nameserver, domain, ips, err);
				//
				cmd := exec.Command("dig", digArgs...);
				out, err := cmd.CombinedOutput()

				queryTotalCount.With(prometheus.Labels{"domain": domain}).Inc()
				fmt.Printf("combined out:\n%s\n", string(out))
				if err != nil {
					fmt.Printf("'dig %v' failed with %s\n", digArgs, err);
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
			}(d, n)
		}
	}

	wg.Wait()

}

func main() {
	var timeoutStr = getEnv("TIMEOUT", default_timeout);
	var intervalStr = getEnv("INTERVAL", default_interval)
	//
	var timeout, timeoutErr = time.ParseDuration(timeoutStr);
	if (timeoutErr != nil) {
		log.Printf("Invalid TIMEOUT duration : `%s`", timeoutStr)
		log.Fatal(timeoutErr);
	}
	var interval, intervalErr = time.ParseDuration(intervalStr);
	if (intervalErr != nil) {
		log.Printf("Invalid INTERVAL duration : `%s`", intervalStr)
		log.Fatal(intervalErr);
	}
	var domainsStr = getEnv("DOMAINS", default_domains)
	var dnsServersStr = getEnv("DNS_SERVERS", "DEFAULT");
	var domains = strings.Split(strings.ReplaceAll(domainsStr, " ", ""), ",");
	var dnsServers = strings.Split(strings.ReplaceAll(dnsServersStr, " ", ""), ",");
	//
	fmt.Printf("Using Config :\n")
	fmt.Printf("\tDNS Servers : %v\n", dnsServers)
	fmt.Printf("\tDomains     : %v\n", domains)
	fmt.Printf("\tTimeout     : %v \n", timeout)
	fmt.Printf("\tInterval    : %v \n", interval)
	fmt.Printf("\n\n")

	resetCounters(domains, dnsServers);
	//
	time.Sleep(2 * time.Second);
	//
	go func() {
		defer starTimer(interval, domains, dnsServers, timeout);
		queryDomains(domains, dnsServers, timeout);
	}()

	// Create Server and Route Handlers
	httpRouter := mux.NewRouter()

	//httpRouter.HandleFunc("/", handler)
	httpRouter.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
			<head><title>Kube DNS Checker</title></head>
			<body>
			<h1>Kube DNS Checker</h1>
			<p><a href="/metrics">Metrics</a></p>
			</body>
			</html>`))
	})
	httpRouter.HandleFunc("/live", healthHandler)
	httpRouter.HandleFunc("/ready", readinessHandler)
	httpRouter.HandleFunc("/metrics", metricsHandler)

	srv := &http.Server{
		Handler:      httpRouter,
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

	// Graceful Shutdown
	waitForShutdown(srv)
}

func starTimer(interval time.Duration, domains []string, dnsServers []string, timeout time.Duration) {

	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			//case t := <-ticker.C:
			//fmt.Println("Tick at", t.Format(time.RFC3339))
			case <-ticker.C:
				queryDomains(domains, dnsServers, timeout);
			}
		}
	}()
}

func resetCounters(domains []string, dnsServers []string) {
	mutex.Lock()

	for _, domain := range domains {
		for _, nameserver := range domains {
			queryTotalCount.With(prometheus.Labels{"dns_server": nameserver, "domain": domain}).Set(0)
			querySuccessCount.With(prometheus.Labels{"dns_server": nameserver, "domain": domain}).Set(0)
			queryFailCount.With(prometheus.Labels{"dns_server": nameserver, "domain": domain}).Set(0)
		}
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
