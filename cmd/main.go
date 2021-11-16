package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/pandora-go-sdk/base"
)

/*
支持三个接口
1. GET  /[index-pattern]/_stats
2. GET /[index-pattern]/_mapping
3. POST /_msearch
前缀logdb
*/
type LogdbProxy struct {
	Proxy      *Proxy
	HTTPClient *http.Client
}

func NewLogdbProxy(proxy *Proxy) (logdbProxy *LogdbProxy, err error) {
	if !strings.HasPrefix(proxy.LogdbURL, "http://") && !strings.HasPrefix(proxy.LogdbURL, "https://") {
		err = fmt.Errorf("endpoint should start with 'http://' or 'https://'")
		return
	}
	if strings.HasSuffix(proxy.LogdbURL, "/") {
		proxy.LogdbURL = proxy.LogdbURL[:len(proxy.LogdbURL)-2]
	}

	var t = &http.Transport{
		Dial: (&net.Dialer{
			Timeout:   time.Second * time.Duration(proxy.ResponseTimout),
			KeepAlive: 30 * time.Second,
		}).Dial,
		ResponseHeaderTimeout: time.Second * time.Duration(proxy.ResponseTimout),
	}
	logdbProxy = &LogdbProxy{
		HTTPClient: &http.Client{Transport: t},
		Proxy:      proxy,
	}
	return logdbProxy, nil
}

type proxyRequest struct {
	method       string
	upstreamPath string
	req          *http.Request
}

func (logdbProxy *LogdbProxy) buildRequest(req *http.Request) (*proxyRequest, error) {
	if strings.HasPrefix(req.URL.Path, "/logdbkibana/") {
		err := req.ParseForm()
		if err != nil {
			return nil, err
		}
		var upstreamPath = req.URL.Path
		return &proxyRequest{req.Method, upstreamPath, req}, nil
	} else if strings.HasPrefix(req.URL.Path, "/logdb/") {
		err := req.ParseForm()
		if err != nil {
			return nil, err
		}
		switch req.Method {
		case "POST":
			u := req.URL
			paths := strings.Split(u.Path, "/")
			if len(paths) == 3 && paths[2] == "_msearch" {
				upstreamPath := "/" + "msearch"
				return &proxyRequest{req.Method, upstreamPath, req}, nil
			}
		case "GET":
			u := req.URL
			paths := strings.Split(u.Path, "/")
			if len(paths) == 3 && paths[2] == "_msearch" {
				upstreamPath := "/" + "msearch"
				return &proxyRequest{"POST", upstreamPath, req}, nil
			} else if len(paths) == 4 {
				switch paths[3] {
				case "_stats":
					upstreamPath := "/" + paths[2] + "/" + "stats"
					return &proxyRequest{req.Method, upstreamPath, req}, nil
				case "_mapping":
					upstreamPath := "/" + paths[2] + "/" + "mapping"
					return &proxyRequest{req.Method, upstreamPath, req}, nil
				}
			}
		}
	}
	return nil, errors.New("not found path " + req.URL.Path)
}

func (logdbProxy *LogdbProxy) LogdbProxy(res http.ResponseWriter, req *http.Request) {
	if logdbProxy.Proxy.CrossDomain {
		res.Header().Set("Access-Control-Allow-Origin", "*")
		res.Header().Set("Access-Control-Allow-Headers", "Authorization,Origin, X-Requested-With, Content-Type, Accept, X-Appid")
		res.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		res.Header().Set("Content-Type", "application/json")
	}
	proxyReq, err := logdbProxy.buildRequest(req)
	if err != nil {
		res.WriteHeader(http.StatusNotFound)
		_, _ = res.Write([]byte("No route for " + req.URL.Path))
		return
	}
	logdbProxy.ProxyRequest(res, proxyReq)

}

func (logdbProxy *LogdbProxy) ProxyRequest(res http.ResponseWriter, request *proxyRequest) {
	if logdbProxy.Proxy.Dump {
		b, _ := httputil.DumpRequest(request.req, true)
		fmt.Println(string(b))
	}

	upstreamPath := "/v5" + request.upstreamPath
	ak, sk, err := getAKSKFromHeader(request.req.Header)
	if err != nil {
		res.WriteHeader(500)
		_, _ = res.Write([]byte(err.Error()))
		return
	}

	if ak == "" {
		ak = logdbProxy.Proxy.Ak
		sk = logdbProxy.Proxy.Sk
	}

	upstreamRequest, err := http.NewRequest(request.method, "", request.req.Body)
	if err != nil {
		res.WriteHeader(500)
		_, _ = res.Write([]byte(err.Error()))
		return
	}
	upstreamRequest.URL, err = url.Parse(logdbProxy.Proxy.LogdbURL + upstreamPath)
	if err != nil {
		res.WriteHeader(500)
		_, _ = res.Write([]byte(err.Error()))
		return
	}
	upstreamRequest.Header.Set("Connection", "close")
	upstreamRequest.Header.Set(base.HTTPHeaderContentType, "text/plain")
	upstreamRequest.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	err = base.Sign(ak, sk, upstreamRequest)
	if err != nil {
		res.WriteHeader(500)
		_, _ = res.Write([]byte(err.Error()))
		return
	}

	upstreamResponse, err := logdbProxy.HTTPClient.Do(upstreamRequest)
	if err != nil {
		res.WriteHeader(502)
		_, _ = res.Write([]byte(err.Error()))
		return
	}

	//fmt.Printf("%v", upstreamResponse.Header)
	reqid := upstreamResponse.Header.Get("X-Reqid")
	res.Header().Set("X-Reqid", reqid)
	res.WriteHeader(upstreamResponse.StatusCode)

	defer upstreamResponse.Body.Close()
	pipeRead(upstreamResponse.Body, res)
}

func pipeRead(r io.Reader, w io.Writer) {
	var buff = make([]byte, 64*1024)
	for {
		l, e := r.Read(buff)
		if e != nil && e != io.EOF {
			return
		}
		_, _ = w.Write(buff[:l])
		if e == io.EOF {
			return
		}
	}
}

type Proxy struct {
	Port           int    `json:"port"`
	CrossDomain    bool   `json:"cross_domain"`
	ResponseTimout int64  `json:"response_timeout"`
	LogdbURL       string `json:"logdbHost"`
	Ak             string `json:"ak"`
	Sk             string `json:"sk"`
	Dump           bool   `json:"dump"`
}

func getAKSKFromHeader(header http.Header) (ak, sk string, err error) {
	auth := header.Get("Authorization")
	if auth == "" {
		return
	}
	authSlic := strings.Split(auth, " ")
	if len(auth) < 2 {
		return "", "", errors.New("invalid Authorization header " + header.Get("Authorization"))
	}
	aksk, err := base64.StdEncoding.DecodeString(authSlic[1])
	if err != nil {
		return "", "", err
	}
	akskslic := strings.Split(string(aksk), ":")
	if len(akskslic) < 2 {
		return "", "", errors.New("invalid ak sk format")
	}
	ak, sk = akskslic[0], akskslic[1]
	return
}

func main() {
	var file string
	flag.StringVar(&file, "f", "", "proxy config file")
	flag.Parse()
	if file == "" {
		flag.Usage()
		return
	}
	f, err := os.Open(file)
	if err != nil {
		fmt.Println("file open failed", err.Error())
		return
	}

	var proxy Proxy
	d := json.NewDecoder(f)
	err = d.Decode(&proxy)
	if err != nil {
		_ = f.Close()
		log.Fatal("config.Load failed:", err)
		return
	}
	_ = f.Close()

	logdbProxy, err := NewLogdbProxy(&proxy)
	if err != nil {
		log.Fatal("initial logdb proxy failed:", err)
		return
	}
	http.HandleFunc("/", logdbProxy.LogdbProxy)
	err = http.ListenAndServe(":"+strconv.Itoa(proxy.Port), nil)
	if err != nil {
		log.Fatal("start failed:", err)
	}
}
