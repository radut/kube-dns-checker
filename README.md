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
`DNS_SERVER`   which dns server to query, default empty, goes to default from /etc/resolv.conf
`DOMAINS`      comma separated domains example "www.google.com,www.cloudflare.com", default value "www.google.com"
`TIMEOUT`      dig timeout in seconds, default 5
`TRIES`        dig tries, default 1
`INTERVAL`     interval to run checks(in seconds) default 3
  
```
