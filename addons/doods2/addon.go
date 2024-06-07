// SPDX-License-Identifier: GPL-2.0-or-later

package doods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net/http"
	"nvr"
	"nvr/pkg/log"
	"nvr/pkg/storage"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var addon = struct {
	doodsIP      string
	detectorList detectors
	previewCache *previewCache

	sendRequest sendRequestFunc

	logger *log.Logger
}{}

func init() {
	nvr.RegisterLogSource([]string{"doods"})
	addon.previewCache = newPreviewCache()

	nvr.RegisterAppRunHook(func(ctx context.Context, app *nvr.App) error {
		addon.logger = app.Logger
		onEnv(app.Env)
		app.Router.Handle("/doods.mjs", app.Auth.Admin(serveDoodsMjs()))
		app.Router.Handle("/api/doods/preview/", app.Auth.Admin(addon.previewCache))
		onAppRun(ctx, app.WG)
		return nil
	})
	nvr.RegisterTplHook(modifyTemplates)
}

func onEnv(env storage.ConfigEnv) {
	configPath := env.ConfigDir + "/doods.json"
	var err error
	addon.doodsIP, err = readConfig(configPath)
	if err != nil {
		stdlog.Fatalf("doods: config: %v, %v\n", err, configPath)
		return
	}

	for {
		addon.detectorList, err = newFetcher(addon.doodsIP).fetchDetectors()
		if err != nil {
			fmt.Printf("doods: could not fetch detectors: %v %v\n"+
				"it can sometimes take a minute for doods to start\n"+
				"retrying..\n", addon.doodsIP, err)
			time.Sleep(3 * time.Second)
			continue
		}
		fmt.Printf("doods: found %d detectors:\n", len(addon.detectorList))
		for _, detector := range addon.detectorList {
			fmt.Printf("  %v\n", detector.Name)
		}
		return
	}
}

func onAppRun(ctx context.Context, wg *sync.WaitGroup) {
	logf := func(level log.Level, format string, a ...interface{}) {
		addon.logger.Log(log.Entry{
			Level: level,
			Src:   "doods",
			Msg:   fmt.Sprintf(format, a...),
		})
	}

	client := newClient(ctx, wg, logf, addon.doodsIP)
	addon.sendRequest = client.sendRequest

	wg.Add(1)
	go client.start()
}

// Config doods global configuration.
type Config struct {
	IP string `json:"ip"`
}

func readConfig(configPath string) (string, error) {
	if !dirExist(configPath) {
		if err := genConfig(configPath); err != nil {
			return "", fmt.Errorf("generate config: %w", err)
		}
	}

	file, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}

	var config Config
	if err := json.Unmarshal(file, &config); err != nil {
		return "", fmt.Errorf("unmarshal config: %w", err)
	}

	return config.IP, nil
}

var defaultConfig = Config{
	IP: "127.0.0.1:8080",
}

func genConfig(path string) error {
	data, _ := json.Marshal(defaultConfig)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return nil
}

func newFetcher(ip string) *fetcher {
	return &fetcher{
		url: "http://" + ip + "/detectors",
	}
}

type fetcher struct {
	url string
}

func (f *fetcher) fetchDetectors() (detectors, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var d getDetectorsResponce
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("unmarshal response: %v %w", body, err)
	}

	return d.Detectors, nil
}

type getDetectorsResponce struct {
	Detectors detectors `json:"detectors"`
}

func detectorByName(name string) (detector, error) {
	for _, detector := range addon.detectorList {
		if detector.Name == name {
			return detector, nil
		}
	}
	return detector{}, fmt.Errorf("%v: %w", name, os.ErrNotExist)
}

type detectors []detector

type detector struct {
	Name string `json:"name"`
	// Type string `json:"type"`
	Model  string   `json:"model"`
	Labels []string `json:"labels"`
	Width  int32    `json:"width"`
	Height int32    `json:"height"`
}

type detectRequest struct {
	ID           string  `json:"id"`
	DetectorName string  `json:"detector_name"`
	Data         *[]byte `json:"data"`
	// Preprocess   []string   `json:"preprocess"`
	Detect thresholds `json:"detect"`
}

type (
	thresholds map[string]float64
	detections []Detection
)

type detectResponse struct {
	ID          string     `json:"id"`
	Detections  detections `json:"detections"`
	ServerError string     `json:"error"`
	err         error
}

// Detection .
type Detection struct {
	Top        float32 `json:"top"`
	Left       float32 `json:"left"`
	Bottom     float32 `json:"bottom"`
	Right      float32 `json:"right"`
	Label      string  `json:"label"`
	Confidence float32 `json:"confidence"`
}

type client struct {
	wg         *sync.WaitGroup
	ctx        context.Context
	logf       log.Func
	url        string
	warmup     time.Duration
	timeout    time.Duration
	retrySleep time.Duration

	pendingRequests map[string]chan detectResponse
	requestChan     chan clientRequest
	responseChan    chan detectResponse
}

func newClient(
	ctx context.Context,
	wg *sync.WaitGroup,
	logf log.Func,
	doodsIP string,
) *client {
	return &client{
		wg:         wg,
		ctx:        ctx,
		logf:       logf,
		url:        "ws://" + doodsIP + "/detect",
		warmup:     1 * time.Second,
		timeout:    1000 * time.Millisecond,
		retrySleep: 3 * time.Second,

		pendingRequests: make(map[string]chan detectResponse),
		requestChan:     make(chan clientRequest),
		responseChan:    make(chan detectResponse),
	}
}

func (c *client) start() {
	time.Sleep(c.warmup)
	c.logf(log.LevelInfo, "starting client: %v", c.url)

	defer c.wg.Done()
	for {
		err := c.run()
		if err != nil {
			c.logf(log.LevelError, "client crashed: %v", err)
		} else {
			c.logf(log.LevelInfo, "client stopped")
		}

		select {
		case <-c.ctx.Done():
			return
		case <-time.After(c.retrySleep):
		}
	}
}

func (c *client) run() error {
	dialCtx, cancel2 := context.WithTimeout(c.ctx, c.timeout)
	defer cancel2()

	conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, c.url, nil) //nolint:bodyclose
	if err != nil {
		return fmt.Errorf("connect: %v %w", c.url, err)
	}
	go c.startReader(conn)

	cleanup := func() {
		conn.Close()
		for _, ret := range c.pendingRequests {
			ret <- detectResponse{err: context.Canceled}
		}
	}

	count := 0
	for {
		select {
		case r := <-c.requestChan:
			count++
			r.request.ID = strconv.Itoa(count)

			if err := conn.WriteJSON(r.request); err != nil {
				cleanup()
				<-c.responseChan
				return err
			}
			c.pendingRequests[r.request.ID] = r.response
			break

		case response := <-c.responseChan:
			if response.err != nil {
				cleanup()
				return fmt.Errorf("read json: %w", response.err)
			}

			if response.ServerError != "" {
				c.logf(log.LevelError, "server: %v", response.ServerError)
			}

			if response.ID == "" {
				continue
			}

			c.pendingRequests[response.ID] <- response
			delete(c.pendingRequests, response.ID)

		case <-c.ctx.Done():
			cleanup()
			<-c.responseChan
			return nil
		}
	}
}

func (c *client) startReader(conn *websocket.Conn) {
	var response detectResponse
	for {
		err := conn.ReadJSON(&response)
		if err != nil {
			c.responseChan <- detectResponse{err: err}
			return
		}
		c.responseChan <- response
	}
}

type sendRequestFunc func(context.Context, detectRequest) (*detections, error)

var errDoods = errors.New("doods error")

func (c *client) sendRequest(ctx context.Context, request detectRequest) (*detections, error) {
	res := make(chan detectResponse)
	req := clientRequest{
		request:  request,
		response: res,
	}

	select {
	case <-ctx.Done():
		return nil, context.Canceled
	case <-c.ctx.Done():
		return nil, context.Canceled
	case c.requestChan <- req:
	}

	select {
	case <-ctx.Done():
		go func() { <-res }()
		return nil, context.Canceled
	case response := <-res:
		if response.err != nil {
			return nil, response.err
		}
		if response.ServerError != "" {
			return nil, fmt.Errorf("%w: %v", errDoods, response.ServerError)
		}
		return &response.Detections, nil
	}
}

type clientRequest struct {
	request  detectRequest
	response chan detectResponse
}

func dirExist(path string) bool {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false
		}
		return false
	}
	return true
}

type previewCache struct {
	monitors map[string][]byte
	mu       *sync.Mutex
}

func newPreviewCache() *previewCache {
	return &previewCache{monitors: make(map[string][]byte), mu: &sync.Mutex{}}
}

func (cache *previewCache) Set(monitorID string, buf []byte) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	cache.monitors[monitorID] = buf
}

// ServeHTTP Implements http.Handler.
func (cache *previewCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	monitorID := strings.TrimPrefix(r.URL.Path, "/api/doods/preview/")

	buf, exist := cache.monitors[monitorID]
	if !exist {
		http.Error(w, "", http.StatusNotFound)
		return
	}

	_, err := w.Write(buf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
