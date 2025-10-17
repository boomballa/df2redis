package com.netease.nim.camellia.redis.base.resource;

import com.netease.nim.camellia.core.model.Resource;

import java.util.List;

/**
 * 格式如下：
 * 1、没有密码
 * redis-sentinel-slaves://@host:port,host:port,host:port/masterName?withMaster=false
 * 2、有密码
 * redis-sentinel-slaves://password@host:port,host:port,host:port/masterName?withMaster=false
 * 3、有密码且有账号
 * redis-sentinel-slaves://username:password@host:port,host:port,host:port/masterName?withMaster=false
 * <p>
 * only for read
 * <p>
 * Created by caojiajun on 2021/4/7
 */
public class RedisSentinelSlavesResource extends Resource {

    private final String master;
    private final List<RedisSentinelResource.Node> nodes;
    private final String password;
    private final boolean withMaster;
    private final String userName;
    private final int db;
    private final String sentinelUserName;
    private final String sentinelPassword;
    private final boolean sentinelSSL;

    public RedisSentinelSlavesResource(String master, List<RedisSentinelResource.Node> nodes, String userName, String password,
                                       boolean withMaster, int db, String sentinelUserName, String sentinelPassword, boolean sentinelSSL) {
        this.master = master;
        this.nodes = nodes;
        this.password = password;
        this.withMaster = withMaster;
        this.userName = userName;
        this.db = db;
        this.sentinelUserName = sentinelUserName;
        this.sentinelPassword = sentinelPassword;
        this.sentinelSSL = sentinelSSL;
        StringBuilder url = new StringBuilder();
        url.append(RedisType.RedisSentinelSlaves.getPrefix());
        if (userName != null && password != null) {
            url.append(userName).append(":").append(password);
        } else if (userName == null && password != null) {
            url.append(password);
        }
        url.append("@");
        for (RedisSentinelResource.Node node : nodes) {
            url.append(node.getHost()).append(":").append(node.getPort());
            url.append(",");
        }
        url.deleteCharAt(url.length() - 1);
        url.append("/");
        url.append(master);
        url.append("?withMaster=").append(withMaster).append("&");
        if (db > 0) {
            url.append("db=").append(db).append("&");
        }
        if (sentinelUserName != null) {
            url.append("sentinelUserName=").append(sentinelUserName).append("&");
        }
        if (sentinelPassword != null) {
            url.append("sentinelPassword=").append(sentinelPassword).append("&");
        }
        if (sentinelSSL) {
            url.append("sentinelSSL=true").append("&");
        }
        url.deleteCharAt(url.length() - 1);
        this.setUrl(url.toString());
    }

    public RedisSentinelSlavesResource(String master, List<RedisSentinelResource.Node> nodes, String userName, String password,
                                       boolean withMaster, int db, String sentinelUserName, String sentinelPassword) {
        this(master, nodes, userName, password, withMaster, db, sentinelUserName, sentinelPassword, false);
    }

    public RedisSentinelSlavesResource(String master, List<RedisSentinelResource.Node> nodes, String userName, String password, boolean withMaster, int db) {
        this(master, nodes, userName, password, withMaster, db, null, null);
    }

    public RedisSentinelSlavesResource(String master, List<RedisSentinelResource.Node> nodes, String userName, String password, boolean withMaster) {
        this(master, nodes, userName, password, withMaster, 0);
    }

    public RedisSentinelSlavesResource(String master, List<RedisSentinelResource.Node> nodes, String password, boolean withMaster) {
        this(master, nodes, null, password, withMaster);
    }

    public String getMaster() {
        return master;
    }

    public List<RedisSentinelResource.Node> getNodes() {
        return nodes;
    }

    public String getPassword() {
        return password;
    }

    public boolean isWithMaster() {
        return withMaster;
    }

    public String getUserName() {
        return userName;
    }

    public int getDb() {
        return db;
    }

    public String getSentinelUserName() {
        return sentinelUserName;
    }

    public String getSentinelPassword() {
        return sentinelPassword;
    }

    public boolean isSentinelSSL() {
        return sentinelSSL;
    }
}
