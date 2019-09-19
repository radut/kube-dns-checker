### Motivation
* kube-dns is the most important component of a kubernetes cluster, therefore it needs to be working all time.
* If there is a problem with either of the nodes, dns-pods, misconfigured network, or routing issues -> kube-dns service -> kube-dns pods you must know about it
* trigger alerts based on error rate.


* container:8080/metrics, already has prometheus annotaitons so will be scraped automatically
* sum(rate(dns_query_fail_count[1m])) by (kubernetes_node,node_ip,job) / sum(rate(dns_query_total_count[1m])) by (kubernetes_node,node_ip,job) * 100 > 0
* check kuberentes/alert.rules 


### How To
```bash

go run main.go
go build main.go

docker build -t kube-dns-checker .
docker run -p8080:8080 kube-dns-checker


docker build -t radut/kube-dns-checker .
docker push radut/kube-dns-checker
```


### Environment Variables
```config
`DNS_SERVERS`    comma separated servers which are being used to query, default values "DEFAULT", which interogates the server from /etc/resolv.conf. 
`DOMAINS`        comma separated domains example "www.google.com,www.cloudflare.com", default value "www.google.com"
`TIMEOUT`        dig timeout in seconds, default 5
`TRIES`          dig tries, default 1
`INTERVAL`       interval to run checks(in seconds) default 3
  
```
 
 
