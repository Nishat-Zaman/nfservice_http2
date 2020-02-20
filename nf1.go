package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/net/http2"
)

var httpVersion = flag.Int("version", 2, "HTTP version")

//HTTPConfig contains the configuration for the HTTP 1.1
type HTTPConfig struct {
	ApiEndpoint string `json:"apiendpoint"`
	NfEndpoint  string `json:"nfendpoint"`
}

// Config contains NF Module Configuration Data Structure
type Config struct {
	// API Root for the remote NF
	RemoteNfAPIRoot          string `json:"remotenfapiroot"`
	LocalNfAPIRoot           string `json:"localapirootprefix"`
	NfNotificationResURIPath string `json:"nfNotificationResUriPath"`
	HTTPConfig               HTTPConfig
}

type NF struct {
	Location string `json:"location"`
	Time     string `json:"time"`
}

// Path for NEF Configuration file
const cfgPath string = "config/nf1.json"

var cfg Config
var nf2Post chan bool
var nfBody NF

func main() {

	// Read the configuration
	err := loadJSONConfig(cfgPath, &cfg)
	if err != nil {
		log.Printf("Failed to load NF configuration: %v", err)
		return
	}

	nf2Post = make(chan bool, 1)

	// Start the Servers in a different context
	// Creating a context. This context will be used for following:
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	/* Subscribing to os Interrupt/Signal - SIGTERM and waiting for
	 * notification in a separate go routine. When the notification is received
	 * the created context will be canceled */
	osSignalCh := make(chan os.Signal, 1)
	signal.Notify(osSignalCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-osSignalCh
		log.Printf("Received signal: %#v", sig)
		cancel()
	}()
	log.Print("Starting NF App servers")
	_ = RunServer(ctx, &cfg)

}

// LoadJSONConfig reads a file located at configPath and unmarshals it to
// config structure
func loadJSONConfig(configPath string, cfg *Config) error {
	cfgData, err := ioutil.ReadFile(filepath.Clean(configPath))
	if err != nil {
		return err
	}
	err = json.Unmarshal(cfgData, cfg)
	if err != nil {
		return err
	}

	// Check if configuration is valid
	if cfg.HTTPConfig.ApiEndpoint == "" {
		log.Print("API HTTP2 Server endpoint  not configured")
		return errors.New("API HTTP2 Server endpoint  not configured")
	}

	if cfg.HTTPConfig.NfEndpoint == "" {
		log.Print("NF HTTP2 Server endpoint not configured")
		return errors.New("NF HTTP2 Server endpoint  not configured")
	}

	/* Check the url type - if its https or http */
	u, err := url.Parse(cfg.RemoteNfAPIRoot)
	if err != nil || u.Scheme != "https" {
		log.Printf("RemoteNfAPIRoot URl error :%v", err)
		return err
	}
	printConfig(cfg)
	return err

}
func printConfig(cfg *Config) {

	log.Printf("********************* NF CONFIGURATION ******************")
	log.Printf("Remote API: %v", cfg.RemoteNfAPIRoot)
	log.Printf("Local NF API Rootprefix :%v", cfg.LocalNfAPIRoot)
	log.Printf("API End Point: %v", cfg.HTTPConfig.ApiEndpoint)
	log.Printf("NF End Point: %v", cfg.HTTPConfig.NfEndpoint)
	log.Printf("*************************************************************")

}

func RunServer(ctx context.Context, cfg *Config) error {

	var apiserver, nfserver *http.Server

	apiserver = &http.Server{
		Addr:           cfg.HTTPConfig.ApiEndpoint,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	err1 := http2.ConfigureServer(apiserver, &http2.Server{})
	if err1 != nil {
		log.Print("failed at configuring HTTP2 server")
	}
	nfserver = &http.Server{
		Addr:           cfg.HTTPConfig.NfEndpoint,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	err := http2.ConfigureServer(nfserver, &http2.Server{})
	if err != nil {
		log.Print("failed at configuring HTTP2 server")
	}
	http.HandleFunc("/nf2loc", apiHandler)
	http.HandleFunc("/nf1", nf1Handler)

	stopServerCh := make(chan bool, 2)

	/* Go Routine is spawned here for listening for cancellation event on
	 * context */
	go func(stopServerCh chan bool) {
		<-ctx.Done()
		log.Print("Executing graceful stop for API HTTP2 Server")
		if err := apiserver.Close(); err != nil {
			log.Printf("Could not close API HTTP2 server: %#v", err)
		}
		log.Printf("API HTTP2 server stopped")

		log.Print("Executing graceful stop for NF HTTP2 Server")
		if err := nfserver.Close(); err != nil {
			log.Printf("Could not close NF HTTP2 server: %#v", err)
		}
		log.Printf("NF HTTP2 server stopped")
		stopServerCh <- true
	}(stopServerCh)
	/* Go Routine is spawned here for starting API HTTP Server */
	go startHTTPServer_n(apiserver, stopServerCh, "API")
	/* Go Routine is spawned here for starting NF HTTP Server */
	go startHTTPServer_n(nfserver, stopServerCh, "NF")

	<-stopServerCh
	<-stopServerCh
	log.Print("Exiting NF App servers")
	return nil
}

/* starting HTTP Server */
func startHTTPServer(server *http.Server,
	stopServerCh chan bool, name string) {
	if server != nil {
		log.Printf("%s HTTP 2 listening on %s", name, server.Addr)

		if err := server.ListenAndServe(); err != nil {
			log.Printf("HTTP2 server error: " + err.Error())
		}
	}
	stopServerCh <- true
}
func startHTTPServer_n(server *http.Server,
	stopServerCh chan bool, name string) {
	if server != nil {
		log.Printf("%s HTTP 2 listening on %s", name, server.Addr)
		if err := server.ListenAndServeTLS("certs/server-cert.pem", "certs/server-key.pem"); err != nil {
			log.Printf("HTTP2 server error: " + err.Error())
		}
		/* if err := server.ListenAndServe(); err != nil {
			log.Printf("HTTP server error: " + err.Error())
		} */
	}
	stopServerCh <- true
}
func apiHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	/* Dump the request received */
	dump, err := httputil.DumpRequest(r, true)
	if err != nil {
		http.Error(w, fmt.Sprint(err), http.StatusInternalServerError)
		return
	}
	log.Println(string(dump))

	var client http.Client
	var nf2body NF

	nf2body.Time = time.Now().String()
	nf2body.Location = cfg.LocalNfAPIRoot +
		cfg.HTTPConfig.NfEndpoint + "/nf1"
	client = http.Client{Timeout: 30 * time.Second}

	requestBody, err := json.Marshal(nf2body)

	caCert, err := ioutil.ReadFile("certs/root-ca-cert.pem")
	if err != nil {
		log.Fatalf("Reading server certificate : %s", err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		RootCAs: caCertPool,
	}

	client.Transport = &http2.Transport{
		TLSClientConfig: tlsConfig,
	}

	// Set request type as POST
	req, _ := http.NewRequest("POST", cfg.RemoteNfAPIRoot, bytes.NewBuffer(requestBody))
	// Add user-agent header and content-type header
	req.Header.Set("User-Agent", "NF1")
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	log.Print("Sending a request to the server")
	resp, err := client.Do(req)
	if err != nil {
		log.Print(err)
		return
	}
	defer func() {
		err = resp.Body.Close()
		if err != nil {
			log.Print("response body was not closed properly")
		}
	}()

	log.Printf("Headers in the response %d =>", resp.StatusCode)
	for k, v := range resp.Header {
		log.Printf("%q:%q\n", k, v)
	}
	log.Printf("Body in the response =>")
	respbody, err := ioutil.ReadAll(resp.Body)
	log.Print(string(respbody))

	// wait for the response
	log.Printf("Waiting for the POST req")
	<-nf2Post
	log.Printf("POST request received")

	respbody, err = json.Marshal(nfBody)
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(respbody)
	if err != nil {
		log.Printf("Write Failed: %v", err)
	}
}

func nf1Handler(w http.ResponseWriter, r *http.Request) {
	/* Dump the request received */
	dump, err := httputil.DumpRequest(r, true)
	if err != nil {
		http.Error(w, fmt.Sprint(err), http.StatusInternalServerError)
		return
	}
	log.Println(string(dump))

	fmt.Fprintf(w, "Hello Thanks !!!")

	/* Read the response and report success if json content is proper */
	if r.Body == nil {
		log.Print("Empty Body")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Retrieve the NF2 information from the request
	if err := json.NewDecoder(r.Body).Decode(&nfBody); err != nil {
		log.Printf("Body parse error: %s", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// now release the nf2 post channel
	nf2Post <- true
	log.Printf("NF1 Handler Completed")
}
