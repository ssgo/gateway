module github.com/ssgo/gateway

go 1.12

require (
	github.com/gomodule/redigo v1.9.2
	github.com/ssgo/config v1.7.7
	github.com/ssgo/discover v1.7.8
	github.com/ssgo/log v1.7.7
	github.com/ssgo/redis v1.7.7
	github.com/ssgo/s v1.7.12
	github.com/ssgo/standard v1.7.7
	github.com/ssgo/u v1.7.7
)

replace (
	github.com/ssgo/discover v1.7.7 => ../discover
	github.com/ssgo/s v1.7.11 => ../s
)
