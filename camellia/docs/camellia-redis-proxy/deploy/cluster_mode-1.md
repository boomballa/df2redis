
## cluster-mode-1

* 无中心化的cluster-mode方案，基于节点间的心跳完成节点的互相发现和最终一致

### 应用场景

* 期望使用redis-proxy，客户端支持redis-cluster协议
* 提前知道proxy-ip的情况下，可以在不需要外部组件（如lvs、slb、zk、etcd等）的情况下，让proxy组成高可用集群（即能平滑扩缩容、节点宕机能自动fail-over）

### 配置：

```yaml
server:
  port: 6380
spring:
  application:
    name: camellia-redis-proxy-server

camellia-redis-proxy:
  #port: -6379 #优先级高于server.port，如果缺失，则使用server.port，如果设置为-6379则会随机一个可用端口
  #application-name: camellia-redis-proxy-server  #优先级高于spring.application.name，如果缺失，则使用spring.application.name
  console-port: 16379 #console端口，默认是16379，如果设置为-16379则会随机一个可用端口
  cport: 16380 #cluster-mode下的心跳端口，默认是proxy端口+10000
  password: pass123
  cluster-mode-enable: true #cluster-mode，把proxy伪装成cluster，需要在camellia-redis-proxy.properties配置proxy.cluster.mode.nodes
  transpond:
    type: local
    local:
      resource: redis://@127.0.0.1:6379
```     
随后你需要在camellia-redis-proxy.properties里选择若干个个proxy节点配置，如下：
```
#随机挑选几个proxy节点配置即可（都配上当然更好，不需要全部配置，但是配置的必须是活着的节点，否则重启会失败），格式为ip:port@cport
proxy.cluster.mode.nodes=192.168.3.218:6380@16380,192.168.3.218:6390@16390
```
依次启动所有proxy即可    
节点宕机、节点扩容，proxy集群内部会通过心跳自动感知（心跳通过cport和自定义的redis协议去实现）

其他可以配置的参数：
```
#proxy节点间的心跳间隔，表示了心跳请求的频率
proxy.cluster.mode.heartbeat.interval.seconds=5
#proxy节点间的心跳超时，20s没有收到心跳，则会剔除该节点
proxy.cluster.mode.heartbeat.timeout.seconds=20
#proxy节点的ip，默认会自动获取本机ip，一般不需要配置
proxy.cluster.mode.current.node.host=10.1.1.1
```

伪redis-cluster模式下常见操作的逻辑如下：
```
1、启动时
1）取配置文件中配置的地址串
2）发送心跳给地址串中的所有地址（排除自己）
3）等待所有地址响应，如果有未响应的，会一直重试，直到成功  
4）标识自己为ONLINE


2、下线时
1）标识自己为OFFLINE
2）发送心跳告知所有其他节点


3、重新上线时
1）标识自己为ONLINE
2）发送心跳告知所有其他节点


4、扩容（有地址串外的节点启动）
1）取地址串中的所有地址
2）发送心跳给地址串中的所有地址（里面没有自己）
3）等待所有地址响应，如果有未响应的，会一直重试，直到成功  
4）标识自己为ONLINE

proxy通过接收心跳，会维护内存中的proxy节点列表，用于返回给客户端的刷新拓扑请求，或者适当时给客户端发一个MOVE的重定向，把流量均衡到其他proxy节点
```
