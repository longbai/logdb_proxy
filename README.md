# grafana proxy

此工具用来使grafana能够无缝对接pandora logdb

## 主要作用

通过代理方式转换路径，变成logdb可以使用的API

## 支持功能
1. Kibana 查询可以, 前缀logdb
2. 支持三个接口
   1. GET  /[index-pattern]/_stats
   2. GET /[index-pattern]/_mapping
   3. POST /_msearch
   前缀logdb


## 注意事项

1. 该接口本身没有鉴权，所以必须不能暴露在公网上；
2. grafana发送的请求头部包含base64 Encoding的ak/sk，所以该proxy和grafana只能走机器内部通信；