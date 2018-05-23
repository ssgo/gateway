package main

import (
	"github.com/ssgo/redis"
	"github.com/ssgo/s"
	"net/http"
	"strings"
	"github.com/ssgo/base"
	"regexp"
	"log"
	"time"
)

var dcCache *redis.Redis
var proxies = map[string]string{}
var regexProxiesSet = map[string]string{}
var regexProxies = make([]*regexp.Regexp, 0)
var config = struct {
	CheckInterval int
	Proxies       map[string]*string
}{}
//var proxiesVersion int

func main() {
	s.Init()
	conf := s.GetConfig()
	dcCache = redis.GetRedis(conf.RegistryCalls)
	base.LoadConfig("proxy", &config)
	if config.CheckInterval == 0 {
		config.CheckInterval = 10
	} else if config.CheckInterval < 3 {
		config.CheckInterval = 3
	}

	configProxies := map[string]string{}
	for k, v := range config.Proxies {
		configProxies[k] = *v
	}
	updateCalls(configProxies)

	//proxiesVersion = dcCache.GET("proxiesVersion").Int()
	updateCalls(dcCache.Do("HGETALL", "_proxies").StringMap())

	s.SetProxyBy(proxy)
	go syncCalls()
	s.Start1()
}

func proxy(request *http.Request) (toApp, toPath *string, headers *map[string]string) {
	// 匹配二级目录
	paths := strings.SplitN(request.RequestURI, "/", 4)
	if len(paths) == 4 {
		p1 := "/" + paths[1] + "/" + paths[2]
		p2 := "/" + paths[3]

		// Host + Path 匹配
		a := proxies[request.Host+p1]
		if a != "" {
			return &a, &p2, &map[string]string{"Proxy-Path": p1}
		}

		// Path 匹配
		a = proxies[p1]
		if a != "" {
			return &a, &p2, &map[string]string{"Proxy-Path": p1}
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
			return &a, &p2, &map[string]string{"Proxy-Path": p1}
		}

		// Path 匹配
		a = proxies[p1]
		if a != "" {
			return &a, &p2, &map[string]string{"Proxy-Path": p1}
		}
	}

	// 匹配 Host
	a := proxies[request.Host]
	if a != "" {
		return &a, &request.RequestURI, nil
	}

	// 模糊匹配
	if len(regexProxies) > 0 {
		requestUrl := request.Host + request.RequestURI
		for _, m := range regexProxies {
			finds := m.FindAllStringSubmatch(requestUrl, 20)
			if len(finds) > 0 && len(finds[0]) > 2 {
				var hh *map[string]string
				pos := strings.Index(request.RequestURI, finds[0][2])
				if pos > 0 {
					hh = &map[string]string{"Proxy-Path": request.RequestURI[0:pos]}
				}
				return &finds[0][1], &finds[0][2], hh
			}
		}
	}

	// 不进行代理
	return
}

func syncCalls() {
	for {
		for i := 0; i < config.CheckInterval; i++ {
			time.Sleep(time.Second * 1)
			if !s.IsRunning() {
				break
			}
		}
		//pv := dcCache.GET("proxiesVersion").Int()
		//if pv > proxiesVersion {
		//	proxiesVersion = pv
		if updateCalls(dcCache.Do("HGETALL", "_proxies").StringMap()) {
			log.Printf("Proxy restart discover")
			s.RestartDiscoverSyncer()
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
		log.Printf("Proxy Register	%s	%s", k, v)

		if strings.Contains(v, "(") {
			matcher, err := regexp.Compile("^" + v + "$")
			if err != nil {
				log.Print("Proxy Error	Compile	", err)
			} else {
				regexProxies = append(regexProxies, matcher)
				regexProxiesSet[k] = v
				continue
			}
		}
		proxies[k] = v

		if s.AddExternalApp(v, s.Call{}) {
			updated = true
		}
	}
	return updated
}
