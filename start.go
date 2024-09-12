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
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
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
var _regexProxies = map[string]*regexProxiesInfo{}
var _regexRewrites = map[string]*regexRewriteInfo{}
var _rewritesLock = sync.RWMutex{}
var _statics = map[string]string{}
var _staticsLock = sync.RWMutex{}
var gatewayConfig = struct {
	CheckInterval int
	Proxy         map[string]string
	Rewrite       map[string]string
	Prefix        string
	CallTimeout   config.Duration
	Static        map[string]string
}{}

var logger = log.New(u.ShortUniqueId())
var proxiesKey = "_proxy"
var proxiesChannel = "_CH_proxy"
var rewritesKey = "_rewrite"
var rewritesChannel = "_CH_rewrite"
var staticsKey = "_static"
var staticsChannel = "_CH_static"

func logInfo(info string, extra ...interface{}) {
	logger.Info("Gateway: "+info, extra...)
}

func logError(error string, extra ...interface{}) {
	logger.Error("Gateway: "+error, extra...)
}

func main() {
	Start()
}

func Start() {
	if as := AsyncStart(); as != nil {
		as.Wait()
	}
}

func AsyncStart() *s.AsyncServer {
	errs := config.LoadConfig("gateway", &gatewayConfig)
	if errs != nil && len(errs) > 0 {
		for _, err := range errs {
			fmt.Println(u.BRed(err.Error()))
		}
		return nil
	}

	if len(os.Args) > 1 {
		if os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
			fmt.Println(`gateway

start		start server
stop		stop server
restart		restart server
reload | -r	reload config
test | -t	test config
help | -h	show this help

no arg will run in foreground
`)
			return nil
		}

		if os.Args[1] == "test" || os.Args[1] == "-t" {
			fmt.Println(u.Green(u.FixedJsonP(gatewayConfig)))
			fmt.Println(u.BGreen("the configuration was successful"))
			return nil
		}

		if os.Args[1] == "reload" || os.Args[1] == "-r" {
			shellFile, _ := filepath.Abs(os.Args[0])
			pidFile := filepath.Join(filepath.Dir(shellFile), ".pid")
			if !u.FileExists(pidFile) && u.FileExists(".pid") {
				pidFile = ".pid"
			}
			a := strings.SplitN(u.ReadFileN(pidFile), ",", 2)
			if a[0] == "" {
				fmt.Println(u.BRed(".pid file not exists"))
				return nil
			}
			err := syscall.Kill(u.Int(a[0]), syscall.SIGUSR1)
			if err != nil {
				fmt.Println(u.BRed(err.Error()))
			} else {
				fmt.Println(u.BGreen("reload signal was sent to " + a[0] + ", please see result in log"))
			}
			return nil
		}
	}

	reloadChan := make(chan os.Signal, 1)
	signal.Notify(reloadChan, syscall.SIGUSR1)
	go func() {
		for {
			sig := <-reloadChan
			if sig != syscall.SIGUSR1 {
				break
			}

			logInfo("reloading config...")
			config.ResetConfigEnv()
			errs := config.LoadConfig("gateway", &gatewayConfig)
			if errs != nil && len(errs) > 0 {
				for _, err := range errs {
					fmt.Println(u.BRed(err.Error()))
				}
			} else {
				updateStatic(gatewayConfig.Static)
				updateRewrite(gatewayConfig.Rewrite)
				updateProxy(gatewayConfig.Proxy)
			}
			logInfo("config reloaded")
		}
	}()

	discover.Init()
	s.Init()

	if discover.Config.Registry != "" {
		tmpRedisPool := redis.GetRedis(discover.Config.Registry, logger)
		if conn := tmpRedisPool.GetConnection(); conn == nil || conn.Err() != nil {
			logger.Warning("redis connection failed, start local mode")
		} else {
			_ = conn.Close()
			redisPool = tmpRedisPool
		}
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
		staticsKey = fmt.Sprint("_", gatewayConfig.Prefix, "statics")
		staticsChannel = fmt.Sprint("_CH_", gatewayConfig.Prefix, "statics")
	}
	s.SetRewriteBy(rewrite)
	s.SetProxyBy(proxy)

	//for host, wwwSet := range gatewayConfig.Static {
	//	if host == "*" {
	//		host = ""
	//	}
	//	vh := s.Host(host)
	//	for webPath, localPath := range *wwwSet {
	//		logger.Info("www site:", "host", host, "webPath", webPath, "localPath", localPath)
	//		vh.Static(webPath, *localPath)
	//	}
	//}
	updateStatic(gatewayConfig.Static)
	updateRewrite(gatewayConfig.Rewrite)
	updateProxy(gatewayConfig.Proxy)

	as := s.AsyncStart()
	as.OnStop(func() {
		reloadChan <- syscall.SIGTERM
	})
	if redisPool != nil {
		go subscribe()
		go func() {
			for {
				for i := 0; i < gatewayConfig.CheckInterval; i++ {
					time.Sleep(time.Second * 1)
					if !s.IsRunning() {
						break
					}
				}
				if !s.IsRunning() {
					break
				}
				updateProxy(redisPool.Do("HGETALL", proxiesKey).StringMap())
				if !s.IsRunning() {
					break
				}
				updateRewrite(redisPool.Do("HGETALL", rewritesKey).StringMap())
				if !s.IsRunning() {
					break
				}
				updateStatic(redisPool.Do("HGETALL", staticsKey).StringMap())
				if !s.IsRunning() {
					break
				}
			}
		}()
	}
	return as
}

func rewrite(request *s.Request) (toPath string, rewrite bool) {
	list2 := map[string]*regexRewriteInfo{}
	_rewritesLock.RLock()
	for k, v := range _regexRewrites {
		list2[k] = v
	}
	_rewritesLock.RUnlock()

	if len(list2) > 0 {
		requestUrl := fmt.Sprint(request.Header.Get("X-Scheme"), "://", request.Host, request.RequestURI)
		requestUrlWithoutScheme := fmt.Sprint(request.Host, request.RequestURI)

		for _, rr := range list2 {
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

	list := map[string]string{}
	_proxiesLock.RLock()
	for k, v := range _proxies {
		list[k] = v
	}
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
	list2 := map[string]*regexProxiesInfo{}
	_proxiesLock.RLock()
	for k, v := range _regexProxies {
		list2[k] = v
	}
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
						discover.AddExternalApp(finds[0][1], callConfig)
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

		updateProxy(redisPool.Do("HGETALL", proxiesKey).StringMap())
		updateRewrite(redisPool.Do("HGETALL", rewritesKey).StringMap())
		updateStatic(redisPool.Do("HGETALL", staticsKey).StringMap())
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
					updateProxy(redisPool.Do("HGETALL", proxiesKey).StringMap())
				} else if v.Channel == rewritesChannel {
					updateRewrite(redisPool.Do("HGETALL", rewritesKey).StringMap())
				} else if v.Channel == staticsChannel {
					updateRewrite(redisPool.Do("HGETALL", staticsKey).StringMap())
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

func updateStatic(in map[string]string) bool {
	updated := false
	for k, v := range in {
		_staticsLock.RLock()
		v1 := _statics[k]
		_staticsLock.RUnlock()
		if v == v1 {
			continue
		}

		logInfo(u.StringIf(v1 != "", "update static set", "new static set"), "key", k, "value", v)
		_staticsLock.Lock()
		_statics[k] = v
		_staticsLock.Unlock()
		a := strings.SplitN(k, "/", 2)
		if len(a) == 1 {
			a = append(a, "/")
		}
		if a[0] == "*" {
			a[0] = ""
		}
		s.StaticByHost(a[1], v, a[0])
		updated = true
	}
	return updated
}

func updateProxy(in map[string]string) bool {
	updated := false
	//fmt.Println("####000")

	for k, v := range in {
		//fmt.Println("####111", k, v)
		_proxiesLock.RLock()
		v1 := _proxies[k]
		v2 := _regexProxies[k]
		_proxiesLock.RUnlock()
		// skip same
		if v == v1 {
			//fmt.Println("####222", k, v)
			continue
		}
		////fmt.Println("####333", k, v)
		if v2 != nil && v == v2.Value {
			continue
		}
		////fmt.Println("####444", k, v)

		if strings.Contains(v, "(") {
			// for regexp
			////fmt.Println("####555", k, v)
			matcher, err := regexp.Compile("^" + v + "$")
			if err != nil {
				logError("proxy regexp compile failed", "key", k, "value", v)
				//log.Print("Proxy Error	Compile	", err)
			} else {
				logInfo(u.StringIf(v2 != nil, "update regexp proxy set", "new regexp proxy set"), "key", k, "value", v)
				_proxiesLock.Lock()
				_regexProxies[k] = &regexProxiesInfo{
					Value: v,
					Regex: *matcher,
				}
				_proxiesLock.Unlock()
				updated = true
			}
		} else {
			// for simple
			////fmt.Println("####666", k, v)
			logInfo(u.StringIf(v1 != "", "update proxy set", "new proxy set"), "key", k, "value", v)
			_proxiesLock.Lock()
			_proxies[k] = v
			_proxiesLock.Unlock()

			// add app to discover
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
				} else {
					updated = true
				}
			} else {
				updated = true
			}
		}
	}
	//fmt.Println("####999")
	return updated
}

func updateRewrite(in map[string]string) bool {
	updated := false
	for k, v := range in {
		_rewritesLock.RLock()
		v2 := _regexRewrites[k]
		_rewritesLock.RUnlock()

		// skip same
		if v2 != nil && v == v2.To {
			continue
		}

		matcher, err := regexp.Compile("^" + k + "$")
		if err != nil {
			logError("rewrite regexp compile failed", "key", k, "value", v)
		} else {
			logInfo(u.StringIf(v2 != nil, "update regexp rewrite set", "new regexp rewrite set"), "key", k, "value", v)
			_rewritesLock.Lock()
			_regexRewrites[k] = &regexRewriteInfo{
				To:    v,
				Regex: *matcher,
			}
			_rewritesLock.Unlock()
			updated = true
		}
	}
	return updated
}
