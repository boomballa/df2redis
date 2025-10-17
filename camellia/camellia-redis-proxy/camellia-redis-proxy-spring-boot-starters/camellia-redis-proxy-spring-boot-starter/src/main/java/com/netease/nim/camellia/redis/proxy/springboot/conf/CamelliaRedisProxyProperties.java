package com.netease.nim.camellia.redis.proxy.springboot.conf;

import com.netease.nim.camellia.redis.proxy.conf.Constants;
import com.netease.nim.camellia.redis.proxy.netty.NettyTransportMode;
import org.springframework.boot.context.properties.ConfigurationProperties;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Created by caojiajun on 2019/11/13.
 */
@ConfigurationProperties(prefix = "camellia-redis-proxy")
public class CamelliaRedisProxyProperties {

    /**
     * proxy的密码
     */
    private String password;

    /**
     * proxy的port，如果没有指定，则使用server.port
     * 如果设置为Constants.Server.serverPortRandSig，则会随机选择一个可用端口
     */
    private int port = -1;

    /**
     * proxy的tls的port，如果小于等于0，则表示不启用tls
     */
    private int tlsPort = -1;

    /**
     * proxy使用unix-domain-socket启动的文件路径
     */
    private String udsPath = "";

    /**
     * proxy的http的port，如果小于等于0，则表示不启用http
     */
    private int httpPort = -1;

    /**
     * proxy的名字，用于注册到注册中心，如果没有指定，则使用spring.application.name
     */
    private String applicationName = "";

    /**
     * 是否开启proxy-protocol协议解析
     */
    private boolean proxyProtocolEnable = Constants.Server.proxyProtocolEnable;

    /**
     * 开启proxy-protocol协议的端口，多个端口用逗号分隔，如果不设置，则默认所有端口都开启
     */
    private String proxyProtocolPorts = "";

    /**
     * proxy tls provider的className
     */
    private String proxyFrontendTlsProviderClassName = Constants.Server.proxyFrontendTlsProviderClassName;

    /**
     * proxy的console port，会起一个console http server，可以自己实现一些自定义指令
     * 如果设置为Constants.Server.consolePortRandSig，则会随机选择一个可用端口
     */
    private int consolePort = Constants.Server.consolePort;

    /**
     * proxy是否启用cluster模式，伪装成redis-cluster
     */
    private boolean clusterModeEnable = Constants.Server.clusterModeEnable;

    /**
     * proxy是否启用sentinel模式，伪装成redis-sentinel
     */
    private boolean sentinelModeEnable = Constants.Server.sentinelModeEnable;

    /**
     * cluster模式下的cport
     */
    private int cport = -1;

    /**
     * cport的password
     */
    private String cportPassword;

    /**
     * cluster模式的实现方式，可以自定义
     */
    private String clusterModeProviderClassName = Constants.Server.clusterModeProviderClassName;

    /**
     * 是否开启监控，会记录每个命令的调用次数，see @RedisMonitor
     */
    private boolean monitorEnable = Constants.Server.monitorEnable;

    /**
     * 监控时间间隔，每个间隔生成一份监控数据，会打印到日志里，也可以用户自己选择输出到其他地方，see @RedisMonitor
     */
    private int monitorIntervalSeconds = Constants.Server.monitorIntervalSeconds;

    /**
     * 监控数据的回调
     */
    private String monitorCallbackClassName = Constants.Server.monitorCallbackClassName;

    /**
     * 认证处理逻辑的className
     */
    private String clientAuthProviderClassName = Constants.Server.clientAuthByConfigProvider;

    /**
     * 上游（后端）客户端工厂
     */
    private String upstreamClientTemplateFactoryClassName = Constants.Server.upstreamClientTemplateFactoryClassName;

    /**
     * 自定义配置的loader
     */
    private String proxyDynamicConfLoaderClassName = Constants.Server.proxyDynamicConfLoaderClassName;

    /**
     * 内存队列工厂
     */
    private String queueFactoryClassName = Constants.Server.queueFactoryClassName;

    /**
     * 插件
     */
    private List<String> plugins = new ArrayList<>();

    /**
     * netty的io模式，默认nio
     */
    private NettyTransportMode nettyTransportMode = Constants.Server.nettyTransportMode;

    /**
     * netty相关参数
     */
    private NettyProperties netty = new NettyProperties();

    /**
     * 自定义参数，可以通过ProxyDynamicConf获取
     * 可以被camellia-redis-proxy.properties覆盖
     */
    private Map<String, String> config = new HashMap<>();

    /**
     * 转发配置
     */
    private TranspondProperties transpond = new TranspondProperties();

    public String getPassword() {
        return password;
    }

    public void setPassword(String password) {
        this.password = password;
    }

    public int getPort() {
        return port;
    }

    public void setPort(int port) {
        this.port = port;
    }

    public int getTlsPort() {
        return tlsPort;
    }

    public void setTlsPort(int tlsPort) {
        this.tlsPort = tlsPort;
    }

    public String getUdsPath() {
        return udsPath;
    }

    public void setUdsPath(String udsPath) {
        this.udsPath = udsPath;
    }

    public int getHttpPort() {
        return httpPort;
    }

    public void setHttpPort(int httpPort) {
        this.httpPort = httpPort;
    }

    public String getApplicationName() {
        return applicationName;
    }

    public void setApplicationName(String applicationName) {
        this.applicationName = applicationName;
    }

    public String getProxyFrontendTlsProviderClassName() {
        return proxyFrontendTlsProviderClassName;
    }

    public void setProxyFrontendTlsProviderClassName(String proxyFrontendTlsProviderClassName) {
        this.proxyFrontendTlsProviderClassName = proxyFrontendTlsProviderClassName;
    }

    public int getConsolePort() {
        return consolePort;
    }

    public void setConsolePort(int consolePort) {
        this.consolePort = consolePort;
    }

    public boolean isClusterModeEnable() {
        return clusterModeEnable;
    }

    public void setClusterModeEnable(boolean clusterModeEnable) {
        this.clusterModeEnable = clusterModeEnable;
    }

    public boolean isSentinelModeEnable() {
        return sentinelModeEnable;
    }

    public void setSentinelModeEnable(boolean sentinelModeEnable) {
        this.sentinelModeEnable = sentinelModeEnable;
    }

    public int getCport() {
        return cport;
    }

    public void setCport(int cport) {
        this.cport = cport;
    }

    public String getCportPassword() {
        return cportPassword;
    }

    public void setCportPassword(String cportPassword) {
        this.cportPassword = cportPassword;
    }

    public boolean isProxyProtocolEnable() {
        return proxyProtocolEnable;
    }

    public void setProxyProtocolEnable(boolean proxyProtocolEnable) {
        this.proxyProtocolEnable = proxyProtocolEnable;
    }

    public String getProxyProtocolPorts() {
        return proxyProtocolPorts;
    }

    public void setProxyProtocolPorts(String proxyProtocolPorts) {
        this.proxyProtocolPorts = proxyProtocolPorts;
    }

    public String getClusterModeProviderClassName() {
        return clusterModeProviderClassName;
    }

    public void setClusterModeProviderClassName(String clusterModeProviderClassName) {
        this.clusterModeProviderClassName = clusterModeProviderClassName;
    }

    public boolean isMonitorEnable() {
        return monitorEnable;
    }

    public void setMonitorEnable(boolean monitorEnable) {
        this.monitorEnable = monitorEnable;
    }

    public int getMonitorIntervalSeconds() {
        return monitorIntervalSeconds;
    }

    public void setMonitorIntervalSeconds(int monitorIntervalSeconds) {
        this.monitorIntervalSeconds = monitorIntervalSeconds;
    }

    public String getMonitorCallbackClassName() {
        return monitorCallbackClassName;
    }

    public void setMonitorCallbackClassName(String monitorCallbackClassName) {
        this.monitorCallbackClassName = monitorCallbackClassName;
    }

    public String getClientAuthProviderClassName() {
        return clientAuthProviderClassName;
    }

    public void setClientAuthProviderClassName(String clientAuthProviderClassName) {
        this.clientAuthProviderClassName = clientAuthProviderClassName;
    }

    public String getUpstreamClientTemplateFactoryClassName() {
        return upstreamClientTemplateFactoryClassName;
    }

    public void setUpstreamClientTemplateFactoryClassName(String upstreamClientTemplateFactoryClassName) {
        this.upstreamClientTemplateFactoryClassName = upstreamClientTemplateFactoryClassName;
    }

    public String getProxyDynamicConfLoaderClassName() {
        return proxyDynamicConfLoaderClassName;
    }

    public void setProxyDynamicConfLoaderClassName(String proxyDynamicConfLoaderClassName) {
        this.proxyDynamicConfLoaderClassName = proxyDynamicConfLoaderClassName;
    }

    public List<String> getPlugins() {
        return plugins;
    }

    public void setPlugins(List<String> plugins) {
        this.plugins = plugins;
    }

    public NettyTransportMode getNettyTransportMode() {
        return nettyTransportMode;
    }

    public void setNettyTransportMode(NettyTransportMode nettyTransportMode) {
        this.nettyTransportMode = nettyTransportMode;
    }

    public String getQueueFactoryClassName() {
        return queueFactoryClassName;
    }

    public void setQueueFactoryClassName(String queueFactoryClassName) {
        this.queueFactoryClassName = queueFactoryClassName;
    }

    public NettyProperties getNetty() {
        return netty;
    }

    public void setNetty(NettyProperties netty) {
        this.netty = netty;
    }

    public Map<String, String> getConfig() {
        return config;
    }

    public void setConfig(Map<String, String> config) {
        this.config = config;
    }

    public TranspondProperties getTranspond() {
        return transpond;
    }

    public void setTranspond(TranspondProperties transpond) {
        this.transpond = transpond;
    }
}
