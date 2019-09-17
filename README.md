### How To
```bash

go run main.go

docker build -t kube-dns-checker .
docker run -p8080:8080 kube-dns-checker


docker build -t radut/kube-dns-checker .
docker push radut/kube-dns-checker
```
