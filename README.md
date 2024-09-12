# 基于 github/ssgo 的一个网关应用（类似nginx）

## 安装

```shell
go install github.com/ssgo/gateway@latest
```

或直接下载对应操作系统的二进制程序：

#### Linux (amd64):

```shell
curl -o gateway https://apigo.cc/gateway/gateway.linux.amd64 && chmod +x gateway
```

#### Linux (arm64):

```shell
curl -o gateway https://apigo.cc/gateway/gateway.linux.amd64 && chmod +x gateway
```

#### Mac (Intel):

```shell
curl -o gateway https://apigo.cc/gateway/gateway.darwin.amd64 && chmod +x gateway
```

#### Mac (Apple):

```shell
curl -o gateway https://apigo.cc/gateway/gateway.darwin.arm64 && chmod +x gateway
```

Windows:

https://apigo.cc/gateway/gateway.windows.amd64.exe

https://apigo.cc/gateway/gateway.windows.arm64.exe


## 配置 (在当前目录下创建env.yml)

```yaml
service:
  listen: 80|443
  ssl:
    xxx.com:
      certfile: xxx.pem
      keyfile: xxx.key
discover:
  registry: redis://:password@localhost:6379/15
log:
  file: out.log
  splittag: 20060102
gateway:
  static:
    /: www
    xxx.com: xxx
    xxx.com/aaa: /opt/www/aaa
  proxy:
    xxx.com: http://127.0.0.1:3000
    xxx.com/(.*): https://$1
    xxx.com/xxx: discoverAppName
    xxx2.com/xxx/(.*): http://127.0.0.1:8001/xxx/$1
    xxx3.com:8080: discoverAppName
  rewrite:
    xxx.com/xxx/(.*): /xxx2/$1
    xxx.com/(.*): https://xxx2.com/$1
```

## 测试配置

```shell
./gateway -t
````

```shell
./gateway test
````

## 启动

```shell
./gateway start
````

## 停止

```shell
./gateway stop
```

## 重启

```shell
./gateway restart
```

## 重新加载配置

```shell
./gateway -r
```

```shell
./gateway reload
```

```shell
kill -30 [pid]
```

## 监听（使用 https://github/ssgo/s 的配置）

以下是一个示例（配置文件env.yml）：

```yaml
service:
  listen: 80|443
  ssl:
    xxx.com:
      certfile: xxx.pem
      keyfile: xxx.key
```


## 日志（使用 https://github/ssgo/log 的配置）

以下是一个示例（配置文件env.yml）：

```yaml
log:
  file: out.log
  splittag: 20060102
```

### 安装日志查看工具（参考 https://github.com/ssgo/tool ）

```shell
go install github.com/ssgo/tool/logv@latest
```

或直接下载对应操作系统的二进制程序：（以Linux为例，更多下载地址可访问 https://github.com/ssgo/tool ）

```shell
curl -o logv https://apigo.cc/tool/logv.linux.amd64 && chmod +x logv
```


## 静态文件配置

以下是一个示例（配置文件env.yml）：

```yaml
gateway:
  static:
    /: www
    xxx.com: xxx
    xxx.com/aaa: /opt/www/aaa
```


## 反向代理配置

以下是一个示例（配置文件env.yml）：

```yaml
gateway:
  proxy:
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


## 重定向（rewrite）配置

以下是一个示例（配置文件env.yml）：

```yaml
gateway:
  rewrite:
    xxx.com/xxx/(.*): /xxx2/$1
    xxx.com/(.*): https://xxx2.com/$1
```


## 服务发现（使用 https://github/ssgo/discover 的配置）

如果有需要反向代理的服务（不确定IP、节点数量），可以使用 ssgo/discover 进行注册，支持在 proxies 中以服务名称进行配置

必须依赖一个可用的redis

以下是一个示例（配置文件env.yml）：

```yaml
discover:
  registry: redis://:xxxx@localhost:6379/15
```


## 动态配置（基于discover中配置的redis）

在 redis 中设置Hash Key “_proxy”、“_rewrite”、“_static”（配置文件中的部分不需要重复设置，但可以被覆盖）

默认每10秒更新一次，可以在配置文件中修改（单位 秒） 

```yaml
gateway:
  checkInterval: 10
```

如果希望立即更新，可以向对应频道 “_CH_proxy”、“_CH_rewrite”、“_CH_static” 推送任意数据

如果存在多个 gateway，可以设置 prefix 来区分，例如设置一个 prefix 为 “AAA_”

```yaml
gateway:
  prefix: AAA_
```

则对应的 redis key 为 “_AAA_proxy”、“_AAA_rewrite”、“_AAA_static”、“_CH_AAA_proxy”、“_CH_AAA_rewrite”、“_CH_AAA_static”