# 基于 github/ssgo 的一个网关应用（类似nginx）

### 安装

```shell
go install github.com/ssgo/gateway@latest
```

或直接下载对应操作系统的二进制程序：

Mac:

https://apigo.cloud/gateway/gateway.darwin.amd64

https://apigo.cloud/gateway/gateway.darwin.arm64


Linux:

https://apigo.cloud/gateway/gateway.linux.amd64

https://apigo.cloud/gateway/gateway.linux.arm64


Windows:

https://apigo.cloud/gateway/gateway.windows.amd64.exe

https://apigo.cloud/gateway/gateway.windows.arm64.exe


### 监听（使用 https://github/ssgo/s 的配置）

以下是一个示例（配置文件env.yml）：

```yaml
service:
  listen: 80|443
  rewriteTimeout: 60000
  ssl:
    xxx.com:
      certfile: xxx.pem
      keyfile: xxx.key
```


### 日志（使用 https://github/ssgo/log 的配置）

以下是一个示例（配置文件env.yml）：

```yaml
log:
  file: out.log
  splittag: 20060102
```


### 静态文件配置

以下是一个示例（配置文件env.yml）：

```yaml
gateway:
  www:
    "*":
      /: www
    xxx.com:
      /: www/xxx
```


### 反向代理配置

以下是一个示例（配置文件env.yml）：

```yaml
gateway:
  proxies:
    xxx.com: http://127.0.0.1:3000
    xxx.com/(.*): https://$1
    xxx.com/xxx: discoverAppName
    xxx2.com/xxx/(.*): http://127.0.0.1:8001/xxx/$1
    xxx3.com:8080: discoverAppName
```

支持的反向代理格式：

Host: URL or DiscoverApp

Host:Port: URL or DiscoverApp

Path: URL or DiscoverApp

Host/Path: URL or DiscoverApp

Host:Port/Path: URL or DiscoverApp

可以key中使用()进行正则匹配分组，然后在value中使用$1、$2...进行替换


### 重定向（rewrite）配置

以下是一个示例（配置文件env.yml）：

```yaml
gateway:
  rewrites:
    xxx.com/xxx/(.*): /xxx2/$1
    xxx.com/(.*): https://xxx2.com/$1
```


### 服务发现（使用 https://github/ssgo/discover 的配置）

如果有需要反向代理的服务（不确定IP、节点数量），可以使用 ssgo/discover 进行注册，支持在 proxies 中以服务名称进行配置

必须依赖一个可用的redis

以下是一个示例（配置文件env.yml）：

```yaml
discover:
  registry: redis://:xxxx@localhost:6379/15
```


### 动态配置（基于discover的redis）

在 redis 中配置Hash Key “_proxies”、“_rewrites”，默认会在10秒（可配置）内自动更新

如果希望立刻更新，可以同时向频道 “_CH_proxies”、“_CH_rewrites” 推送任意数据 
