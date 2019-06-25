package main

import (
	"fmt"
	redigo "github.com/gomodule/redigo/redis"
	"github.com/ssgo/config"
	"github.com/ssgo/discover"
	"github.com/ssgo/log"
	"github.com/ssgo/redis"
	"github.com/ssgo/s"
	"github.com/ssgo/standard"
	"github.com/ssgo/u"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var redisPool *redis.Redis
var pubsubRedisPool *redis.Redis
var updateLock = sync.Mutex{}

type regexProxiesInfo struct {
	Value string
	Regex regexp.Regexp
}

type regexRewriteInfo struct {
	To    string
	Regex regexp.Regexp
}

var _proxies = map[string]string{}
var _regexProxies = map[string]*regexProxiesInfo{}
var _regexRewrites = map[string]*regexRewriteInfo{}
var gatewayConfig = struct {
	CheckInterval int
	Proxies       map[string]string
	Rewrites      map[string]string
	Prefix        string
}{}

var logger = log.New(u.ShortUniqueId())
var proxiesKey = "_proxies"
var proxiesChannel = "_CH_proxies"
var rewritesKey = "_rewrites"
var rewritesChannel = "_CH_rewrites"

func logInfo(info string, extra ...interface{}) {
	logger.Info("Gateway: "+info, extra...)
}

func logError(error string, extra ...interface{}) {
	logger.Error("Gateway: "+error, extra...)
}

func main() {
	discover.Init()
	s.Init()
	redisPool = redis.GetRedis(discover.Config.Registry, logger)
	confForPubSub := *redisPool.Config
	confForPubSub.IdleTimeout = -1
	confForPubSub.ReadTimeout = -1
	pubsubRedisPool = redis.NewRedis(&confForPubSub, logger)

	config.LoadConfig("gateway", &gatewayConfig)
	if gatewayConfig.CheckInterval == 0 {
		gatewayConfig.CheckInterval = 10
	} else if gatewayConfig.CheckInterval < 3 {
		gatewayConfig.CheckInterval = 3
	}

	if gatewayConfig.Prefix != "" {
		proxiesKey = fmt.Sprint("_", gatewayConfig.Prefix, "proxies")
		proxiesChannel = fmt.Sprint("_CH_", gatewayConfig.Prefix, "proxies")
		rewritesKey = fmt.Sprint("_", gatewayConfig.Prefix, "rewrites")
		rewritesChannel = fmt.Sprint("_CH_", gatewayConfig.Prefix, "rewrites")
	}
	s.SetRewriteBy(rewrite)
	s.SetProxyBy(proxy)
	as := s.AsyncStart()

	syncProxies()
	syncRewrites()
	go subscribe()

	for {
		for i := 0; i < gatewayConfig.CheckInterval; i++ {
			time.Sleep(time.Second * 1)
			if !s.IsRunning() {
				break
			}
		}
		syncProxies()
		syncRewrites()
		if !s.IsRunning() {
			break
		}
	}
	as.Stop()
}

func rewrite(request *http.Request) (toPath string, rewrite bool) {
	if len(_regexRewrites) > 0 {

		requestUrl := fmt.Sprint(request.Header.Get("X-Scheme"), "://", request.Host, request.RequestURI)

		for _, rr := range _regexRewrites {
			finds := rr.Regex.FindAllStringSubmatch(requestUrl, 20)
			if len(finds) > 0 {
				to := rr.To
				if len(finds[0]) > 1 {
					for i := 1; i < len(finds[0]); i++ {
						varName := fmt.Sprintf("$%d", i)
						to = strings.ReplaceAll(to, varName, finds[0][i])
					}
					return to, true
				}
			}
		}
	}

	// 不进行代理
	return "", false
}

func proxy(request *http.Request) (toApp, toPath *string, headers map[string]string) {
	outHeaders := map[string]string{
		standard.DiscoverHeaderFromApp:  "gateway",
		standard.DiscoverHeaderFromNode: s.GetServerAddr(),
	}

	// 匹配二级目录
	paths := strings.SplitN(request.RequestURI, "/", 4)
	if len(paths) == 4 {
		p1 := "/" + paths[1] + "/" + paths[2]
		p2 := "/" + paths[3]

		// Host + Path 匹配
		a := _proxies[request.Host+p1]
		if a != "" {
			outHeaders["Proxy-Path"] = p1
			return &a, &p2, outHeaders
		}

		// Path 匹配
		a = _proxies[p1]
		if a != "" {
			outHeaders["Proxy-Path"] = p1
			return &a, &p2, outHeaders
		}
	}

	// 匹配一级目录
	paths = strings.SplitN(request.RequestURI, "/", 3)
	if len(paths) == 3 {
		p1 := "/" + paths[1]
		p2 := "/" + paths[2]

		// Host + Path 匹配
		a := _proxies[request.Host+p1]
		if a != "" {
			outHeaders["Proxy-Path"] = p1
			return &a, &p2, outHeaders
		}

		// Path 匹配
		a = _proxies[p1]
		if a != "" {
			outHeaders["Proxy-Path"] = p1
			return &a, &p2, outHeaders
		}
	}

	// 匹配 Host
	a := _proxies[request.Host]
	if a != "" {
		return &a, &request.RequestURI, outHeaders
	}

	// 模糊匹配
	if len(_regexProxies) > 0 {
		requestUrl := request.Host + request.RequestURI
		for _, rp := range _regexProxies {
			finds := rp.Regex.FindAllStringSubmatch(requestUrl, 20)
			if len(finds) > 0 && len(finds[0]) > 2 {
				pos := strings.Index(request.RequestURI, finds[0][2])
				if pos > 0 {
					outHeaders["Proxy-Path"] = request.RequestURI[0:pos]
				}
				return &finds[0][1], &finds[0][2], outHeaders
			}
		}
	}

	// 不进行代理
	return
}

func syncProxies() {
	proxies := map[string]string{}
	regexProxies := map[string]*regexProxiesInfo{}
	updated1 := updateProxies(&proxies, &regexProxies, gatewayConfig.Proxies)

	updateLock.Lock()
	updated2 := updateProxies(&proxies, &regexProxies, redisPool.Do("HGETALL", proxiesKey).StringMap())
	if updated1 || updated2 {
		logInfo("restart discover subscriber")
		discover.Restart()
	}
	updateLock.Unlock()
	_proxies = proxies
	_regexProxies = regexProxies
}

func syncRewrites() {
	regexRewrites := map[string]*regexRewriteInfo{}
	updateRewrites(&regexRewrites, gatewayConfig.Rewrites)

	updateLock.Lock()
	updateRewrites(&regexRewrites, redisPool.Do("HGETALL", rewritesKey).StringMap())
	updateLock.Unlock()

	_regexRewrites = regexRewrites
}

func subscribe() {
	for {
		syncConn := &redigo.PubSubConn{Conn: pubsubRedisPool.GetConnection()}
		err := syncConn.Subscribe(proxiesChannel, rewritesChannel)
		if err != nil {
			logError(err.Error())
			_ = syncConn.Close()
			syncConn = nil

			time.Sleep(time.Second * 1)
			if !s.IsRunning() {
				break
			}
			continue
		}

		syncProxies()
		syncRewrites()
		if !s.IsRunning() {
			break
		}

		// 开始接收订阅数据
		for {
			isErr := false
			receiveObj := syncConn.Receive()
			switch v := receiveObj.(type) {
			case redigo.Message:
				logInfo("received subscribe", "message", v)
				if v.Channel == proxiesChannel {
					syncProxies()
				} else if v.Channel == rewritesChannel {
					syncRewrites()
				}
			case redigo.Subscription:
			case redigo.Pong:
			case error:
				if !strings.Contains(v.Error(), "connection closed") {
					logError(v.Error())
				}
				isErr = true
				break
			}
			if isErr {
				break
			}
			if !s.IsRunning() {
				break
			}
		}
		if !s.IsRunning() {
			break
		}
		time.Sleep(time.Second * 1)
		if !s.IsRunning() {
			break
		}
	}
}

func updateProxies(proxies *map[string]string, regexProxies *map[string]*regexProxiesInfo, in map[string]string) bool {
	//logInfo("update", "data", in)
	updated := false
	for k, v := range in {
		if v == _proxies[k] {
			(*proxies)[k] = v
			continue
		}

		if _regexProxies[k] != nil && v == _regexProxies[k].Value {
			(*regexProxies)[k] = _regexProxies[k]
			continue
		}

		if strings.Contains(v, "(") {
			matcher, err := regexp.Compile("^" + v + "$")
			if err != nil {
				logError("proxy regexp compile failed", "key", k, "value", v)
				//log.Print("Proxy Error	Compile	", err)
			} else {
				logInfo(u.StringIf(_regexProxies[k] != nil, "update regexp proxy set", "new regexp proxy set"), "key", k, "value", v)
				(*regexProxies)[k] = &regexProxiesInfo{
					Value: v,
					Regex: *matcher,
				}
			}
		} else {
			logInfo(u.StringIf(_proxies[k] != "", "update proxy set", "new proxy set"), "key", k, "value", v)
			(*proxies)[k] = v
			calls, ok := discover.Config.Calls[v]
			if !ok && discover.AddExternalApp(v, calls) {
				updated = true
			}
		}
	}
	return updated
}

func updateRewrites(regexRewrites *map[string]*regexRewriteInfo, in map[string]string) {
	for k, v := range in {

		if _regexRewrites[k] != nil && v == _regexRewrites[k].To {
			(*regexRewrites)[k] = _regexRewrites[k]
			continue
		}

		matcher, err := regexp.Compile("^" + k + "$")
		if err != nil {
			logError("rewrite regexp compile failed", "key", k, "value", v)
		} else {
			logInfo(u.StringIf(_regexRewrites[k] != nil, "update regexp rewrite set", "new regexp rewrite set"), "key", k, "value", v)
			(*regexRewrites)[k] = &regexRewriteInfo{
				To:    v,
				Regex: *matcher,
			}
		}
	}
}
