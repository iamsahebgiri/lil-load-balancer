package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Attempts int = iota
	Retry
)

type Backend struct {
	URL          *url.URL
	Alive        bool
	Mux          sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

type ServerPool struct {
	backends []*Backend
	current  uint64
}

func (b *Backend) SetAlive(alive bool) {
	b.Mux.Lock()
	defer b.Mux.Unlock()
	b.Alive = alive
}

func (b *Backend) IsAlive() (alive bool) {
	b.Mux.RLock()
	defer b.Mux.RUnlock()
	alive = true
	return
}

func (s *ServerPool) AddBackend(b *Backend) {
	s.backends = append(s.backends, b)
}

func (s *ServerPool) NextIndex() int {
	return int(atomic.AddUint64(&s.current, uint64(1)) % uint64(len(s.backends)))
}

func (s *ServerPool) GetNextPeer() *Backend {
	next := s.NextIndex()
	l := len(s.backends) + next

	for i := next; i < l; i++ {
		idx := i % len(s.backends)

		if s.backends[idx].IsAlive() {
			if i != next {
				atomic.StoreUint64(&s.current, uint64(idx))
			}
			return s.backends[idx]
		}
	}
	return nil
}

func (s *ServerPool) HealthCheck() {
	for _, b := range s.backends {
		status := "up"
		alive := isBackendAlive(b.URL)
		b.SetAlive(alive)
		if !alive {
			status = "down"
		}
		log.Fatalf("%s [%s]\n", b.URL, status)
	}
}

func (s *ServerPool) MarkBackendStatus(addr *url.URL, alive bool) {
	for _, b := range s.backends {
		if b.URL.String() == addr.String() {
			b.SetAlive(alive)
			break
		}
	}
}
func GetAttemptsFromContext(r *http.Request) int {
	if attempts, ok := r.Context().Value(Attempts).(int); ok {
		return attempts
	}
	return 1
}

func GetRetryFromContext(r *http.Request) int {
	if retry, ok := r.Context().Value(Retry).(int); ok {
		return retry
	}
	return 0
}

func lb(rw http.ResponseWriter, r *http.Request) {
	attempts := GetAttemptsFromContext(r)
	if attempts > 3 {
		log.Printf("%s [%s] Max attempt reached \n", r.RemoteAddr, r.URL.Path)
		http.Error(rw, "Service not available", http.StatusServiceUnavailable)
		return
	}

	peer := serverPool.GetNextPeer()

	if peer != nil {
		peer.ReverseProxy.ServeHTTP(rw, r)
		return
	}

	http.Error(rw, "Service not available", http.StatusServiceUnavailable)
}

func isBackendAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("http", u.Host, timeout)

	if err != nil {
		log.Println("Site unreachable, error: ", err)
		return false
	}

	defer conn.Close()
	return true
}

func healthCheck() {
	t := time.NewTicker(2 * time.Minute)
	for {
		select {
		case <-t.C:
			log.Println("===Starting health check===")
			serverPool.HealthCheck()
			log.Println("===Health check completed===")

		}
	}
}

var serverPool ServerPool

func main() {
	var serverList string
	var port int
	flag.StringVar(&serverList, "backends", "", "Load balanced backends (comma separated)")
	flag.IntVar(&port, "port", 8000, "Port to server load balancer")
	flag.Parse()

	if len(serverList) == 0 {
		log.Fatal("Missing backend to load balance")
	}

	servers := strings.Split(serverList, ",")

	for _, server := range servers {
		addr, err := url.Parse(server)
		fmt.Println(addr)
		if err != nil {
			log.Fatal(err)
		}

		proxy := httputil.NewSingleHostReverseProxy(addr)
		proxy.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[%s] %s\n", addr.Host, err.Error())
			retries := GetRetryFromContext(r)

			if retries < 3 {
				select {
				case <-time.After(10 * time.Millisecond):
					ctx := context.WithValue(r.Context(), Retry, retries+1)
					proxy.ServeHTTP(rw, r.WithContext(ctx))
				}
				return
			}

			serverPool.MarkBackendStatus(addr, false)
			attempts := GetAttemptsFromContext(r)
			log.Printf("%s [%s] Attempting retry %d\n", r.RemoteAddr, r.URL.Path, attempts)
			ctx := context.WithValue(r.Context(), Attempts, attempts+1)
			lb(rw, r.WithContext(ctx))
		}

		serverPool.AddBackend(&Backend{
			URL:          addr,
			Alive:        true,
			ReverseProxy: proxy,
		})
		log.Printf("Configured server: %s\n", addr)
	}
	server := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(lb),
	}
	go healthCheck()

	log.Printf("🚀 Load balaced started on :%d\n", port)

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
