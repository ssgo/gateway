package main

import (
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
	"time"
)

var dcCache *redis.Redis
var proxies = map[string]string{}
var regexProxiesSet = map[string]string{}
var regexProxies = make([]*regexp.Regexp, 0)
var gatewayConfig = struct {
	CheckInterval int
	Proxies       map[string]*string
}{}

var logger = log.New(u.ShortUniqueId())

func logInfo(info string, extra ...interface{}) {
	logger.Info("Gateway: "+info, extra...)
}

func logError(error string, extra ...interface{}) {
	logger.Error("Gateway: "+error, extra...)
}

func main() {
	discover.Init()
	s.Init()
	dcCache = redis.GetRedis(discover.Config.RegistryCalls, logger)
	config.LoadConfig("proxy", &gatewayConfig)
	if gatewayConfig.CheckInterval == 0 {
		gatewayConfig.CheckInterval = 10
	} else if gatewayConfig.CheckInterval < 3 {
		gatewayConfig.CheckInterval = 3
	}

	//configProxies := map[string]string{}
	//for k, v := range gatewayConfig.Proxies {
	//	configProxies[k] = *v
	//}
	//updateCalls(configProxies)
	//
	////proxiesVersion = dcCache.GET("proxiesVersion").Int()
	//updateCalls(dcCache.Do("HGETALL", "_proxies").StringMap())
	//
	//s.SetProxyBy(proxy)
	//go syncCalls()
	//s.Start1()

	s.SetProxyBy(proxy)
	as := s.AsyncStart()

	configProxies := map[string]string{}
	for k, v := range gatewayConfig.Proxies {
		configProxies[k] = *v
	}
	updateCalls(configProxies)

	//proxiesVersion = dcCache.GET("proxiesVersion").Int()
	updateCalls(dcCache.Do("HGETALL", "_proxies").StringMap())

	syncCalls()
	as.Stop()
}

func proxy(request *http.Request) (toApp, toPath *string, headers *map[string]string) {
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
		a := proxies[request.Host+p1]
		if a != "" {
			outHeaders["Proxy-Path"] = p1
			return &a, &p2, &outHeaders
		}

		// Path 匹配
		a = proxies[p1]
		if a != "" {
			outHeaders["Proxy-Path"] = p1
			return &a, &p2, &outHeaders
		}
	}

	// 匹配一级目录
	paths = strings.SplitN(request.RequestURI, "/", 3)
	if len(paths) == 3 {
		p1 := "/" + paths[1]
		p2 := "/" + paths[2]

		// Host + Path 匹配
		a := proxies[request.Host+p1]
		if a != "" {
			outHeaders["Proxy-Path"] = p1
			return &a, &p2, &outHeaders
		}

		// Path 匹配
		a = proxies[p1]
		if a != "" {
			outHeaders["Proxy-Path"] = p1
			return &a, &p2, &outHeaders
		}
	}

	// 匹配 Host
	a := proxies[request.Host]
	if a != "" {
		return &a, &request.RequestURI, &outHeaders
	}

	// 模糊匹配
	if len(regexProxies) > 0 {
		requestUrl := request.Host + request.RequestURI
		for _, m := range regexProxies {
			finds := m.FindAllStringSubmatch(requestUrl, 20)
			if len(finds) > 0 && len(finds[0]) > 2 {
				pos := strings.Index(request.RequestURI, finds[0][2])
				if pos > 0 {
					outHeaders["Proxy-Path"] = request.RequestURI[0:pos]
				}
				return &finds[0][1], &finds[0][2], &outHeaders
			}
		}
	}

	// 不进行代理
	return
}

func syncCalls() {
	for {
		for i := 0; i < gatewayConfig.CheckInterval; i++ {
			time.Sleep(time.Second * 1)
			if !s.IsRunning() {
				break
			}
		}
		//pv := dcCache.GET("proxiesVersion").Int()
		//if pv > proxiesVersion {
		//	proxiesVersion = pv
		if updateCalls(dcCache.Do("HGETALL", "_proxies").StringMap()) {
			logInfo("restart discover")
			//log.Printf("Proxy restart discover")
			discover.Restart()
			//s.RestartDiscoverSyncer()
		}
		//}
		if !s.IsRunning() {
			break
		}
	}
}

func updateCalls(in map[string]string) bool {
	updated := false
	for k, v := range in {
		if v == proxies[k] || v == regexProxiesSet[k] {
			continue
		}
		//log.Printf("Proxy Register	%s	%s", k, v)

		if strings.Contains(v, "(") {
			matcher, err := regexp.Compile("^" + v + "$")
			if err != nil {
				logError("regexp compile failed", "key", k, "value", v)
				//log.Print("Proxy Error	Compile	", err)
			} else {
				logInfo(u.StringIf(regexProxiesSet[k] != "", "update regexp proxy set", "new regexp proxy set"), "key", k, "value", v)
				regexProxies = append(regexProxies, matcher)
				regexProxiesSet[k] = v
			}
		} else {
			logInfo(u.StringIf(proxies[k] != "", "update proxy set", "new proxy set"), "key", k, "value", v)
			proxies[k] = v
			//if s.AddExternalApp(v, s.Call{}) {
			if discover.AddExternalApp(v, discover.CallInfo{}) {
				updated = true
			}
		}
	}
	return updated
}
