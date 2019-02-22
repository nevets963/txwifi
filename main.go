// IoT Wifi Management


//todo: if in AP mode, and there are no connected clients ?all_sta?
// and there is a network defined in wpa_supplicant.conf
// ocassionally ?5 minutes? got into CL mode..
package main

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/bhoriuchi/go-bunyan/bunyan"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/txn2/txwifi/iotwifi"
)

var (
	Signal = make(chan string, 1)
)

// ApiReturn structures a message for returned API calls.
type ApiReturn struct {
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Payload interface{} `json:"payload"`
}

func main() {

	logConfig := bunyan.Config{
		Name:   "txwifi",
		Stream: os.Stdout,
		Level:  bunyan.LogLevelDebug,
	}

	blog, err := bunyan.CreateLogger(logConfig)
	if err != nil {
		panic(err)
	}

	blog.Info("Starting IoT Wifi...")

	//Todo: is a queue of 1 blocking wpa,hostapd,dnsmasq?
	messages := make(chan iotwifi.CmdMessage, 1)


	cfgUrl := setEnvIfEmpty("IOTWIFI_CFG", "cfg/wificfg.json")
	port := setEnvIfEmpty("IOTWIFI_PORT", "8080")
	allowKill := setEnvIfEmpty("WIFI_ALLOW_KILL","false")
	static := setEnvIfEmpty("IOTWIFI_STATIC", "/static/")

	go iotwifi.HandleLog(blog, messages)
	go iotwifi.RunWifi(blog, messages, cfgUrl, Signal)
	Signal <- "CL"
	go iotwifi.DetectWifi(blog, Signal)
	wpacfg := iotwifi.NewWpaCfg(blog, cfgUrl)

	apiPayloadReturn := func(w http.ResponseWriter, message string, payload interface{}) {
		apiReturn := &ApiReturn{
			Status:  "OK",
			Message: message,
			Payload: payload,
		}
		ret, _ := json.Marshal(apiReturn)

		w.Header().Set("Content-Type", "application/json")
		w.Write(ret)
	}

	// marshallPost populates a struct with json in post body
	marshallPost := func(w http.ResponseWriter, r *http.Request, v interface{}) {
		bytes, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			blog.Error(err)
			return
		}

		defer r.Body.Close()

		decoder := json.NewDecoder(strings.NewReader(string(bytes)))

		err = decoder.Decode(&v)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			blog.Error(err)
			return
		}
	}

	// common error return from api
	retError := func(w http.ResponseWriter, err error) {
		apiReturn := &ApiReturn{
			Status:  "FAIL",
			Message: err.Error(),
		}
		ret, _ := json.Marshal(apiReturn)

		w.Header().Set("Content-Type", "application/json")
		w.Write(ret)
	}

	// handle /status POSTs json in the form of iotwifi.WpaConnect
	statusHandler := func(w http.ResponseWriter, r *http.Request) {

		status, _ := wpacfg.Status()

		apiPayloadReturn(w, "status", status)
	}

	// handle /connect POSTs json in the form of iotwifi.WpaConnect
	connectHandler := func(w http.ResponseWriter, r *http.Request) {
		var creds iotwifi.WpaCredentials
		marshallPost(w, r, &creds)

		blog.Info("Connect Handler Got: ssid:|%s| psk:|redacted|", creds.Ssid)

		go wpacfg.ConnectNetwork(creds)

		apiReturn := &ApiReturn{
			Status:  "OK",
			Message: "Connection",
			Payload: "Attempting to connect to " +creds.Ssid,
		}

		ret, err := json.Marshal(apiReturn)
		if err != nil {
			retError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(ret)
		Signal <- "CL"
	}

	// scan for wifi networks
	scanHandler := func(w http.ResponseWriter, r *http.Request) {
		blog.Info("Got Scan")
		wpaNetworks, err := wpacfg.ScanNetworks()
		if err != nil {
			retError(w, err)
			return
		}

		apiReturn := &ApiReturn{
			Status:  "OK",
			Message: "Networks",
			Payload: wpaNetworks,
		}

		ret, err := json.Marshal(apiReturn)
		if err != nil {
			retError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(ret)
	}

	//ToDo: remove
	apHandler := func(w http.ResponseWriter, r *http.Request) {
		blog.Info("Got ap")

		apiReturn := &ApiReturn{
			Status:  "OK",
			Message: "Networks",
			Payload: "ap",
		}
		ret, _ := json.Marshal(apiReturn)

		w.Header().Set("Content-Type", "application/json")
		w.Write(ret)
		Signal <- "AP"
	}

	//ToDo: remove
	clHandler := func(w http.ResponseWriter, r *http.Request) {
		blog.Info("Got cl")

		apiReturn := &ApiReturn{
			Status:  "OK",
			Message: "Networks",
			Payload: "cl",
		}
		ret, _ := json.Marshal(apiReturn)

		w.Header().Set("Content-Type", "application/json")
		w.Write(ret)
		Signal <- "CL"
	}

	// kill the application
	killHandler := func(w http.ResponseWriter, r *http.Request) {
		messages <- iotwifi.CmdMessage{Id: "kill"}

		apiReturn := &ApiReturn{
			Status:  "OK",
			Message: "Killing service.",
		}
		ret, err := json.Marshal(apiReturn)
		if err != nil {
			retError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(ret)
	}

	// common log middleware for api
	logHandler := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			staticFields := make(map[string]interface{})
			staticFields["remote"] = r.RemoteAddr
			staticFields["method"] = r.Method
			staticFields["url"] = r.RequestURI

			blog.Info(staticFields, "HTTP")
			next.ServeHTTP(w, r)
		})
	}

	// setup router and middleware
	r := mux.NewRouter()
	r.Use(logHandler)

	// set app routes
	r.HandleFunc("/status", statusHandler)
	r.HandleFunc("/connect", connectHandler).Methods("POST")
	r.HandleFunc("/scan", scanHandler)
	//Todo: these are temp, remove them
	r.HandleFunc("/ap", apHandler)
	r.HandleFunc("/cl", clHandler)
	// ---
	if allowKill == "true" {
		r.HandleFunc("/kill", killHandler)
	}
	r.PathPrefix("/").Handler(http.FileServer(http.Dir(static)))
	http.Handle("/", r)

	// CORS
	headersOk := handlers.AllowedHeaders([]string{"Content-Type", "Authorization", "Content-Length", "X-Requested-With", "Accept", "Origin"})
	originsOk := handlers.AllowedOrigins([]string{"*"})
	methodsOk := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "OPTIONS", "DELETE"})

	// serve http
	blog.Info("HTTP Listening on " + port)
	http.ListenAndServe(":"+port, handlers.CORS(originsOk, headersOk, methodsOk)(r))

}

// getEnv gets an environment variable or sets a default if
// one does not exist.
func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		return fallback
	}

	return value
}

// setEnvIfEmp<ty sets an environment variable to itself or
// fallback if empty.
func setEnvIfEmpty(env string, fallback string) (envVal string) {
	envVal = getEnv(env, fallback)
	os.Setenv(env, envVal)

	return envVal
}
