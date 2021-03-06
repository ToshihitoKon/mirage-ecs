package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
)

var app *Mirage

type Mirage struct {
	Config       *Config
	WebApi       *WebApi
	ReverseProxy *ReverseProxy
	ECS          *ECS
}

func Setup(cfg *Config) {
	m := &Mirage{
		Config:       cfg,
		WebApi:       NewWebApi(cfg),
		ReverseProxy: NewReverseProxy(cfg),
		ECS:          NewECS(cfg),
	}

	app = m
}

func Run() {
	// launch server
	var wg sync.WaitGroup
	for _, v := range app.Config.Listen.HTTP {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			laddr := fmt.Sprintf("%s:%d", app.Config.Listen.ForeignAddress, port)
			listener, err := net.Listen("tcp", laddr)
			if err != nil {
				log.Printf("[error] cannot listen %s", laddr)
				return
			}

			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
				app.ServeHTTPWithPort(w, req, port)
			})

			log.Println("[info] listen port:", port)
			http.Serve(listener, mux)
		}(v.ListenPort)
	}

	log.Println("[info] Launch succeeded!")

	wg.Wait()
}

func (m *Mirage) ServeHTTPWithPort(w http.ResponseWriter, req *http.Request, port int) {
	host := strings.ToLower(strings.Split(req.Host, ":")[0])

	switch {
	case m.isWebApiHost(host):
		m.WebApi.ServeHTTP(w, req)

	case m.isDockerHost(host):
		m.ReverseProxy.ServeHTTPWithPort(w, req, port)

	default:
		if req.URL.Path == "/" {
			// otherwise root returns 200 (for healthcheck)
			http.Error(w, "mirage-ecs", http.StatusOK)
		} else {
			// return 404
			log.Printf("[warn] host %s is not found", host)
			http.NotFound(w, req)
		}
	}

}

func (m *Mirage) isDockerHost(host string) bool {
	if strings.HasSuffix(host, m.Config.Host.ReverseProxySuffix) {
		subdomain := strings.ToLower(strings.Split(host, ".")[0])
		return m.ReverseProxy.Exists(subdomain)
	}

	return false
}

func (m *Mirage) isWebApiHost(host string) bool {
	return isSameHost(m.Config.Host.WebApi, host)
}

func isSameHost(s1 string, s2 string) bool {
	lower1 := strings.Trim(strings.ToLower(s1), " ")
	lower2 := strings.Trim(strings.ToLower(s2), " ")

	return lower1 == lower2
}
