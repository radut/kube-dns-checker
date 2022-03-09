package main

import (
	"context"
	"fmt"
	"log"
	"net"
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
var default_domains = "www.google.com"

var default_interval = "5s"
var default_timeout = "3s"

const MAX_CONCURRENT = 2

var (
	queryTime = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_time_ms",
			Help: "Time taken for dns query in milliseconds",
		},
		[]string{"nameserver", "domain"},
	)
	querySuccess = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_success",
			Help: "DNS responded OK(1) or NOT OK/Timeout(0)",
		},
		[]string{"nameserver", "domain"},
	)
	queryTotalCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_total_count",
			Help: "DNS queries total count",
		},
		[]string{"nameserver", "domain"},
	)
	querySuccessCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_success_count",
			Help: "DNS queries success count",
		},
		[]string{"nameserver", "domain"},
	)
	queryFailCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_query_fail_count",
			Help: "DNS queries fail count",
		},
		[]string{"nameserver", "domain"},
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

func queryDomainsWithInternalGOResolver(debug bool, domains []string, dnsServers []string, timeout time.Duration) {

	var wg sync.WaitGroup
	sem := make(chan int, MAX_CONCURRENT)
	for _, d := range domains {
		for _, n := range dnsServers {
			wg.Add(1)
			sem <- 1 // will block if there is MAX_CONCURRENT ints in sem
			go func(domain string, nameserver string) {
				var resolver *net.Resolver
				if nameserver == "DEFAULT" {
					//var buf, _ = ioutil.ReadFile("/etc/resolv.conf")
					resolver = &net.Resolver{
						PreferGo: true,
						Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
							d := net.Dialer{}
							log.Printf("Default NS picked address: %v", address)
							return d.DialContext(ctx, "udp", address)
						},
					}
				} else {
					var ip = net.ParseIP(nameserver)
					if ip == nil {
						log.Printf("skipping invalid nameserver: `%s`\n", nameserver)
						<-sem     // removes an int from sem, allowing another to proceed
						wg.Done() //if we do for,and need to wait for group
						return
					}
					resolver = &net.Resolver{
						PreferGo: true,
						Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
							d := net.Dialer{}
							return d.DialContext(ctx, "udp", net.JoinHostPort(ip.String(), "53"))
						},
					}
				}
				now := time.Now()
				log.Printf("Lookup Start 'dnsServer=%v domain=%v' with internal GO Resolver\n", nameserver, domain)
				ctx, _ := context.WithTimeout(context.Background(), timeout)
				ips, err := resolver.LookupIPAddr(ctx, domain)
				elapsed := time.Since(now)
				if err == nil {
					log.Printf("Lookup OK    'dnsServer=%v domain=%v' : took %v -> response=%v\n", nameserver, domain, elapsed, ips)
				} else {
					log.Printf("Lookup ERROR 'dnsServer=%v domain=%v' : took %v -> error=%v\n", nameserver, domain, elapsed, err)
				}
				mutex.Lock()

				queryTotalCount.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Inc()
				if err != nil {
					querySuccess.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Set(0)
					queryFailCount.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Inc()
				} else {
					querySuccess.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Set(1)
					querySuccessCount.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Inc()
				}

				queryTime.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Set(float64(elapsed.Milliseconds()))
				mutex.Unlock()

				<-sem     // removes an int from sem, allowing another to proceed
				wg.Done() //if we do for,and need to wait for group
			}(d, n)
		}
	}

	wg.Wait()
	fmt.Printf("\n")
}

func executeDig(debug bool, digArgs []string, now time.Time, timeout time.Duration) ([]byte, error, time.Duration, string, string) {
	cmd := exec.Command("dig", digArgs...)
	fmt.Printf("%s\n", cmd)
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(now)
	var outStrings = strings.Split(string(out), "\n")
	var queryTimeStrLine = ""
	var response = ""
	for _, line := range outStrings {
		if debug {
			fmt.Printf("%s\n", line)
		}
		if strings.Index(line, "IN") > -1 {
			if strings.Index(line, "A") > -1 || strings.Index(line, "CNAME") > -1 {
				if len(response) > 3 {
					response = response + " --> " + line
				} else {
					response = line
				}
			}
		}
		if strings.Index(line, ";; Query time: ") > -1 {
			var queryTimeStrTemp = strings.Split(queryTimeStrLine, ";; Query time: ")
			if len(queryTimeStrTemp) == 2 {
				var queryTimeStr = queryTimeStrTemp[1]
				queryTimeStr = strings.ReplaceAll(queryTimeStr, " msec", "ms")
				queryTimeStr = strings.ReplaceAll(queryTimeStr, " sec", "s")
				var durationQueryTime, timeoutErr = time.ParseDuration(queryTimeStr)
				if timeoutErr != nil {
					fmt.Printf("Cannot parse Query time: `%s`", queryTimeStr)
				} else {
					elapsed = durationQueryTime
				}
			}
		} else if strings.Index(line, "connection timed out") > -1 {
			elapsed = timeout
		}
	}
	return out, err, elapsed, queryTimeStrLine, response
}

func listContains(lst []string, lookup string) bool {
	for _, val := range lst {
		if val == lookup {
			return true
		}
	}
	return false
}
func queryDomainsWithDIG(debug bool, domains []string, dig_args []string, dnsServers []string, timeout time.Duration) {

	var wg sync.WaitGroup
	sem := make(chan int, MAX_CONCURRENT)
	for _, d := range domains {
		for _, n := range dnsServers {
			wg.Add(1)
			sem <- 1 // will block if there is MAX_CONCURRENT ints in sem
			go func(domain string, nameserver string) {

				//routine
				//cmd := exec.Command("dig", "@1.2.3.1", "+time=5", "+tries=1", domain)
				var digArgs = []string{}
				if nameserver != "DEFAULT" {
					digArgs = append(digArgs, "@"+nameserver)
				}

				for _, arg := range dig_args {
					if arg != "" {
						digArgs = append(digArgs, arg)
					}
				}

				//digArgs = append(digArgs, "+noall")
				//digArgs = append(digArgs, "+answer")
				//digArgs = append(digArgs, "+stats")
				digArgs = append(digArgs, "+time="+strconv.Itoa(int(timeout.Seconds())))
				digArgs = append(digArgs, "+tries="+strconv.Itoa(1))
				digArgs = append(digArgs, domain)
				//
				now := time.Now()
				log.Printf("Lookup Start           'dnsServer=%v domain=%v' with 'dig %v'\n", nameserver, domain, digArgs)
				_, err, elapsed, response, queryTimeStrLine := executeDig(debug, digArgs, now, timeout)

				if err == nil {
					log.Printf("Lookup OK              'dnsServer=%v domain=%v' : took %v -> response='%s'\n", nameserver, domain, elapsed, response+" "+queryTimeStrLine)
				} else {
					log.Printf("Lookup ERROR           'dnsServer=%v domain=%v' : took %v -> error=%v\n", nameserver, domain, elapsed, err)
					//retry
					digArgs = append(digArgs, "+tcp")
					log.Printf("Lookup RETRY TCP START 'dnsServer=%v domain=%v' with 'dig %v'\n", nameserver, domain, digArgs)
					_, err, elapsed, response, queryTimeStrLine := executeDig(debug, digArgs, now, timeout)
					if err == nil {
						log.Printf("Lookup RETRY TCP OK    'dnsServer=%v domain=%v' : took %v -> response='%s'\n", nameserver, domain, elapsed, response+" "+queryTimeStrLine)
					} else {
						log.Printf("Lookup RETRY TCP ERROR 'dnsServer=%v domain=%v' : took %v -> error=%v\n", nameserver, domain, elapsed, err)
					}
				}
				mutex.Lock()

				queryTotalCount.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Inc()
				if err != nil {
					querySuccess.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Set(0)
					queryFailCount.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Inc()
				} else {
					querySuccess.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Set(1)
					querySuccessCount.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Inc()
				}

				queryTime.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Set(float64(elapsed.Milliseconds()))
				mutex.Unlock()

				<-sem     // removes an int from sem, allowing another to proceed
				wg.Done() //if we do for,and need to wait for group
			}(d, n)
		}
	}

	wg.Wait()
	fmt.Printf("\n")
}

func main() {
	var timeoutStr = getEnv("TIMEOUT", default_timeout)
	var intervalStr = getEnv("INTERVAL", default_interval)

	//
	var timeout, timeoutErr = time.ParseDuration(timeoutStr)
	if timeoutErr != nil {
		fmt.Printf("Invalid TIMEOUT duration : `%s`", timeoutStr)
		log.Fatal(timeoutErr)
	}
	var interval, intervalErr = time.ParseDuration(intervalStr)
	if intervalErr != nil {
		fmt.Printf("Invalid INTERVAL duration : `%s`", intervalStr)
		log.Fatal(intervalErr)
	}
	var useGORESOLV = getEnvAsBool("GO_RESOLVER", false)
	var debug = getEnvAsBool("DEBUG", false)

	var domainsStr = getEnv("DOMAINS", default_domains)
	var domains = []string{}
	var lst = strings.Split(strings.ReplaceAll(domainsStr, " ", ""), ",")
	for _, n := range lst {
		if !listContains(domains, n) {
			domains = append(domains, n)
		}
	}
	var dnsServersStr = getEnv("NAMESERVERS", "DEFAULT")
	lst = strings.Split(strings.ReplaceAll(dnsServersStr, " ", ""), ",")
	var dnsServers = []string{}
	for _, n := range lst {
		if !listContains(dnsServers, n) {
			dnsServers = append(dnsServers, n)
		}
	}

	var digArgsStr = getEnv("DIG_ARGS", "")

	lst = strings.Split(digArgsStr, " ")
	var digArgs = []string{}
	for _, n := range lst {
		if !listContains(digArgs, n) {
			digArgs = append(digArgs, n)
		}
	}

	//
	fmt.Printf("Using Config :\n")
	if !useGORESOLV {
		fmt.Printf("\tResolver    : DIG \n")
		fmt.Printf("\tDIG_ARGS    : %v\n", digArgs)
	} else {
		fmt.Printf("\tResolver    : GO \n")
	}
	fmt.Printf("\tNAMESERVERS : %v\n", dnsServers)
	fmt.Printf("\tDomains     : %v\n", domains)
	fmt.Printf("\tTimeout     : %v \n", timeout)
	fmt.Printf("\tInterval    : %v \n", interval)
	fmt.Printf("\tDEBUG       : %v \n", debug)
	fmt.Printf("\n\n")

	resetCounters(domains, dnsServers)
	//
	time.Sleep(1 * time.Second)

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
		log.Println("Starting WebServer on port 8080")
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	//
	go func() {
		defer starTimer(debug, interval, useGORESOLV, domains, digArgs, dnsServers, timeout)
		if useGORESOLV {
			queryDomainsWithInternalGOResolver(debug, domains, dnsServers, timeout)
		} else {
			queryDomainsWithDIG(debug, domains, digArgs, dnsServers, timeout)
		}
	}()

	// Graceful Shutdown
	waitForShutdown(srv)
}

func starTimer(debug bool, interval time.Duration, useGORESOLV bool, domains []string, digArgs []string, dnsServers []string, timeout time.Duration) {

	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			//case t := <-ticker.C:
			//fmt.Println("Tick at", t.Format(time.RFC3339))
			case <-ticker.C:
				{
					if useGORESOLV {
						queryDomainsWithInternalGOResolver(debug, domains, dnsServers, timeout)
					} else {
						queryDomainsWithDIG(debug, domains, digArgs, dnsServers, timeout)
					}
				}
			}
		}
	}()
}

func resetCounters(domains []string, dnsServers []string) {
	mutex.Lock()

	for _, domain := range domains {
		for _, nameserver := range domains {
			queryTotalCount.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Set(0)
			querySuccessCount.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Set(0)
			queryFailCount.With(prometheus.Labels{"nameserver": nameserver, "domain": domain}).Set(0)
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
