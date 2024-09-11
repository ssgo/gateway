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
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var redisPool *redis.Redis
var pubsubRedisPool *redis.Redis

//var updateLock = sync.Mutex{}

type regexProxiesInfo struct {
	Value string
	Regex regexp.Regexp
}

type regexRewriteInfo struct {
	To    string
	Regex regexp.Regexp
}

var _proxies = map[string]string{}
var _proxiesLock = sync.RWMutex{}
var _rewritesLock = sync.RWMutex{}
var _regexProxies = map[string]*regexProxiesInfo{}
var _regexRewrites = map[string]*regexRewriteInfo{}
var gatewayConfig = struct {
	CheckInterval int
	Proxies       map[string]string
	Rewrites      map[string]string
	Prefix        string
	CallTimeout   config.Duration
	Www           map[string]*map[string]*string
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
	errs := config.LoadConfig("gateway", &gatewayConfig)
	if errs != nil && len(errs) > 0 {
		for _, err := range errs {
			fmt.Println(u.BRed(err.Error()))
		}
		return
	}

	if len(os.Args) > 1 && (os.Args[1] == "test" || os.Args[1] == "-t") {
		fmt.Println(u.Green(u.FixedJsonP(gatewayConfig)))
		fmt.Println(u.BGreen("the configuration was successful"))
		return
	}

	discover.Init()
	s.Init()
	redisPool = redis.GetRedis(discover.Config.Registry, logger)
	if conn := redisPool.GetConnection(); conn == nil || conn.Err() != nil {
		logger.Warning("redis connection failed, start local mode")
		redisPool = nil
	} else {
		_ = conn.Close()
	}

	if redisPool != nil {
		confForPubSub := *redisPool.Config
		confForPubSub.IdleTimeout = -1
		confForPubSub.ReadTimeout = -1
		pubsubRedisPool = redis.NewRedis(&confForPubSub, logger)
	}

	if gatewayConfig.CheckInterval == 0 {
		gatewayConfig.CheckInterval = 10
	} else if gatewayConfig.CheckInterval < 3 {
		gatewayConfig.CheckInterval = 3
	}

	if gatewayConfig.CallTimeout == 0 {
		gatewayConfig.CallTimeout = config.Duration(10 * time.Second)
	}

	if gatewayConfig.Prefix != "" {
		proxiesKey = fmt.Sprint("_", gatewayConfig.Prefix, "proxies")
		proxiesChannel = fmt.Sprint("_CH_", gatewayConfig.Prefix, "proxies")
		rewritesKey = fmt.Sprint("_", gatewayConfig.Prefix, "rewrites")
		rewritesChannel = fmt.Sprint("_CH_", gatewayConfig.Prefix, "rewrites")
	}
	s.SetRewriteBy(rewrite)
	s.SetProxyBy(proxy)

	for host, wwwSet := range gatewayConfig.Www {
		if host == "*" {
			host = ""
		}
		vh := s.Host(host)
		for webPath, localPath := range *wwwSet {
			logger.Info("www site:", "host", host, "webPath", webPath, "localPath", localPath)
			vh.Static(webPath, *localPath)
		}
	}

	as := s.AsyncStart()
	syncProxies()
	syncRewrites()

	if redisPool != nil {
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
	} else {
		as.Wait()
	}
}

func rewrite(request *s.Request) (toPath string, rewrite bool) {
	_rewritesLock.RLock()
	list := _regexRewrites
	_rewritesLock.RUnlock()
	if len(list) > 0 {

		requestUrl := fmt.Sprint(request.Header.Get("X-Scheme"), "://", request.Host, request.RequestURI)
		requestUrlWithoutScheme := fmt.Sprint(request.Host, request.RequestURI)

		for _, rr := range list {
			finds := rr.Regex.FindAllStringSubmatch(requestUrl, 20)
			if len(finds) == 0 {
				finds = rr.Regex.FindAllStringSubmatch(requestUrlWithoutScheme, 20)
			}
			if len(finds) == 0 {
				continue
			}

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

	// 不进行代理
	return "", false
}

func proxy(request *s.Request) (authLevel int, toApp, toPath *string, headers map[string]string) {
	//fmt.Println("proxy", len(_proxies))
	outHeaders := map[string]string{
		standard.DiscoverHeaderFromApp:  "gateway",
		standard.DiscoverHeaderFromNode: s.GetServerAddr(),
	}

	scheme := u.StringIf(request.TLS == nil, "http", "https")
	host1 := ""
	host2 := ""
	if strings.ContainsRune(request.Host, ':') {
		hostArr := strings.SplitN(request.Host, ":", 2)
		host1 = hostArr[0]
		host2 = request.Host
	} else {
		host1 = request.Host
		host2 = request.Host + ":" + u.StringIf(request.TLS == nil, "80", "443")
	}

	pathMatchers := make([]string, 0)
	pathMatchers = append(pathMatchers, fmt.Sprint(scheme, "://", host1, request.RequestURI))
	pathMatchers = append(pathMatchers, fmt.Sprint(scheme, "://", host2, request.RequestURI))
	pathMatchers = append(pathMatchers, fmt.Sprint(host1, request.RequestURI))
	pathMatchers = append(pathMatchers, fmt.Sprint(host2, request.RequestURI))
	pathMatchers = append(pathMatchers, request.RequestURI)

	hostMatchers := make([]string, 0)
	hostMatchers = append(hostMatchers, fmt.Sprint(scheme, "://", host1))
	hostMatchers = append(hostMatchers, fmt.Sprint(scheme, "://", host2))
	hostMatchers = append(hostMatchers, host1)
	hostMatchers = append(hostMatchers, host2)

	_proxiesLock.RLock()
	list := _proxies
	_proxiesLock.RUnlock()
	for p, a := range list {
		//fmt.Println("check proxy ", p, a)
		matchPath := ""
		matchPathArr := strings.SplitN(strings.ReplaceAll(p, "://", ""), "/", 2)
		if len(matchPathArr) == 2 {
			matchPath = "/" + matchPathArr[1]
		}

		if matchPath == "" {
			for _, m := range hostMatchers {
				if m == p {
					//fmt.Println(" >>>>>>>>1", p, m, request.RequestURI)
					return 0, fixAppName(a), &request.RequestURI, outHeaders
				}
			}
		} else {
			for _, m := range pathMatchers {
				if strings.HasPrefix(m, p) {
					if strings.HasPrefix(request.RequestURI, matchPath) {
						p2 := request.RequestURI[len(matchPath):]
						if len(p2) == 0 || p2[0] != '/' {
							p2 = "/" + p2
						}
						//fmt.Println(" >>>>>>>>2", p, m, p2)
						return 0, fixAppName(a), &p2, outHeaders
					} else {
						//fmt.Println(" >>>>>>>>3", p, m, request.RequestURI)
						return 0, fixAppName(a), &request.RequestURI, outHeaders
					}
				}
			}
		}
	}

	//// 匹配二级目录
	//paths := strings.SplitN(request.RequestURI, "/", 4)
	//if len(paths) == 4 {
	//	p1 := "/" + paths[1] + "/" + paths[2]
	//	p2 := "/" + paths[3]
	//
	//	// Host + Path 匹配
	//	a := _proxies[request.Host+p1]
	//	if a != "" {
	//		outHeaders["Proxy-Path"] = p1
	//		return fixAppName(a), &p2, outHeaders
	//	}
	//
	//	// Path 匹配
	//	a = _proxies[p1]
	//	if a != "" {
	//		outHeaders["Proxy-Path"] = p1
	//		return fixAppName(a), &p2, outHeaders
	//	}
	//}
	//
	//// 匹配一级目录
	//paths = strings.SplitN(request.RequestURI, "/", 3)
	//if len(paths) == 3 {
	//	p1 := "/" + paths[1]
	//	p2 := "/" + paths[2]
	//
	//	// Host + Path 匹配
	//	a := _proxies[request.Host+p1]
	//	if a != "" {
	//		outHeaders["Proxy-Path"] = p1
	//		return fixAppName(a), &p2, outHeaders
	//	}
	//
	//	// Path 匹配
	//	a = _proxies[p1]
	//	if a != "" {
	//		outHeaders["Proxy-Path"] = p1
	//		return fixAppName(a), &p2, outHeaders
	//	}
	//}

	//// 匹配 Host
	//a := _proxies[request.Host]
	//if a != "" {
	//	return fixAppName(a), &request.RequestURI, outHeaders
	//}

	// 模糊匹配
	_proxiesLock.RLock()
	list2 := _regexProxies
	_proxiesLock.RUnlock()

	if len(list2) > 0 {
		requestUrl := request.Host + request.RequestURI
		for _, rp := range list2 {
			//fmt.Println("check regexp proxy ", rp.Regex, rp.Value)
			finds := rp.Regex.FindAllStringSubmatch(requestUrl, 20)
			if len(finds) > 0 && len(finds[0]) > 2 {
				//fmt.Println(" >>>>>>>>2", request.RequestURI, finds[0][2])
				pos := strings.Index(request.RequestURI, finds[0][2])
				if pos > 0 {
					outHeaders["Proxy-Path"] = request.RequestURI[0:pos]
				}

				if !strings.Contains(finds[0][1], "://") && strings.ContainsRune(finds[0][1], ':') {
					callConfig := ""
					if strings.ContainsRune(finds[0][1], ':') {
						// support call config in proxy value
						a := strings.SplitN(finds[0][1], ":", 2)
						finds[0][1] = a[0]
						callConfig = a[1]
					} else {
						callConfig = u.String(gatewayConfig.CallTimeout.TimeDuration())
					}
					if redisPool != nil {
						if discover.AddExternalApp(finds[0][1], callConfig) {
							discover.Restart()
						}
					}
				}
				return 0, &finds[0][1], &finds[0][2], outHeaders
			}
		}
	}

	// 不进行代理
	return
}

func fixAppName(appName string) *string {
	if !strings.Contains(appName, "://") && strings.ContainsRune(appName, ':') {
		a := strings.SplitN(appName, ":", 2)
		return &a[0]
	} else {
		return &appName
	}
}

func syncProxies() {
	proxies := map[string]string{}
	regexProxies := map[string]*regexProxiesInfo{}
	////fmt.Println("####000", u.JsonP(gatewayConfig.Proxies), 111)
	updated1 := updateProxies(&proxies, &regexProxies, gatewayConfig.Proxies)
	//fmt.Println("####>>>", u.JsonP(proxies), 111)

	updated2 := false
	if redisPool != nil {
		//updateLock.Lock()
		updated2 = updateProxies(&proxies, &regexProxies, redisPool.Do("HGETALL", proxiesKey).StringMap())
		//if updated1 || updated2 {
		//logInfo("restart discover subscriber")
		////fmt.Println("####>>>****1")
		//discover.Restart()
		////fmt.Println("####>>>****2")
		//}
		//updateLock.Unlock()
	}
	//fmt.Println("####>>>", u.JsonP(proxies), 222)

	if updated1 || updated2 {
		_proxiesLock.Lock()
		_proxies = proxies
		_regexProxies = regexProxies
		_proxiesLock.Unlock()
		logInfo("proxies updated", "count", len(proxies), "regexpCount", len(regexProxies))
	}
}

func syncRewrites() {
	regexRewrites := map[string]*regexRewriteInfo{}
	updated1 := updateRewrites(&regexRewrites, gatewayConfig.Rewrites)
	updated2 := false

	if redisPool != nil {
		//updateLock.Lock()
		updated2 = updateRewrites(&regexRewrites, redisPool.Do("HGETALL", rewritesKey).StringMap())
		//updateLock.Unlock()
	}

	if updated1 || updated2 {
		_rewritesLock.Lock()
		_regexRewrites = regexRewrites
		_rewritesLock.Unlock()
		logInfo("rewrites updated", "count", len(regexRewrites))
	}
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
	updated := false
	//fmt.Println("####000")
	_proxiesLock.RLock()
	list := _proxies
	list2 := _regexProxies
	_proxiesLock.RUnlock()

	for k, v := range in {
		//fmt.Println("####111", k, v)
		v2 := list[k]
		v3 := list2[k]
		if v == v2 {
			(*proxies)[k] = v
			//fmt.Println("####222", k, v)
			continue
		}
		////fmt.Println("####333", k, v)
		if v3 != nil && v == v3.Value {
			(*regexProxies)[k] = v3
			continue
		}
		////fmt.Println("####444", k, v)
		if strings.Contains(v, "(") {
			////fmt.Println("####555", k, v)
			matcher, err := regexp.Compile("^" + v + "$")
			if err != nil {
				logError("proxy regexp compile failed", "key", k, "value", v)
				//log.Print("Proxy Error	Compile	", err)
			} else {
				logInfo(u.StringIf(v3 != nil, "update regexp proxy set", "new regexp proxy set"), "key", k, "value", v)
				(*regexProxies)[k] = &regexProxiesInfo{
					Value: v,
					Regex: *matcher,
				}
			}
		} else {
			////fmt.Println("####666", k, v)
			logInfo(u.StringIf(v2 != "", "update proxy set", "new proxy set"), "key", k, "value", v)
			(*proxies)[k] = v
			////fmt.Println("########2", len((*proxies)))
			if !strings.Contains(v, "://") {
				if discover.Config.Calls[v] == "" {
					callConfig := ""
					if strings.ContainsRune(v, ':') {
						// support call config in proxy value
						a := strings.SplitN(v, ":", 2)
						v = a[0]
						callConfig = a[1]
					} else {
						callConfig = u.String(gatewayConfig.CallTimeout.TimeDuration())
					}
					if redisPool != nil {
						if discover.AddExternalApp(v, callConfig) {
							updated = true
						}
					}
				}
			}
		}
	}
	//fmt.Println("####999")
	return updated
}

func updateRewrites(regexRewrites *map[string]*regexRewriteInfo, in map[string]string) bool {
	_proxiesLock.RLock()
	list := _regexRewrites
	_proxiesLock.RUnlock()

	updated := false
	for k, v := range in {
		v3 := list[k]
		if v3 != nil && v == v3.To {
			(*regexRewrites)[k] = v3
			continue
		}

		matcher, err := regexp.Compile("^" + k + "$")
		if err != nil {
			logError("rewrite regexp compile failed", "key", k, "value", v)
		} else {
			logInfo(u.StringIf(v3 != nil, "update regexp rewrite set", "new regexp rewrite set"), "key", k, "value", v)
			(*regexRewrites)[k] = &regexRewriteInfo{
				To:    v,
				Regex: *matcher,
			}
			updated = true
		}
	}
	return updated
}
