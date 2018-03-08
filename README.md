# 基于 ssgo/s 的一个网关应用

为了更好的支撑所有后端应用服务化，该网关可以接管所有流量，根据注册信息向后端节点转发

可以直接使用，也可以参照该项目定制一个需要的网关，后面所有节点都建议使用 h2c 协议，特殊情况可以在 service_calls 中指定 httpVersion 为 1

# 存储依赖

应用会依赖 service.json 中 registryCalls 指定的 redis 配置访问注册信息，默认值为 "discover:15"

同时会访问该 redis db 中的 proxies 的内容，进行动态配置，如果修改 proxies 的内容需要进行 INCR proxiesVersion 操作提升版本

## 配置

可在项目根目录放置一个 proxy.json

```json
{
  "checkInterval": 5,
  "proxies": {
    "localhost:8080": "mainapp",
    "127.0.0.1/status": "status",
    "/hello": "welcome",
    "forDev": ".*?/(.*?)(/.*)"
  }
}
```

checkInterval 同步 redis db 中 proxies 动态配置的间隔时间，单位 秒

proxies 中 支持
 - Host => App
 - Path => App
 - Host&Path => App
 - Host&Path 进行正则匹配后 $1 是 App $2 是 requestPath，key 没有实际意义

配置内容也可以同时使用 env.json 或环境变量设置（优先级高于配置文件）
